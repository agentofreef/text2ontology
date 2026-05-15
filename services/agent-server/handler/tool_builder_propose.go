// Builder propose_* tools — write pending (mark=false) drafts of OD / Intent /
// Link that the user later confirms via the activation endpoints in
// services/backend-api/handler/handler_builder_activate.go (US-005/US-006).
//
// IMPORTANT: every INSERT here MUST set mark=false explicitly because two of
// the three target tables default mark=TRUE (lakehouse_metric_intent,
// ont_link_type). Omitting mark would silently activate.
//
// Trigger keywords for propose_intent are intentionally NOT written here —
// lakehouse_keyword has no `mark` column, so pending intents would leak
// keywords into recall. They are stored only in the tool result JSON and
// inserted at activation time within the same transaction as mark=true.
package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/lib/pq"

	. "github.com/lakehouse2ontology/httputil"
)

// strSliceArg coerces a JSON-decoded args["x"] into []string. Tolerates
// missing/null/non-array shapes (returns empty slice) and trims each entry.
func strSliceArg(args map[string]interface{}, key string) []string {
	raw, ok := args[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s := strings.TrimSpace(fmt.Sprintf("%v", v)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// boolArg pulls a boolean from args, defaulting to false.
func boolArg(args map[string]interface{}, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

// builderToolProposeOd inserts a pending OD + properties (mark=false) and
// returns the proposal payload for the frontend BuilderProposeOdCard.
//
// Concurrency: a single Postgres tx ensures the OD and all its properties
// land together. UNIQUE(project_id, name) violations are mapped to a
// human-readable error so the AI can suggest an alternative name.
func builderToolProposeOd(ctx context.Context, db *sql.DB, projectID, threadID string, args map[string]interface{}) M {
	name := strings.TrimSpace(StrVal(args, "name"))
	kind := strings.TrimSpace(StrVal(args, "kind"))
	if kind == "" {
		kind = "entity"
	}
	semanticSQL := StrVal(args, "semanticSql")
	description := StrVal(args, "description")

	if name == "" {
		return M{"error": "name is required"}
	}
	if strings.TrimSpace(semanticSQL) == "" {
		return M{"error": "semanticSql is required"}
	}

	// Server-side schema rewrite. The LLM is told (via tool description) that
	// the staging schema is reachable via the literal prefix `staging.`. We
	// resolve the project's real lakehouse_schema here and substitute every
	// occurrence (case-insensitive, only at identifier boundaries) so the
	// generated SQL actually runs. Without this, the LLM ships SQL that
	// trial-runs to `relation "staging.X" does not exist`.
	semanticSQL = rewriteStagingSchema(ctx, db, projectID, semanticSQL)

	rawProps, ok := args["properties"].([]interface{})
	if !ok || len(rawProps) == 0 {
		return M{"error": "至少需要一个 property"}
	}
	type propIn struct {
		Name          string
		DataType      string
		SourceColumn  string
		IsFilterable  bool
		IsGroupable   bool
		IsMachineCode bool
	}
	props := make([]propIn, 0, len(rawProps))
	for i, raw := range rawProps {
		m, ok := raw.(map[string]interface{})
		if !ok {
			return M{"error": fmt.Sprintf("property #%d 必须是对象", i+1)}
		}
		pn := strings.TrimSpace(StrVal(m, "name"))
		dt := strings.TrimSpace(StrVal(m, "dataType"))
		sc := strings.TrimSpace(StrVal(m, "sourceColumn"))
		if pn == "" || dt == "" || sc == "" {
			return M{"error": fmt.Sprintf("property #%d 缺少 name/dataType/sourceColumn", i+1)}
		}
		props = append(props, propIn{
			Name:          pn,
			DataType:      dt,
			SourceColumn:  sc,
			IsFilterable:  boolArg(m, "isFilterable"),
			IsGroupable:   boolArg(m, "isGroupable"),
			IsMachineCode: boolArg(m, "isMachineCode"),
		})
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return M{"error": "begin tx failed: " + err.Error()}
	}
	defer tx.Rollback()

	// Provenance lives in source_config (JSONB) so it doesn't pollute the
	// user-visible note column. See plan Gap-5.
	srcCfg, _ := json.Marshal(map[string]string{"builderThreadId": threadID})

	var objID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ont_object_type
		    (project_id, name, display_name, kind, description, mark, origin, source_type, source_config, semantic_sql)
		VALUES ($1, $2, $3, $4, $5, false, 'builder', 'builder', $6::jsonb, $7)
		RETURNING id`,
		projectID, name, name, kind, description, string(srcCfg), semanticSQL,
	).Scan(&objID)
	if err != nil {
		// pq UNIQUE violation surfaces as code 23505. Map to a clear,
		// LLM-actionable error so the AI can rename and retry.
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			return M{"error": fmt.Sprintf("OD 名称 '%s' 已存在，请换一个名称", name)}
		}
		if strings.Contains(err.Error(), "duplicate key") {
			return M{"error": fmt.Sprintf("OD 名称 '%s' 已存在，请换一个名称", name)}
		}
		return M{"error": "insert ont_object_type failed: " + err.Error()}
	}

	type propOut struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		DataType      string `json:"dataType"`
		SourceColumn  string `json:"sourceColumn"`
		IsFilterable  bool   `json:"isFilterable"`
		IsGroupable   bool   `json:"isGroupable"`
		IsMachineCode bool   `json:"isMachineCode"`
	}
	resultProps := make([]propOut, 0, len(props))
	for _, p := range props {
		var pid string
		err := tx.QueryRowContext(ctx, `
			INSERT INTO ont_property
			    (project_id, object_type_id, name, display_name, data_type,
			     source_column, is_filterable, is_groupable, is_machine_code, mark)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, false)
			RETURNING id`,
			projectID, objID, p.Name, p.Name, p.DataType,
			p.SourceColumn, p.IsFilterable, p.IsGroupable, p.IsMachineCode,
		).Scan(&pid)
		if err != nil {
			return M{"error": fmt.Sprintf("insert ont_property '%s' failed: %s", p.Name, err.Error())}
		}
		resultProps = append(resultProps, propOut{
			ID:            pid,
			Name:          p.Name,
			DataType:      p.DataType,
			SourceColumn:  p.SourceColumn,
			IsFilterable:  p.IsFilterable,
			IsGroupable:   p.IsGroupable,
			IsMachineCode: p.IsMachineCode,
		})
	}

	if err := tx.Commit(); err != nil {
		return M{"error": "commit tx failed: " + err.Error()}
	}

	return M{
		"objectId":             objID,
		"name":                 name,
		"kind":                 kind,
		"semanticSql":          semanticSQL,
		"description":          description,
		"properties":           resultProps,
		"pending_confirmation": true,
		"summary_text":         fmt.Sprintf("OD 草稿 %s 已生成 (%d 个属性)，请在卡片中确认或编辑", name, len(resultProps)),
	}
}

// builderToolProposeIntent inserts a pending lakehouse_metric_intent
// (mark=false EXPLICIT — schema default is TRUE). Trigger keywords are NOT
// inserted into lakehouse_keyword; they ride along in the result JSON for
// the frontend card and are committed only at activation time.
func builderToolProposeIntent(db *sql.DB, projectID, threadID string, args map[string]interface{}) M {
	objectID := strings.TrimSpace(StrVal(args, "objectId"))
	name := strings.TrimSpace(StrVal(args, "name"))
	canonicalMetric := strings.TrimSpace(StrVal(args, "canonicalMetric"))
	if !IsValidUUID(objectID) {
		return M{"error": "objectId 必须是有效 UUID"}
	}
	if name == "" {
		return M{"error": "name is required"}
	}
	if canonicalMetric == "" {
		return M{"error": "canonicalMetric is required"}
	}

	// Validate the OD exists and belongs to this project.
	var existsID string
	if err := db.QueryRow(`
		SELECT id FROM ont_object_type
		WHERE id = $1 AND project_id = $2 AND mark = true`,
		objectID, projectID).Scan(&existsID); err != nil {
		if err == sql.ErrNoRows {
			return M{"error": "objectId 对应的 OD 不存在或尚未激活"}
		}
		return M{"error": "validate object failed: " + err.Error()}
	}

	autoGroupBy := strSliceArg(args, "autoGroupBy")
	triggerKeywords := strSliceArg(args, "triggerKeywords")
	pivotValues := strSliceArg(args, "pivotValues")
	pivotColumnLabels := strSliceArg(args, "pivotColumnLabels")

	// canonical_filters: pass-through JSONB array. Default to empty array.
	var canonicalFilters []map[string]interface{}
	if raw, ok := args["canonicalFilters"].([]interface{}); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]interface{}); ok {
				canonicalFilters = append(canonicalFilters, m)
			}
		}
	}
	if canonicalFilters == nil {
		canonicalFilters = []map[string]interface{}{}
	}
	cfBytes, _ := json.Marshal(canonicalFilters)

	pivotOn := strings.TrimSpace(StrVal(args, "pivotOn"))
	pivotTotalLabel := strings.TrimSpace(StrVal(args, "pivotTotalLabel"))
	if pivotTotalLabel == "" {
		pivotTotalLabel = "Total"
	}
	pivotWithPercent := boolArg(args, "pivotWithPercent")
	pivotAppendGrandTotal := boolArg(args, "pivotAppendGrandTotal")

	// Send NULL for pivot_on / pivot_values / pivot_column_labels when
	// unset so the row matches existing nullable column semantics.
	var pivotOnArg interface{}
	if pivotOn != "" {
		pivotOnArg = pivotOn
	}
	var pivotValuesArg interface{}
	if len(pivotValues) > 0 {
		pivotValuesArg = pq.Array(pivotValues)
	}
	var pivotColumnLabelsArg interface{}
	if len(pivotColumnLabels) > 0 {
		pivotColumnLabelsArg = pq.Array(pivotColumnLabels)
	}

	var intentID string
	err := db.QueryRow(`
		INSERT INTO lakehouse_metric_intent
		    (project_id, object_id, name, canonical_metric, canonical_filters,
		     auto_group_by, pivot_on, pivot_values, pivot_column_labels,
		     pivot_total_label, pivot_with_percent, pivot_append_grand_total, mark)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12, false)
		RETURNING id`,
		projectID, objectID, name, canonicalMetric, string(cfBytes),
		pq.Array(autoGroupBy), pivotOnArg, pivotValuesArg, pivotColumnLabelsArg,
		pivotTotalLabel, pivotWithPercent, pivotAppendGrandTotal,
	).Scan(&intentID)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			return M{"error": fmt.Sprintf("Intent 名称 '%s' 已存在，请换一个名称", name)}
		}
		return M{"error": "insert lakehouse_metric_intent failed: " + err.Error()}
	}

	return M{
		"intentId":              intentID,
		"objectId":              objectID,
		"name":                  name,
		"canonicalMetric":       canonicalMetric,
		"canonicalFilters":      canonicalFilters,
		"autoGroupBy":           autoGroupBy,
		"triggerKeywords":       triggerKeywords,
		"pivotOn":               pivotOn,
		"pivotValues":           pivotValues,
		"pivotColumnLabels":     pivotColumnLabels,
		"pivotTotalLabel":       pivotTotalLabel,
		"pivotWithPercent":      pivotWithPercent,
		"pivotAppendGrandTotal": pivotAppendGrandTotal,
		"pending_confirmation":  true,
	}
}

// builderToolProposeLink inserts a pending ont_link_type (mark=false
// EXPLICIT — schema default is TRUE). Property anchors are validated to
// belong to the declared from/to objects so activation can safely create
// property-anchored ont_causality rows. Causality is NOT created here.
func builderToolProposeLink(db *sql.DB, projectID, threadID string, args map[string]interface{}) M {
	fromObjectID := strings.TrimSpace(StrVal(args, "fromObjectId"))
	toObjectID := strings.TrimSpace(StrVal(args, "toObjectId"))
	fromPropertyID := strings.TrimSpace(StrVal(args, "fromPropertyId"))
	toPropertyID := strings.TrimSpace(StrVal(args, "toPropertyId"))
	fkColumn := strings.TrimSpace(StrVal(args, "fkColumn"))
	linkName := strings.TrimSpace(StrVal(args, "linkName"))
	cardinality := strings.TrimSpace(StrVal(args, "cardinality"))
	description := StrVal(args, "description")
	if cardinality == "" {
		cardinality = "many_to_one"
	}

	for _, pair := range []struct{ field, value string }{
		{"fromObjectId", fromObjectID},
		{"toObjectId", toObjectID},
		{"fromPropertyId", fromPropertyID},
		{"toPropertyId", toPropertyID},
	} {
		if !IsValidUUID(pair.value) {
			return M{"error": pair.field + " 必须是有效 UUID"}
		}
	}
	if fkColumn == "" {
		return M{"error": "fkColumn is required"}
	}
	if linkName == "" {
		return M{"error": "linkName is required"}
	}

	// Both property anchors must belong to their declared object AND project.
	for _, pair := range []struct {
		propertyID string
		objectID   string
		side       string
	}{
		{fromPropertyID, fromObjectID, "from"},
		{toPropertyID, toObjectID, "to"},
	} {
		var probe string
		err := db.QueryRow(`
			SELECT id FROM ont_property
			WHERE id = $1 AND object_type_id = $2 AND project_id = $3`,
			pair.propertyID, pair.objectID, projectID).Scan(&probe)
		if err != nil {
			if err == sql.ErrNoRows {
				return M{"error": fmt.Sprintf("%sPropertyId 不属于 %sObjectId 或不在当前项目", pair.side, pair.side)}
			}
			return M{"error": "validate property failed: " + err.Error()}
		}
	}

	var linkID string
	err := db.QueryRow(`
		INSERT INTO ont_link_type
		    (project_id, from_object_id, to_object_id, link_name,
		     description, fk_column, cardinality, mark)
		VALUES ($1, $2, $3, $4, $5, $6, $7, false)
		RETURNING id`,
		projectID, fromObjectID, toObjectID, linkName, description, fkColumn, cardinality,
	).Scan(&linkID)
	if err != nil {
		return M{"error": "insert ont_link_type failed: " + err.Error()}
	}

	return M{
		"linkId":               linkID,
		"fromObjectId":         fromObjectID,
		"toObjectId":           toObjectID,
		"fromPropertyId":       fromPropertyID,
		"toPropertyId":         toPropertyID,
		"fkColumn":             fkColumn,
		"cardinality":          cardinality,
		"linkName":             linkName,
		"description":          description,
		"pending_confirmation": true,
	}
}

// stagingPrefixRE matches the literal placeholder `staging.<table>` where
// <table> is either a double-quoted identifier (group 2) or an unquoted
// word (group 3). Group 1 is the leading separator we keep intact so we
// don't merge tokens. Case-insensitive so `STAGING.` works too. Identifier
// boundary in front prevents false positives like `mystaging.x`.
var stagingPrefixRE = regexp.MustCompile(`(?i)(^|[\s,()])staging\.(?:"([^"]+)"|(\w+))`)

// rewriteStagingSchema substitutes the literal placeholder `staging.<table>`
// with the project's real lakehouse_schema and ALWAYS double-quotes the
// table identifier — staging tables in this project use CamelCase names
// (Orders, Products, Categories), and Postgres folds unquoted identifiers
// to lowercase. If we leave the table unquoted in the rewrite, the trial-run
// hits "relation .orders does not exist" even though the rewrite was
// otherwise correct.
//
// If the project has no lakehouse_schema configured (rare; staging tables
// don't exist yet), we leave the SQL unchanged so the trial-run failure is
// still informative ("relation staging.X does not exist") rather than a
// confusing rewrite to `""."X"`.
func rewriteStagingSchema(ctx context.Context, db *sql.DB, projectID, sqlText string) string {
	var schema string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id = $1`, projectID,
	).Scan(&schema); err != nil || strings.TrimSpace(schema) == "" {
		return sqlText
	}
	return stagingPrefixRE.ReplaceAllStringFunc(sqlText, func(match string) string {
		groups := stagingPrefixRE.FindStringSubmatch(match)
		// groups[0]=full match, [1]=leading sep, [2]=quoted ident or "",
		// [3]=unquoted ident or "". Exactly one of [2]/[3] is non-empty.
		ident := groups[2]
		if ident == "" {
			ident = groups[3]
		}
		return groups[1] + `"` + schema + `"."` + ident + `"`
	})
}
