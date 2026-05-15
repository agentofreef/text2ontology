package handler

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/lib/pq"

	. "github.com/lakehouse2ontology/httputil"
)

// Tag dictionary (suite-scoped) + M2M links to ont_test_case.
// Endpoints registered through handleLHTestSuiteByID routing:
//   GET    /lh-test-suites/{suiteId}/tags                   list with usage count
//   POST   /lh-test-suites/{suiteId}/tags                   {name} — idempotent upsert
//   PATCH  /lh-test-suites/{suiteId}/tags/{tagId}           {name} — rename
//   DELETE /lh-test-suites/{suiteId}/tags/{tagId}           drop tag (cascade links)
//   PUT    /lh-test-suites/{suiteId}/cases/{caseId}/tags    {tagIds}
//   POST   /lh-test-suites/{suiteId}/cases/bulk-tag         {caseIds, add?, remove?}

func handleLHSuiteTags(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`
			SELECT t.id, t.name, COALESCE(c.cnt,0)
			FROM ont_test_case_tag t
			LEFT JOIN (
				SELECT l.tag_id, COUNT(*) AS cnt
				FROM ont_test_case_tag_link l
				JOIN ont_test_case c ON c.id = l.case_id
				WHERE c.suite_id = $1
				GROUP BY l.tag_id
			) c ON c.tag_id = t.id
			WHERE t.suite_id = $1
			ORDER BY t.sort_order, t.name`, suiteID)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()
		var tags []M
		for rows.Next() {
			var id, name string
			var cnt int
			rows.Scan(&id, &name, &cnt)
			tags = append(tags, M{"id": id, "name": name, "count": cnt})
		}
		if tags == nil {
			tags = []M{}
		}
		ListResp(w, tags, len(tags))

	case http.MethodPost:
		body := ReadBody(r)
		name := strings.TrimSpace(StrVal(body, "name"))
		if name == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "name required"})
			return
		}
		var id string
		err := db.QueryRow(`INSERT INTO ont_test_case_tag (suite_id, name)
			VALUES ($1, $2)
			ON CONFLICT (suite_id, name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, suiteID, name).Scan(&id)
		if err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		JsonResp(w, M{"id": id, "name": name})

	default:
		w.WriteHeader(405)
	}
}

func handleLHSuiteTagByID(db *sql.DB, suiteID, tagID string, w http.ResponseWriter, r *http.Request) {
	if !IsValidUUID(tagID) {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "invalid tagId"})
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		body := ReadBody(r)
		name := strings.TrimSpace(StrVal(body, "name"))
		if name == "" {
			w.WriteHeader(400)
			JsonResp(w, M{"error": "name required"})
			return
		}
		if _, err := db.Exec(`UPDATE ont_test_case_tag SET name = $1
			WHERE id = $2 AND suite_id = $3`, name, tagID, suiteID); err != nil {
			w.WriteHeader(409)
			JsonResp(w, M{"error": "标签重名或不存在"})
			return
		}
		JsonResp(w, M{"ok": true, "id": tagID, "name": name})

	case http.MethodDelete:
		db.Exec(`DELETE FROM ont_test_case_tag WHERE id = $1 AND suite_id = $2`, tagID, suiteID)
		JsonResp(w, M{"ok": true})

	default:
		w.WriteHeader(405)
	}
}

// PUT /cases/{caseId}/tags — full replace of tag set.
// Accepts either `tagIds` (UUIDs) or `tagNames` (strings). Names are
// resolved/created inside this suite (idempotent upsert) before link replacement.
func handleLHCaseTags(db *sql.DB, suiteID, caseID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	if !IsValidUUID(caseID) {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "invalid caseId"})
		return
	}
	body := ReadBody(r)
	tagIDs := uuidArrayFromBody(body["tagIds"])
	tagNames := stringArrayFromBody(body["tagNames"])

	tx, err := db.Begin()
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	// Resolve tagNames into IDs (create missing). Merge with explicit tagIds.
	for _, n := range tagNames {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		var id string
		if err := tx.QueryRow(`INSERT INTO ont_test_case_tag (suite_id, name)
			VALUES ($1, $2)
			ON CONFLICT (suite_id, name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, suiteID, n).Scan(&id); err == nil {
			tagIDs = append(tagIDs, id)
		}
	}

	if _, err := tx.Exec(`DELETE FROM ont_test_case_tag_link WHERE case_id = $1`, caseID); err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	if len(tagIDs) > 0 {
		// Guard: only keep tags that actually belong to this suite.
		if _, err := tx.Exec(`INSERT INTO ont_test_case_tag_link (case_id, tag_id)
			SELECT $1, t.id FROM ont_test_case_tag t
			WHERE t.suite_id = $2 AND t.id = ANY($3)
			ON CONFLICT DO NOTHING`, caseID, suiteID, pq.Array(tagIDs)); err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
	}
	tx.Commit()
	JsonResp(w, M{"ok": true})
}

// POST /cases/bulk-tag — add and/or remove tags for many cases at once.
// Tag names are resolved/created inside the suite; any unknown name on "add" creates a new tag.
func handleLHBulkTag(db *sql.DB, suiteID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	body := ReadBody(r)
	caseIDs := uuidArrayFromBody(body["caseIds"])
	addNames := stringArrayFromBody(body["add"])
	removeNames := stringArrayFromBody(body["remove"])
	if len(caseIDs) == 0 || (len(addNames) == 0 && len(removeNames) == 0) {
		w.WriteHeader(400)
		JsonResp(w, M{"error": "caseIds and (add or remove) required"})
		return
	}

	tx, err := db.Begin()
	if err != nil {
		w.WriteHeader(500)
		JsonResp(w, M{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	addIDs := make([]string, 0, len(addNames))
	for _, n := range addNames {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		var id string
		if err := tx.QueryRow(`INSERT INTO ont_test_case_tag (suite_id, name)
			VALUES ($1, $2)
			ON CONFLICT (suite_id, name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, suiteID, n).Scan(&id); err == nil {
			addIDs = append(addIDs, id)
		}
	}
	removeIDs := make([]string, 0, len(removeNames))
	for _, n := range removeNames {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		var id string
		if err := tx.QueryRow(`SELECT id FROM ont_test_case_tag
			WHERE suite_id = $1 AND name = $2`, suiteID, n).Scan(&id); err == nil {
			removeIDs = append(removeIDs, id)
		}
	}

	if len(addIDs) > 0 {
		// Only cases truly belonging to this suite survive the join — prevents cross-suite writes.
		if _, err := tx.Exec(`INSERT INTO ont_test_case_tag_link (case_id, tag_id)
			SELECT c.id, t.id
			FROM ont_test_case c
			CROSS JOIN ont_test_case_tag t
			WHERE c.suite_id = $1 AND t.suite_id = $1
			  AND c.id = ANY($2) AND t.id = ANY($3)
			ON CONFLICT DO NOTHING`,
			suiteID, pq.Array(caseIDs), pq.Array(addIDs)); err != nil {
			w.WriteHeader(500)
			JsonResp(w, M{"error": err.Error()})
			return
		}
	}
	if len(removeIDs) > 0 {
		tx.Exec(`DELETE FROM ont_test_case_tag_link
			WHERE case_id = ANY($1) AND tag_id = ANY($2)`,
			pq.Array(caseIDs), pq.Array(removeIDs))
	}
	tx.Commit()
	JsonResp(w, M{"ok": true, "affectedCases": len(caseIDs), "added": len(addIDs), "removed": len(removeIDs)})
}

// loadTagsForCases — bulk loader keyed by case_id. Used to merge tags into run-detail responses
// without N+1 queries.
func loadTagsForCases(db *sql.DB, caseIDs []string) map[string][]M {
	result := make(map[string][]M, len(caseIDs))
	if len(caseIDs) == 0 {
		return result
	}
	rows, err := db.Query(`
		SELECT l.case_id, t.id, t.name
		FROM ont_test_case_tag_link l
		JOIN ont_test_case_tag t ON t.id = l.tag_id
		WHERE l.case_id = ANY($1)
		ORDER BY t.sort_order, t.name`, pq.Array(caseIDs))
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var caseID, tagID, tagName string
		rows.Scan(&caseID, &tagID, &tagName)
		result[caseID] = append(result[caseID], M{"id": tagID, "name": tagName})
	}
	return result
}

// ---------- body coercion helpers ----------

func uuidArrayFromBody(v interface{}) []string {
	out := []string{}
	if arr, ok := v.([]interface{}); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok && IsValidUUID(s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func stringArrayFromBody(v interface{}) []string {
	out := []string{}
	if arr, ok := v.([]interface{}); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
