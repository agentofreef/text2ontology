package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
)

// Keyword Triage API.
//
// Surface for the /dax/ontology/lakehouse-keyword-triage page. Lets operators
// see which tokens (from ont_agent_annotation) are unbound or partially bound,
// and assign each token to one or more lakehouse_keyword rows representing
// Od-alias / column-alias / value-alias / metric-intent triggers / stopword.
//
// Endpoints:
//   GET    /api/ontology/keyword-triage/queue?projectId=&badge=
//   GET    /api/ontology/keyword-triage/token?projectId=&token=
//   POST   /api/ontology/keyword-triage/assign      body: see TriageAssign
//   GET    /api/ontology/metric-intents/{id}/triggers
//   DELETE /api/ontology/metric-intents/{id}/triggers/{kwId}

// ── Queue ────────────────────────────────────────────────────────────────

// handleTriageQueue returns the left-rail token list with badge classification.
//
// Token frequency comes from ont_agent_annotation.tokens (pipe-separated),
// joined against lakehouse_keyword (case-insensitive) to determine how many
// rows already bind this token and what kind.
//
// Badge precedence (one badge per token):
//
//	ignored  — at least one row has is_stopword=true
//	partial  — at least one row has any anchor (object_id|property_id|metric_id)
//	floating — row(s) exist but none has an anchor and none is stopword
//	orphan   — no lakehouse_keyword row at all
func handleTriageQueue(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)
		if !IsValidUUID(pid) {
			ListResp(w, []M{}, 0)
			return
		}
		badge := r.URL.Query().Get("badge") // optional filter
		search := strings.TrimSpace(r.URL.Query().Get("search"))

		// Aggregate token frequency from annotations.
		// LATERAL split + filter: ignore empty pieces and pure-whitespace pieces.
		const sqlText = `
		WITH token_freq AS (
		    SELECT LOWER(TRIM(tok)) AS token,
		           COUNT(*)::int    AS cnt,
		           MAX(updated_at)  AS last_seen
		    FROM ont_agent_annotation,
		         LATERAL unnest(string_to_array(COALESCE(tokens,''), '|')) AS tok
		    WHERE project_id = $1
		      AND tokens IS NOT NULL
		      AND TRIM(tok) <> ''
		    GROUP BY LOWER(TRIM(tok))
		),
		token_bindings AS (
		    -- A keyword row can be addressed via its canonical keyword text
		    -- OR via any entry in its aliases array. Unnest both into a
		    -- common LOWER(token) bucket so the badge counts reflect every
		    -- spelling the operator might triage.
		    SELECT LOWER(t)                              AS token,
		           COUNT(*) FILTER (
		               WHERE COALESCE(is_stopword,false) = false
		                 AND (property_id IS NOT NULL
		                   OR object_id IS NOT NULL
		                   OR metric_id IS NOT NULL)
		           )::int                                AS anchor_count,
		           COUNT(*) FILTER (
		               WHERE COALESCE(is_stopword,false) = false
		                 AND property_id      IS NULL
		                 AND object_id        IS NULL
		                 AND metric_id        IS NULL
		           )::int                                AS floating_count,
		           BOOL_OR(COALESCE(is_stopword,false))  AS has_stopword
		    FROM lakehouse_keyword,
		         LATERAL unnest(ARRAY[keyword] || COALESCE(aliases, '{}'::text[])) AS t
		    WHERE project_id = $1
		    GROUP BY LOWER(t)
		)
		SELECT tf.token,
		       tf.cnt,
		       COALESCE(tb.anchor_count, 0),
		       COALESCE(tb.floating_count, 0),
		       COALESCE(tb.has_stopword, false)
		FROM token_freq tf
		LEFT JOIN token_bindings tb USING (token)
		WHERE ($2 = '' OR tf.token LIKE '%'||LOWER($2)||'%')
		ORDER BY tf.cnt DESC, tf.last_seen DESC
		LIMIT 500`

		rows, err := db.Query(sqlText, pid, search)
		if err != nil {
			JsonResp(w, M{"error": err.Error(), "data": []M{}, "total": 0,
				"counts": M{"orphan": 0, "floating": 0, "partial": 0, "ignored": 0}})
			return
		}
		defer rows.Close()

		counts := map[string]int{"orphan": 0, "floating": 0, "partial": 0, "ignored": 0}
		var data []M
		for rows.Next() {
			var token string
			var cnt, anchorCount, floatingCount int
			var hasStopword bool
			if err := rows.Scan(&token, &cnt, &anchorCount, &floatingCount, &hasStopword); err != nil {
				continue
			}
			b := classifyTriageBadge(anchorCount, floatingCount, hasStopword)
			counts[b]++
			if badge != "" && badge != b {
				continue
			}
			data = append(data, M{
				"token":         token,
				"count":         cnt,
				"anchorCount":   anchorCount,
				"floatingCount": floatingCount,
				"badge":         b,
			})
		}
		if data == nil {
			data = []M{}
		}
		JsonResp(w, M{"data": data, "total": len(data), "counts": counts})
	}
}

// classifyTriageBadge maps a token's binding stats to one of four states.
// See handleTriageQueue header for precedence rules.
func classifyTriageBadge(anchorCount, floatingCount int, hasStopword bool) string {
	switch {
	case hasStopword:
		return "ignored"
	case anchorCount > 0:
		return "partial"
	case floatingCount > 0:
		return "floating"
	default:
		return "orphan"
	}
}

// ── Token detail ─────────────────────────────────────────────────────────

// handleTriageToken returns the right-panel context for a single token:
//   - existing lakehouse_keyword bindings for this token
//   - top-N annotations containing this token, with the per-token mappings of
//     EVERY token in those questions (so the reviewer sees what other tokens
//     in the same question already resolve to)
func handleTriageToken(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)
		token := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("token")))
		if !IsValidUUID(pid) || token == "" {
			JsonResp(w, M{"token": token, "bindings": []M{}, "questions": []M{}})
			return
		}

		bindings := loadTriageBindings(db, pid, token)
		questions := loadTriageQuestions(db, pid, token, 10)

		JsonResp(w, M{
			"token":     token,
			"bindings":  bindings,
			"questions": questions,
		})
	}
}

func loadTriageBindings(db *sql.DB, projectID, token string) []M {
	const q = `
		SELECT lk.id::text,
		       lk.keyword,
		       COALESCE(lk.aliases, '{}'::text[]),
		       COALESCE(lk.is_stopword, false),
		       COALESCE(lk.is_column_name, false),
		       lk.property_id::text,  COALESCE(p.name,''),
		       lk.object_id::text,    COALESCE(o.name,''),
		       lk.metric_id::text, COALESCE(mi.name,''),
		       COALESCE(po.name,''), COALESCE(mio.name,''),
		       lk.synced_at
		FROM lakehouse_keyword lk
		LEFT JOIN ont_property p       ON lk.property_id      = p.id
		LEFT JOIN ont_object_type po   ON p.object_type_id    = po.id
		LEFT JOIN ont_object_type o    ON lk.object_id        = o.id
		LEFT JOIN lakehouse_metric mi  ON lk.metric_id        = mi.id
		LEFT JOIN ont_object_type mio  ON mi.object_id        = mio.id
		WHERE lk.project_id = $1
		  AND (
		      LOWER(lk.keyword) = $2
		   OR EXISTS (
		         SELECT 1 FROM unnest(COALESCE(lk.aliases,'{}'::text[])) a
		         WHERE LOWER(a) = $2)
		  )
		ORDER BY lk.synced_at DESC`

	rows, err := db.Query(q, projectID, token)
	if err != nil {
		return []M{}
	}
	defer rows.Close()

	out := []M{}
	for rows.Next() {
		var id, keyword string
		var aliases pq.StringArray
		var isStop, isCol bool
		var propID, propName, objID, objName, intentID, intentName string
		var propOd, intentOd string
		var syncedAt sql.NullTime
		if err := rows.Scan(&id, &keyword, &aliases, &isStop, &isCol,
			&propID, &propName, &objID, &objName, &intentID, &intentName,
			&propOd, &intentOd, &syncedAt); err != nil {
			continue
		}
		// One row → one binding kind. Determine which based on populated cols.
		kind := "floating"
		switch {
		case isStop:
			kind = "ignore"
		case intentID != "" && intentID != "00000000-0000-0000-0000-000000000000":
			kind = "intent_trigger"
		case objID != "" && objID != "00000000-0000-0000-0000-000000000000":
			kind = "od_alias"
		case propID != "" && propID != "00000000-0000-0000-0000-000000000000" && isCol:
			kind = "column_alias"
		case propID != "" && propID != "00000000-0000-0000-0000-000000000000":
			kind = "value_alias"
		}
		out = append(out, M{
			"id":           id,
			"keyword":      keyword,
			"aliases":      []string(aliases),
			"kind":         kind,
			"propertyId":   propID,
			"propertyName": propName,
			"propertyOd":   propOd,
			"objectId":     objID,
			"objectName":   objName,
			"intentId":     intentID,
			"intentName":   intentName,
			"intentOd":     intentOd,
			"isColumnName": isCol,
			"isStopword":   isStop,
		})
	}
	return out
}

func loadTriageQuestions(db *sql.DB, projectID, token string, limit int) []M {
	// Questions whose tokens contain the target (case-insensitive on a piece).
	// We keep the original tokens string + tokenMappings JSONB so the frontend
	// can render every other token's binding alongside the target token.
	q := `
		SELECT id::text, question, COALESCE(tokens,''),
		       COALESCE(token_mappings::text,'[]'),
		       status, created_at
		FROM ont_agent_annotation
		WHERE project_id = $1
		  AND tokens IS NOT NULL
		  AND EXISTS (
		      SELECT 1 FROM unnest(string_to_array(tokens,'|')) t
		      WHERE LOWER(TRIM(t)) = $2
		  )
		ORDER BY updated_at DESC
		LIMIT $3`

	rows, err := db.Query(q, projectID, token, limit)
	if err != nil {
		return []M{}
	}
	defer rows.Close()

	out := []M{}
	for rows.Next() {
		var id, question, tokensStr, mappingsJSON string
		var status bool
		var ca sql.NullTime
		if err := rows.Scan(&id, &question, &tokensStr, &mappingsJSON, &status, &ca); err != nil {
			continue
		}
		var mappings interface{}
		_ = json.Unmarshal([]byte(mappingsJSON), &mappings)
		if mappings == nil {
			mappings = []interface{}{}
		}
		tokenList := []string{}
		for _, t := range strings.Split(tokensStr, "|") {
			t = strings.TrimSpace(t)
			if t != "" {
				tokenList = append(tokenList, t)
			}
		}
		var createdAt string
		if ca.Valid {
			createdAt = ca.Time.Format("2006-01-02T15:04:05Z07:00")
		}
		out = append(out, M{
			"id":            id,
			"question":      question,
			"tokens":        tokenList,
			"tokenMappings": mappings,
			"status":        status,
			"createdAt":     createdAt,
		})
	}
	return out
}

// ── Assign ───────────────────────────────────────────────────────────────

// TriageAssignBinding is one element in the assign payload's bindings array.
// At most one of objectId / propertyId / intentId is meaningful per row,
// matched by the kind discriminator.
type TriageAssignBinding struct {
	Kind         string `json:"kind"`         // od_alias | column_alias | value_alias | intent_trigger
	ObjectID     string `json:"objectId"`     // od_alias
	PropertyID   string `json:"propertyId"`   // column_alias | value_alias
	IntentID     string `json:"intentId"`     // intent_trigger
	Value        string `json:"value"`        // value_alias — informational, stored in keyword text if differs
	IsColumnName bool   `json:"isColumnName"` // forced to true for column_alias
}

// handleTriageAssign synchronises the lakehouse_keyword rows for one token
// with the desired binding set. Diff against existing rows: rows present in
// the new payload survive, rows missing are removed, new rows are inserted.
//
// If `ignore: true` is set, all rows for this token are removed and a single
// stopword row is inserted in their place.
func handleTriageAssign(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body := ReadBody(r)
		projectID := StrVal(body, "projectId")
		token := strings.TrimSpace(StrVal(body, "token"))
		if !IsValidUUID(projectID) || token == "" {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"error": "projectId and token are required"})
			return
		}
		// Body projectId is not gated by the middleware — verify access.
		if !authmw.EnforceProjectAccess(w, r, db, projectID) {
			return
		}
		ignore := false
		if v, ok := body["ignore"].(bool); ok {
			ignore = v
		}

		tx, err := db.Begin()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		defer tx.Rollback()

		// Wipe all references to this token before re-applying the payload.
		// Triage owns the token's lifecycle; rebuilding from scratch keeps the
		// state consistent with the new bindings without diffing.
		//
		// Two layers must be cleared because column/value/od aliases now live on
		// the canonical row's `aliases[]` array (not as standalone rows):
		//   1. Standalone rows where keyword=token (legacy data + stopword rows).
		//   2. Any canonical row in this project that lists the token in
		//      aliases[] (and the matching row in lakehouse_keyword_alias_vector).
		if _, err := tx.Exec(
			`DELETE FROM lakehouse_keyword
			 WHERE project_id = $1 AND LOWER(keyword) = LOWER($2)`,
			projectID, token); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		if err := stripTokenFromAllAliases(tx, projectID, token); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"error": err.Error()})
			return
		}

		if ignore {
			// Stopword row needs an object_type_id (NOT NULL legacy column).
			// Pick any active Od for this project as a holder; recall ignores
			// stopword rows entirely so the choice has no runtime effect.
			holderOd, err := pickHolderOd(tx, projectID)
			if err != nil || holderOd == "" {
				log.Printf("triage assign(ignore=true): pickHolderOd failed token=%q err=%v holder=%q", token, err, holderOd)
				w.WriteHeader(http.StatusBadRequest)
				JsonResp(w, M{"error": "no active Od to anchor stopword to"})
				return
			}
			if _, err := tx.Exec(`
				INSERT INTO lakehouse_keyword
				    (project_id, object_type_id, keyword, is_stopword)
				VALUES ($1, $2, $3, true)`,
				projectID, holderOd, token); err != nil {
				log.Printf("triage assign(ignore=true) INSERT failed token=%q holder=%q err=%v", token, holderOd, err)
				w.WriteHeader(http.StatusInternalServerError)
				JsonResp(w, M{"error": err.Error()})
				return
			}
		} else {
			bindings := parseTriageBindings(body["bindings"])
			for _, b := range bindings {
				if err := insertTriageRow(tx, projectID, token, b); err != nil {
					log.Printf("triage assign INSERT failed token=%q kind=%q err=%v", token, b.Kind, err)
					w.WriteHeader(http.StatusBadRequest)
					JsonResp(w, M{"error": err.Error()})
					return
				}
			}
		}

		if err := tx.Commit(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		JsonResp(w, M{"success": true, "token": token})
	}
}

func parseTriageBindings(raw interface{}) []TriageAssignBinding {
	out := []TriageAssignBinding{}
	arr, ok := raw.([]interface{})
	if !ok {
		return out
	}
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		b := TriageAssignBinding{
			Kind:       StrVal(m, "kind"),
			ObjectID:   StrVal(m, "objectId"),
			PropertyID: StrVal(m, "propertyId"),
			IntentID:   StrVal(m, "intentId"),
			Value:      StrVal(m, "value"),
		}
		if v, ok := m["isColumnName"].(bool); ok {
			b.IsColumnName = v
		}
		out = append(out, b)
	}
	return out
}

// insertTriageRow records ONE binding for `token` by:
//  1. Finding (or creating) the canonical lakehouse_keyword row for that
//     anchor (Od / property column / property value / metric intent).
//     "Canonical" means the row whose keyword text equals the natural name
//     for that anchor (Od.name, property.name, the value string, intent.name).
//  2. Appending `token` to that row's aliases[] (and the alias_vector child
//     table) — unless `token` already equals the canonical keyword, in which
//     case the row alone is sufficient.
//
// This is a deliberate departure from the old "one row per spelling" model.
// Reasons:
//   - /lakehouse-objects/detail's "列别名" dialog reads the canonical row's
//     aliases[]; without consolidation each synonym appeared as an unrelated
//     keyword instead of as an alias of the canonical column ref.
//   - Vector recall's per-alias child table (lakehouse_keyword_alias_vector)
//     keeps the canonical keyword_vector clean — one embedding per concept,
//     not one per spelling.
//   - /lakehouse-keywords stops being flooded with synonym rows.
func insertTriageRow(tx *sql.Tx, projectID, token string, b TriageAssignBinding) error {
	switch b.Kind {
	case "od_alias":
		if !IsValidUUID(b.ObjectID) {
			return ErrBadBinding("od_alias requires objectId")
		}
		var odName string
		if err := tx.QueryRow(
			`SELECT name FROM ont_object_type WHERE id = $1`,
			b.ObjectID,
		).Scan(&odName); err != nil {
			return err
		}
		return recordAliasOnCanonical(tx,
			`SELECT id::text FROM lakehouse_keyword
			  WHERE project_id = $1 AND object_id = $2
			    AND property_id IS NULL AND metric_id IS NULL
			    AND COALESCE(is_stopword,false) = false
			    AND LOWER(keyword) = LOWER($3)
			  LIMIT 1`,
			[]interface{}{projectID, b.ObjectID, odName},
			`INSERT INTO lakehouse_keyword
			     (project_id, object_type_id, object_id, keyword)
			 VALUES ($1, $2, $2, $3) RETURNING id::text`,
			[]interface{}{projectID, b.ObjectID, odName},
			odName, token,
		)

	case "column_alias":
		if !IsValidUUID(b.PropertyID) {
			return ErrBadBinding("column_alias requires propertyId")
		}
		var odID, propName string
		if err := tx.QueryRow(
			`SELECT object_type_id::text, name FROM ont_property WHERE id = $1`,
			b.PropertyID,
		).Scan(&odID, &propName); err != nil {
			return err
		}
		return recordAliasOnCanonical(tx,
			`SELECT id::text FROM lakehouse_keyword
			  WHERE project_id = $1 AND property_id = $2
			    AND COALESCE(is_column_name,false) = true
			    AND COALESCE(is_stopword,false) = false
			  ORDER BY (LOWER(keyword) = LOWER($3)) DESC, synced_at DESC
			  LIMIT 1`,
			[]interface{}{projectID, b.PropertyID, propName},
			`INSERT INTO lakehouse_keyword
			     (project_id, object_type_id, property_id, keyword, is_column_name)
			 VALUES ($1, $2, $3, $4, true) RETURNING id::text`,
			[]interface{}{projectID, odID, b.PropertyID, propName},
			propName, token,
		)

	case "value_alias":
		if !IsValidUUID(b.PropertyID) {
			return ErrBadBinding("value_alias requires propertyId")
		}
		// Canonical = the actual data value (b.Value). Falls back to the token
		// itself only when no value was picked (rare — UI's value picker writes
		// b.Value). The canonical row is then keyword=value, is_column_name=false.
		canonical := strings.TrimSpace(b.Value)
		if canonical == "" {
			canonical = token
		}
		var odID string
		if err := tx.QueryRow(
			`SELECT object_type_id::text FROM ont_property WHERE id = $1`,
			b.PropertyID,
		).Scan(&odID); err != nil {
			return err
		}
		return recordAliasOnCanonical(tx,
			`SELECT id::text FROM lakehouse_keyword
			  WHERE project_id = $1 AND property_id = $2
			    AND COALESCE(is_column_name,false) = false
			    AND COALESCE(is_stopword,false) = false
			    AND LOWER(keyword) = LOWER($3)
			  LIMIT 1`,
			[]interface{}{projectID, b.PropertyID, canonical},
			`INSERT INTO lakehouse_keyword
			     (project_id, object_type_id, property_id, keyword, is_column_name)
			 VALUES ($1, $2, $3, $4, false) RETURNING id::text`,
			[]interface{}{projectID, odID, b.PropertyID, canonical},
			canonical, token,
		)

	case "intent_trigger":
		if !IsValidUUID(b.IntentID) {
			return ErrBadBinding("intent_trigger requires intentId")
		}
		var odID, intentName string
		if err := tx.QueryRow(
			`SELECT object_id::text, name FROM lakehouse_metric WHERE id = $1 AND deleted_at IS NULL AND mark = true`,
			b.IntentID,
		).Scan(&odID, &intentName); err != nil {
			return err
		}
		return recordAliasOnCanonical(tx,
			`SELECT id::text FROM lakehouse_keyword
			  WHERE project_id = $1 AND metric_id = $2
			    AND COALESCE(is_stopword,false) = false
			  ORDER BY (LOWER(keyword) = LOWER($3)) DESC, synced_at DESC
			  LIMIT 1`,
			[]interface{}{projectID, b.IntentID, intentName},
			`INSERT INTO lakehouse_keyword
			     (project_id, object_type_id, metric_id, keyword)
			 VALUES ($1, $2, $3, $4) RETURNING id::text`,
			[]interface{}{projectID, odID, b.IntentID, intentName},
			intentName, token,
		)
	}
	return ErrBadBinding("unknown binding kind: " + b.Kind)
}

// stripTokenFromAllAliases removes `token` (case-insensitive) from every
// canonical row's aliases[] in the project, plus the matching child rows in
// lakehouse_keyword_alias_vector. Called at the start of triage assign so the
// previous mapping is fully cleared before re-applying the new payload.
func stripTokenFromAllAliases(tx *sql.Tx, projectID, token string) error {
	if _, err := tx.Exec(`
		UPDATE lakehouse_keyword
		   SET aliases = ARRAY(
		       SELECT a FROM unnest(COALESCE(aliases,'{}'::text[])) a
		        WHERE LOWER(a) <> LOWER($2)
		   ),
		       synced_at = now()
		 WHERE project_id = $1
		   AND EXISTS (
		       SELECT 1 FROM unnest(COALESCE(aliases,'{}'::text[])) a
		        WHERE LOWER(a) = LOWER($2)
		   )`, projectID, token); err != nil {
		return err
	}
	_, err := tx.Exec(`
		DELETE FROM lakehouse_keyword_alias_vector lav
		 USING lakehouse_keyword lk
		 WHERE lav.keyword_id = lk.id
		   AND lk.project_id = $1
		   AND LOWER(lav.alias) = LOWER($2)`, projectID, token)
	return err
}

// recordAliasOnCanonical is the upsert primitive shared by all binding kinds.
//
// It first runs `lookupSQL` (must SELECT id::text and may return zero rows).
// If empty, `insertSQL` runs (must INSERT and RETURN id::text) to create the
// canonical row. Then, unless `token` already equals `canonical`
// (case-insensitive), `token` is appended to the row's aliases[] and a
// matching NULL-vector row is upserted into lakehouse_keyword_alias_vector
// so the next embedding-recompute job will fill it in.
func recordAliasOnCanonical(
	tx *sql.Tx,
	lookupSQL string, lookupArgs []interface{},
	insertSQL string, insertArgs []interface{},
	canonical, token string,
) error {
	var canonicalID string
	switch err := tx.QueryRow(lookupSQL, lookupArgs...).Scan(&canonicalID); err {
	case nil:
		// found — use it
	case sql.ErrNoRows:
		if err := tx.QueryRow(insertSQL, insertArgs...).Scan(&canonicalID); err != nil {
			return err
		}
	default:
		return err
	}

	if strings.EqualFold(strings.TrimSpace(token), strings.TrimSpace(canonical)) {
		return nil
	}

	if _, err := tx.Exec(`
		UPDATE lakehouse_keyword
		   SET aliases = ARRAY(
		       SELECT DISTINCT a FROM (
		           SELECT a FROM unnest(COALESCE(aliases,'{}'::text[])) a
		            WHERE LOWER(a) <> LOWER($2)
		           UNION ALL
		           SELECT $2
		       ) sub(a)
		       ORDER BY a
		   ),
		       synced_at = now()
		 WHERE id = $1`, canonicalID, token); err != nil {
		return err
	}

	_, err := tx.Exec(`
		INSERT INTO lakehouse_keyword_alias_vector (keyword_id, alias)
		VALUES ($1, $2)
		ON CONFLICT (keyword_id, alias) DO NOTHING`, canonicalID, token)
	return err
}

func pickHolderOd(tx *sql.Tx, projectID string) (string, error) {
	var id string
	err := tx.QueryRow(
		`SELECT id::text FROM ont_object_type
		 WHERE project_id = $1 AND COALESCE(mark, true) = true
		 ORDER BY created_at ASC LIMIT 1`,
		projectID,
	).Scan(&id)
	return id, err
}

// ErrBadBinding wraps a string error for invalid triage bindings.
type errBadBinding struct{ msg string }

func (e *errBadBinding) Error() string { return e.msg }

// ErrBadBinding constructs a typed error for invalid triage bindings.
func ErrBadBinding(msg string) error { return &errBadBinding{msg: msg} }

// ── Objects tree (Od → properties → keyword counts) ─────────────────────

// handleTriageObjectsTree returns every active Od + its active properties +
// per-property keyword count, in one query. Used by the triage workspace's
// visualizers (Od alias / column alias / value alias) so they don't have to
// stitch together /objects + /properties + /lakehouse-keywords client-side.
//
// The plain /api/ontology/objects endpoint returns Od metadata only — no
// embedded properties — which is why the visualizers were empty. This
// endpoint fixes that for the triage use case without bloating the generic
// objects endpoint for everyone else.
//
// GET /api/ontology/keyword-triage/objects-tree?projectId=
func handleTriageObjectsTree(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		pid := GetProjectID(r)
		if !IsValidUUID(pid) {
			ListResp(w, []M{}, 0)
			return
		}

		const q = `
		SELECT
		    o.id::text AS od_id,
		    o.name AS od_name,
		    COALESCE(o.display_name,'') AS od_display_name,
		    COALESCE(o.kind,'') AS od_kind,
		    COALESCE(o.description,'') AS od_description,
		    COALESCE(o.source_table,'') AS od_source_table,
		    COALESCE((
		        SELECT COUNT(*) FROM lakehouse_keyword lk
		        WHERE lk.object_type_id = o.id
		          AND COALESCE(lk.is_stopword,false) = false
		    ), 0)::int AS od_keyword_count,
		    COALESCE(props.json, '[]'::jsonb)::text AS properties_json
		FROM ont_object_type o
		LEFT JOIN LATERAL (
		    SELECT jsonb_agg(
		        jsonb_build_object(
		            'id',            p.id::text,
		            'name',          p.name,
		            'displayName',   COALESCE(p.display_name, ''),
		            'dataType',      COALESCE(p.data_type, ''),
		            'sourceColumn',  COALESCE(p.source_column, ''),
		            'description',   COALESCE(p.description, ''),
		            'isMachineCode', COALESCE(p.is_machine_code, false),
		            -- enum_values is a manual store on ont_property — usually empty.
		            'enumValues',    COALESCE(to_jsonb(p.enum_values), '[]'::jsonb),
		            -- existingValues comes from lakehouse_keyword rows where
		            -- is_column_name=false. These are the distinct values that
		            -- were materialised by the "同步" button in
		            -- /dax/ontology/lakehouse-objects/detail (which calls
		            -- /pbit-lakehouse/sync-property-keywords). This is the actual
		            -- source of truth for value-alias suggestions — it's what
		            -- /dax/ontology/lakehouse-keywords already shows. Cap at 200
		            -- to avoid blowing up the response for high-cardinality cols.
		            'existingValues', COALESCE((
		                SELECT jsonb_agg(DISTINCT lk.keyword)
		                FROM (
		                    SELECT keyword
		                    FROM lakehouse_keyword
		                    WHERE property_id = p.id
		                      AND COALESCE(is_column_name, false) = false
		                      AND COALESCE(is_stopword, false) = false
		                    ORDER BY keyword
		                    LIMIT 200
		                ) lk
		            ), '[]'::jsonb),
		            'keywordCount',  COALESCE((
		                SELECT COUNT(*) FROM lakehouse_keyword lk
		                WHERE lk.property_id = p.id
		                  AND COALESCE(lk.is_stopword,false) = false
		            ), 0)
		        ) ORDER BY p.name
		    ) AS json
		    FROM ont_property p
		    WHERE p.object_type_id = o.id
		    -- NOTE: ont_property.mark defaults to FALSE (vs ont_object_type.mark
		    -- which defaults to TRUE). On properties, mark=true means "manually
		    -- tagged" — not "active". For triage we need every property the
		    -- operator could bind a keyword to, regardless of tagging status.
		) props ON true
		WHERE o.project_id = $1
		  AND COALESCE(o.mark, true) = true
		ORDER BY o.kind DESC, o.name`

		rows, err := db.Query(q, pid)
		if err != nil {
			JsonResp(w, M{"error": err.Error(), "data": []M{}, "total": 0})
			return
		}
		defer rows.Close()

		out := []M{}
		for rows.Next() {
			var odID, odName, odDisplay, odKind, odDesc, odTable, propsJSON string
			var keywordCount int
			if err := rows.Scan(&odID, &odName, &odDisplay, &odKind, &odDesc, &odTable, &keywordCount, &propsJSON); err != nil {
				continue
			}
			var props []interface{}
			_ = json.Unmarshal([]byte(propsJSON), &props)
			if props == nil {
				props = []interface{}{}
			}
			out = append(out, M{
				"id":           odID,
				"name":         odName,
				"displayName":  odDisplay,
				"kind":         odKind,
				"description":  odDesc,
				"sourceTable":  odTable,
				"keywordCount": keywordCount,
				"properties":   props,
			})
		}
		JsonResp(w, M{"data": out, "total": len(out)})
	}
}

// ── Metric intent triggers ───────────────────────────────────────────────

// handleIntentTriggers returns all lakehouse_keyword rows bound to a given
// metric intent. Used by the metric-intents page TRIGGERS column.
func handleIntentTriggers(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		// /api/ontology/metric-intents/{id}/triggers              (GET)
		// /api/ontology/metric-intents/{id}/triggers/{kwId}       (DELETE)
		path := strings.TrimPrefix(r.URL.Path, "/api/ontology/metric-intents/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 || parts[1] != "triggers" {
			http.NotFound(w, r)
			return
		}
		intentID := parts[0]
		if !IsValidUUID(intentID) {
			http.NotFound(w, r)
			return
		}
		// Cross-project IDOR guard: triggers belong to the intent's project.
		if !authmw.EnforceEntityProject(w, r, db, "lakehouse_metric", "id", intentID) {
			return
		}

		// DELETE specific trigger row.
		if r.Method == http.MethodDelete && len(parts) >= 3 {
			kwID := parts[2]
			if !IsValidUUID(kwID) {
				http.NotFound(w, r)
				return
			}
			if _, err := db.Exec(
				`DELETE FROM lakehouse_keyword
				 WHERE id = $1 AND metric_id = $2`,
				kwID, intentID,
			); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		// GET list.
		// usage_count = number of distinct annotations whose tokens contain
		// this keyword OR any of its aliases (case-insensitive). It approximates
		// "how often this trigger was reachable in real user activity" so the
		// operator can spot dead triggers worth pruning.
		rows, err := db.Query(`
			SELECT lk.id::text, lk.keyword,
			       COALESCE(lk.aliases, '{}'::text[]),
			       lk.synced_at,
			       COALESCE(usage.cnt, 0)::int AS usage_count
			FROM lakehouse_keyword lk
			LEFT JOIN LATERAL (
			    SELECT COUNT(*) AS cnt
			    FROM ont_agent_annotation a,
			         LATERAL unnest(string_to_array(COALESCE(a.tokens,''), '|')) tok
			    WHERE a.project_id = lk.project_id
			      AND TRIM(tok) <> ''
			      AND LOWER(TRIM(tok)) IN (
			          SELECT LOWER(lk.keyword)
			          UNION
			          SELECT LOWER(al)
			          FROM unnest(COALESCE(lk.aliases, '{}'::text[])) AS al
			      )
			) usage ON true
			WHERE lk.metric_id = $1
			ORDER BY usage_count DESC, lk.keyword`,
			intentID)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		out := []M{}
		for rows.Next() {
			var id, keyword string
			var aliases pq.StringArray
			var syncedAt sql.NullTime
			var usageCount int
			if err := rows.Scan(&id, &keyword, &aliases, &syncedAt, &usageCount); err != nil {
				continue
			}
			var ts string
			if syncedAt.Valid {
				ts = syncedAt.Time.Format("2006-01-02T15:04:05Z07:00")
			}
			out = append(out, M{
				"id":         id,
				"keyword":    keyword,
				"aliases":    []string(aliases),
				"syncedAt":   ts,
				"usageCount": usageCount,
			})
		}
		ListResp(w, out, len(out))
	}
}
