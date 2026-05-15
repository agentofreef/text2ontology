// Builder UPDATE / DELETE tools — complement to tool_builder_propose.go
// (create) and tool_builder_list.go (read). Each tool runs inside a single
// transaction so any partial failure leaves the ontology untouched.
//
// Design principles:
//   - Both pending (mark=false) and active (mark=true) entities are mutable.
//   - project_id ownership is verified on every tool to prevent cross-tenant
//     reads/writes.
//   - Edits are PARTIAL: only fields actually present in args are written; an
//     omitted key never NULLs out an existing column.
//   - Activated entities pay extra side-effect cost: keyword + causality rows
//     are touched only when mark=true; pending entities defer them to
//     activation (handler_builder_activate.go owns that path).
//   - semantic_sql edits on an active OD invalidate canonical_query so the
//     OD must be re-activated to re-SOLIDIFY.
package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lib/pq"

	. "github.com/lakehouse2ontology/httputil"
)

// ─── helpers ────────────────────────────────────────────────────────────────

// stringSliceFromArgs coerces args[key] (decoded JSON) into []string. Returns
// (nil, false) when the key is absent so callers can distinguish "not
// provided" from "provided but empty array".
func stringSliceFromArgs(args map[string]interface{}, key string) ([]string, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, false
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out, true
}

// objSliceFromArgs coerces args[key] into []map[string]interface{}.
func objSliceFromArgs(args map[string]interface{}, key string) ([]map[string]interface{}, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, false
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for _, v := range arr {
		if m, ok := v.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out, true
}

// ─── 1. builderToolUpdateOd ─────────────────────────────────────────────────

// builderToolUpdateOd applies partial edits to an OD + property add/edit/delete.
// If semantic_sql changes on an already-activated OD, the OD is marked
// needs-reactivation (canonical_query cleared, validated_at NULL) so the
// LLM/user is forced to re-run activation to re-solidify.
func builderToolUpdateOd(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	objectID := strings.TrimSpace(StrVal(args, "objectId"))
	if !IsValidUUID(objectID) {
		return M{"error": "objectId must be a valid UUID"}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return M{"error": "begin tx failed: " + err.Error()}
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 1. Ownership check.
	var existingID string
	var existingMark bool
	var existingCanonical string
	err = tx.QueryRowContext(ctx,
		`SELECT id, mark, COALESCE(canonical_query,'')
		   FROM ont_object_type
		  WHERE id=$1 AND project_id=$2`,
		objectID, projectID).Scan(&existingID, &existingMark, &existingCanonical)
	if err == sql.ErrNoRows {
		return M{"error": "OD not found in this project"}
	}
	if err != nil {
		return M{"error": "ownership check failed: " + err.Error()}
	}

	// 2. Apply OD-level edits (partial).
	updatedFields := []string{}
	semanticSQLChanged := false
	if editsRaw, ok := args["edits"].(map[string]interface{}); ok && len(editsRaw) > 0 {
		setCols := []string{}
		setArgs := []interface{}{}
		appendCol := func(col string, val interface{}) {
			setCols = append(setCols, fmt.Sprintf("%s=$%d", col, len(setArgs)+1))
			setArgs = append(setArgs, val)
		}
		if v, ok := editsRaw["name"].(string); ok && v != "" {
			appendCol("name", v)
			updatedFields = append(updatedFields, "name")
		}
		if v, ok := editsRaw["displayName"].(string); ok {
			appendCol("display_name", v)
			updatedFields = append(updatedFields, "display_name")
		}
		if v, ok := editsRaw["kind"].(string); ok && v != "" {
			appendCol("kind", v)
			updatedFields = append(updatedFields, "kind")
		}
		if v, ok := editsRaw["description"].(string); ok {
			appendCol("description", v)
			updatedFields = append(updatedFields, "description")
		}
		if v, ok := editsRaw["semanticSql"].(string); ok && v != "" {
			appendCol("semantic_sql", v)
			updatedFields = append(updatedFields, "semantic_sql")
			semanticSQLChanged = true
		}
		if len(setCols) > 0 {
			// Append user_edited_fields tracking (matches routes.go:1073-1076 pattern).
			editedColsLit := make([]string, len(updatedFields))
			for i, f := range updatedFields {
				editedColsLit[i] = "'" + strings.ReplaceAll(f, "'", "''") + "'"
			}
			editedFieldsArr := "ARRAY[" + strings.Join(editedColsLit, ",") + "]::text[]"
			setArgs = append(setArgs, objectID)
			q := fmt.Sprintf(
				`UPDATE ont_object_type SET %s,
				 user_edited_fields = (SELECT ARRAY(SELECT DISTINCT unnest(COALESCE(user_edited_fields,'{}'::text[]) || %s))),
				 updated_at=now()
				 WHERE id=$%d`,
				strings.Join(setCols, ", "), editedFieldsArr, len(setArgs))
			if _, err := tx.ExecContext(ctx, q, setArgs...); err != nil {
				if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
					return M{"error": "OD name conflicts with an existing OD"}
				}
				return M{"error": "apply OD edits failed: " + err.Error()}
			}
		}
	}

	// 3. Property edits (match by id OR name).
	addedProps := 0
	deletedProps := 0
	if propEdits, ok := objSliceFromArgs(args, "propertyEdits"); ok {
		for _, pe := range propEdits {
			editsMap, ok := pe["edits"].(map[string]interface{})
			if !ok || len(editsMap) == 0 {
				continue
			}
			pid := strings.TrimSpace(StrVal(pe, "propertyId"))
			pname := strings.TrimSpace(StrVal(pe, "propertyName"))
			if pid == "" && pname == "" {
				continue
			}
			setCols := []string{}
			setArgs := []interface{}{}
			appendP := func(col string, val interface{}) {
				setCols = append(setCols, fmt.Sprintf("%s=$%d", col, len(setArgs)+1))
				setArgs = append(setArgs, val)
			}
			if v, ok := editsMap["dataType"].(string); ok && v != "" {
				appendP("data_type", v)
			}
			if v, ok := editsMap["sourceColumn"].(string); ok {
				appendP("source_column", v)
			}
			if v, ok := editsMap["isFilterable"].(bool); ok {
				appendP("is_filterable", v)
			}
			if v, ok := editsMap["isGroupable"].(bool); ok {
				appendP("is_groupable", v)
			}
			if v, ok := editsMap["isMachineCode"].(bool); ok {
				appendP("is_machine_code", v)
			}
			if v, ok := editsMap["displayName"].(string); ok {
				appendP("display_name", v)
			}
			if v, ok := editsMap["description"].(string); ok {
				appendP("description", v)
			}
			if len(setCols) == 0 {
				continue
			}
			setArgs = append(setArgs, objectID)
			whereClause := ""
			if pid != "" {
				if !IsValidUUID(pid) {
					return M{"error": "propertyId must be a valid UUID"}
				}
				setArgs = append(setArgs, pid)
				whereClause = fmt.Sprintf(`object_type_id=$%d AND id=$%d`, len(setArgs)-1, len(setArgs))
			} else {
				setArgs = append(setArgs, pname)
				whereClause = fmt.Sprintf(`object_type_id=$%d AND name=$%d`, len(setArgs)-1, len(setArgs))
			}
			q := fmt.Sprintf(`UPDATE ont_property SET %s, updated_at=now() WHERE %s`,
				strings.Join(setCols, ", "), whereClause)
			if _, err := tx.ExecContext(ctx, q, setArgs...); err != nil {
				return M{"error": "apply property edit failed: " + err.Error()}
			}
		}
	}

	// 4. Property adds (mark inherits parent OD activation state).
	if propAdds, ok := objSliceFromArgs(args, "propertyAdds"); ok {
		for _, pa := range propAdds {
			pname := strings.TrimSpace(StrVal(pa, "name"))
			ptype := strings.TrimSpace(StrVal(pa, "dataType"))
			pcol := strings.TrimSpace(StrVal(pa, "sourceColumn"))
			if pname == "" || ptype == "" || pcol == "" {
				return M{"error": "propertyAdds: each entry needs name, dataType, sourceColumn"}
			}
			isFilterable := true
			if v, ok := pa["isFilterable"].(bool); ok {
				isFilterable = v
			}
			isGroupable := true
			if v, ok := pa["isGroupable"].(bool); ok {
				isGroupable = v
			}
			isMC := false
			if v, ok := pa["isMachineCode"].(bool); ok {
				isMC = v
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO ont_property
				   (project_id, object_type_id, name, display_name, data_type,
				    source_column, is_filterable, is_groupable, is_machine_code, mark)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				projectID, objectID, pname, pname, ptype, pcol,
				isFilterable, isGroupable, isMC, existingMark)
			if err != nil {
				if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
					return M{"error": fmt.Sprintf("property '%s' already exists on this OD", pname)}
				}
				return M{"error": "insert property failed: " + err.Error()}
			}
			addedProps++
		}
	}

	// 5. Property deletes (constrained to this OD).
	if propDels, ok := stringSliceFromArgs(args, "propertyDeletes"); ok {
		for _, pid := range propDels {
			if !IsValidUUID(pid) {
				return M{"error": "propertyDeletes: each entry must be a valid UUID"}
			}
			res, err := tx.ExecContext(ctx,
				`DELETE FROM ont_property WHERE id=$1 AND object_type_id=$2`,
				pid, objectID)
			if err != nil {
				return M{"error": "delete property failed: " + err.Error()}
			}
			n, _ := res.RowsAffected()
			deletedProps += int(n)
		}
	}

	// 6. semantic_sql change on active OD → invalidate canonical_query.
	needsReactivation := false
	if semanticSQLChanged && existingMark && existingCanonical != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE ont_object_type
			    SET canonical_query='', validated_at=NULL, updated_at=now()
			  WHERE id=$1`,
			objectID); err != nil {
			return M{"error": "invalidate canonical_query failed: " + err.Error()}
		}
		needsReactivation = true
	}

	if err := tx.Commit(); err != nil {
		return M{"error": "commit failed: " + err.Error()}
	}
	committed = true

	summary := fmt.Sprintf("OD %s 已更新", objectID)
	if needsReactivation {
		summary += "（semantic_sql 已变更，OD 需重新激活以重新固化 canonical_query）"
	}
	resp := M{
		"objectId":          objectID,
		"updatedFields":     updatedFields,
		"addedProperties":   addedProps,
		"deletedProperties": deletedProps,
		"needsReactivation": needsReactivation,
		"summary_text":      summary,
	}
	if needsReactivation {
		resp["warning"] = "OD has been marked as needing re-activation due to semantic_sql change"
	}
	return resp
}

// ─── 2. builderToolDeleteOd ─────────────────────────────────────────────────

// builderToolDeleteOd removes an OD plus its dependent Intents and Links when
// cascade=true. With cascade=false, fails with the dependent-id list so the
// caller can decide.
func builderToolDeleteOd(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	objectID := strings.TrimSpace(StrVal(args, "objectId"))
	if !IsValidUUID(objectID) {
		return M{"error": "objectId must be a valid UUID"}
	}
	cascade := true
	if v, ok := args["cascade"].(bool); ok {
		cascade = v
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return M{"error": "begin tx failed: " + err.Error()}
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 1. Ownership.
	var name string
	var mark bool
	err = tx.QueryRowContext(ctx,
		`SELECT name, mark FROM ont_object_type
		  WHERE id=$1 AND project_id=$2`,
		objectID, projectID).Scan(&name, &mark)
	if err == sql.ErrNoRows {
		return M{"error": "OD not found in this project"}
	}
	if err != nil {
		return M{"error": "ownership check failed: " + err.Error()}
	}

	// 2. Find dependents.
	intentIDs := []string{}
	intentRows, err := tx.QueryContext(ctx,
		`SELECT id FROM lakehouse_metric_intent WHERE object_id=$1`, objectID)
	if err != nil {
		return M{"error": "list dependent intents failed: " + err.Error()}
	}
	for intentRows.Next() {
		var iid string
		if err := intentRows.Scan(&iid); err == nil {
			intentIDs = append(intentIDs, iid)
		}
	}
	intentRows.Close()

	linkIDs := []string{}
	linkRows, err := tx.QueryContext(ctx,
		`SELECT id FROM ont_link_type WHERE from_object_id=$1 OR to_object_id=$1`, objectID)
	if err != nil {
		return M{"error": "list dependent links failed: " + err.Error()}
	}
	for linkRows.Next() {
		var lid string
		if err := linkRows.Scan(&lid); err == nil {
			linkIDs = append(linkIDs, lid)
		}
	}
	linkRows.Close()

	if (len(intentIDs) > 0 || len(linkIDs) > 0) && !cascade {
		return M{
			"error":           "OD has dependents; pass cascade=true to delete them along with the OD",
			"dependentIntents": intentIDs,
			"dependentLinks":   linkIDs,
		}
	}

	// 3. Cascade delete.
	cascadeIntents := 0
	cascadeLinks := 0
	if cascade {
		// Intents — FK CASCADE on lakehouse_keyword(metric_intent_id) handles
		// keywords; we also explicitly delete keywords pointing at this OD
		// directly (object_id) so triage stays consistent.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM lakehouse_metric_intent WHERE object_id=$1 AND project_id=$2`,
			objectID, projectID); err != nil {
			return M{"error": "delete dependent intents failed: " + err.Error()}
		}
		cascadeIntents = len(intentIDs)

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM ont_link_type
			  WHERE (from_object_id=$1 OR to_object_id=$1) AND project_id=$2`,
			objectID, projectID); err != nil {
			return M{"error": "delete dependent links failed: " + err.Error()}
		}
		cascadeLinks = len(linkIDs)

		// Causality rows pointing at this OD's property knowledge entries.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM ont_causality
			  WHERE project_id=$2 AND (
			        from_knowledge_id IN (
			            SELECT id FROM ont_knowledge
			             WHERE anchor_type='property' AND anchor_id IN (
			                 SELECT id FROM ont_property WHERE object_type_id=$1
			             )
			        )
			     OR to_knowledge_id IN (
			            SELECT id FROM ont_knowledge
			             WHERE anchor_type='property' AND anchor_id IN (
			                 SELECT id FROM ont_property WHERE object_type_id=$1
			             )
			        )
			  )`, objectID, projectID); err != nil {
			return M{"error": "delete causality rows failed: " + err.Error()}
		}
	}

	// 4. Delete the OD itself (FK ON DELETE CASCADE handles ont_property).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ont_object_type WHERE id=$1 AND project_id=$2`,
		objectID, projectID); err != nil {
		return M{"error": "delete OD failed: " + err.Error()}
	}

	if err := tx.Commit(); err != nil {
		return M{"error": "commit failed: " + err.Error()}
	}
	committed = true

	return M{
		"objectId":              objectID,
		"name":                  name,
		"wasMarked":             mark,
		"cascadeDeletedIntents": cascadeIntents,
		"cascadeDeletedLinks":   cascadeLinks,
		"summary_text":          fmt.Sprintf("已删除 OD '%s' 及其级联资源", name),
	}
}

// ─── 3. builderToolUpdateIntent ─────────────────────────────────────────────

// builderToolUpdateIntent applies partial edits to an Intent. Keyword adds /
// deletes only execute against active intents; pending intents must wait for
// activation to write keywords (lakehouse_keyword has no `mark` column, so
// pending keywords would leak into recall).
func builderToolUpdateIntent(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	intentID := strings.TrimSpace(StrVal(args, "intentId"))
	if !IsValidUUID(intentID) {
		return M{"error": "intentId must be a valid UUID"}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return M{"error": "begin tx failed: " + err.Error()}
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 1. Ownership.
	var existingID, objectID string
	var mark bool
	err = tx.QueryRowContext(ctx,
		`SELECT id, object_id, mark
		   FROM lakehouse_metric_intent
		  WHERE id=$1 AND project_id=$2`,
		intentID, projectID).Scan(&existingID, &objectID, &mark)
	if err == sql.ErrNoRows {
		return M{"error": "Intent not found in this project"}
	}
	if err != nil {
		return M{"error": "ownership check failed: " + err.Error()}
	}

	// 2. Apply edits.
	updatedFields := []string{}
	if editsRaw, ok := args["edits"].(map[string]interface{}); ok && len(editsRaw) > 0 {
		setCols := []string{}
		setArgs := []interface{}{}
		appendCol := func(col string, val interface{}) {
			setCols = append(setCols, fmt.Sprintf("%s=$%d", col, len(setArgs)+1))
			setArgs = append(setArgs, val)
		}
		if v, ok := editsRaw["name"].(string); ok && v != "" {
			appendCol("name", v)
			updatedFields = append(updatedFields, "name")
		}
		if v, ok := editsRaw["canonicalMetric"].(string); ok && v != "" {
			appendCol("canonical_metric", v)
			updatedFields = append(updatedFields, "canonical_metric")
		}
		if v, ok := editsRaw["canonicalFilters"]; ok && v != nil {
			b, mErr := json.Marshal(v)
			if mErr != nil {
				return M{"error": "marshal canonicalFilters failed: " + mErr.Error()}
			}
			appendCol("canonical_filters", string(b))
			updatedFields = append(updatedFields, "canonical_filters")
		}
		if arr, ok := stringSliceFromArgs(editsRaw, "autoGroupBy"); ok {
			appendCol("auto_group_by", pq.Array(arr))
			updatedFields = append(updatedFields, "auto_group_by")
		}
		if v, ok := editsRaw["pivotOn"].(string); ok {
			appendCol("pivot_on", v)
			updatedFields = append(updatedFields, "pivot_on")
		}
		if arr, ok := stringSliceFromArgs(editsRaw, "pivotValues"); ok {
			appendCol("pivot_values", pq.Array(arr))
			updatedFields = append(updatedFields, "pivot_values")
		}
		if arr, ok := stringSliceFromArgs(editsRaw, "pivotColumnLabels"); ok {
			appendCol("pivot_column_labels", pq.Array(arr))
			updatedFields = append(updatedFields, "pivot_column_labels")
		}
		if v, ok := editsRaw["pivotTotalLabel"].(string); ok && v != "" {
			appendCol("pivot_total_label", v)
			updatedFields = append(updatedFields, "pivot_total_label")
		}
		if v, ok := editsRaw["pivotWithPercent"].(bool); ok {
			appendCol("pivot_with_percent", v)
			updatedFields = append(updatedFields, "pivot_with_percent")
		}
		if v, ok := editsRaw["pivotAppendGrandTotal"].(bool); ok {
			appendCol("pivot_append_grand_total", v)
			updatedFields = append(updatedFields, "pivot_append_grand_total")
		}
		if v, ok := editsRaw["priority"]; ok {
			switch n := v.(type) {
			case float64:
				appendCol("priority", int(n))
				updatedFields = append(updatedFields, "priority")
			case int:
				appendCol("priority", n)
				updatedFields = append(updatedFields, "priority")
			case int64:
				appendCol("priority", int(n))
				updatedFields = append(updatedFields, "priority")
			}
		}
		if len(setCols) > 0 {
			setArgs = append(setArgs, intentID)
			q := fmt.Sprintf(`UPDATE lakehouse_metric_intent SET %s, updated_at=now() WHERE id=$%d`,
				strings.Join(setCols, ", "), len(setArgs))
			if _, err := tx.ExecContext(ctx, q, setArgs...); err != nil {
				if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
					return M{"error": "Intent name conflicts with an existing Intent"}
				}
				return M{"error": "apply Intent edits failed: " + err.Error()}
			}
		}
	}

	// 3. Keyword adds / deletes — active intents only.
	keywordsAdded := 0
	keywordsDeleted := 0
	keywordsAddArg, hasAdd := stringSliceFromArgs(args, "keywordAdds")
	keywordsDelArg, hasDel := stringSliceFromArgs(args, "keywordDeletes")
	keywordsDeferred := false

	if (hasAdd || hasDel) && !mark {
		keywordsDeferred = true
	} else if mark {
		for _, kw := range keywordsAddArg {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO lakehouse_keyword
				   (project_id, object_type_id, metric_intent_id, keyword, is_machine_code)
				 VALUES ($1, $2, $3, $4, false)
				 ON CONFLICT DO NOTHING`,
				projectID, objectID, intentID, kw)
			if err != nil {
				return M{"error": "insert keyword '" + kw + "' failed: " + err.Error()}
			}
			n, _ := res.RowsAffected()
			keywordsAdded += int(n)
		}
		if len(keywordsDelArg) > 0 {
			res, err := tx.ExecContext(ctx,
				`DELETE FROM lakehouse_keyword
				  WHERE metric_intent_id=$1 AND project_id=$2 AND keyword = ANY($3)`,
				intentID, projectID, pq.Array(keywordsDelArg))
			if err != nil {
				return M{"error": "delete keywords failed: " + err.Error()}
			}
			n, _ := res.RowsAffected()
			keywordsDeleted = int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return M{"error": "commit failed: " + err.Error()}
	}
	committed = true

	resp := M{
		"intentId":        intentID,
		"updatedFields":   updatedFields,
		"keywordsAdded":   keywordsAdded,
		"keywordsDeleted": keywordsDeleted,
		"summary_text":    fmt.Sprintf("Intent %s 已更新", intentID),
	}
	if keywordsDeferred {
		resp["warning"] = "Intent is pending; keyword changes are deferred to activation and were ignored"
	}
	return resp
}

// ─── 4. builderToolDeleteIntent ─────────────────────────────────────────────

func builderToolDeleteIntent(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	intentID := strings.TrimSpace(StrVal(args, "intentId"))
	if !IsValidUUID(intentID) {
		return M{"error": "intentId must be a valid UUID"}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return M{"error": "begin tx failed: " + err.Error()}
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var name string
	err = tx.QueryRowContext(ctx,
		`SELECT name FROM lakehouse_metric_intent
		  WHERE id=$1 AND project_id=$2`,
		intentID, projectID).Scan(&name)
	if err == sql.ErrNoRows {
		return M{"error": "Intent not found in this project"}
	}
	if err != nil {
		return M{"error": "ownership check failed: " + err.Error()}
	}

	// Explicit keyword cleanup (FK CASCADE would handle this, but explicit
	// makes the intent clear and decouples from FK definition).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM lakehouse_keyword
		  WHERE metric_intent_id=$1 AND project_id=$2`,
		intentID, projectID); err != nil {
		return M{"error": "delete keywords failed: " + err.Error()}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM lakehouse_metric_intent WHERE id=$1 AND project_id=$2`,
		intentID, projectID); err != nil {
		return M{"error": "delete Intent failed: " + err.Error()}
	}

	if err := tx.Commit(); err != nil {
		return M{"error": "commit failed: " + err.Error()}
	}
	committed = true

	return M{
		"intentId":     intentID,
		"name":         name,
		"summary_text": fmt.Sprintf("已删除 Intent %s", name),
	}
}

// ─── 5. builderToolUpdateLink ───────────────────────────────────────────────

// builderToolUpdateLink applies partial edits to a Link, optionally rebinding
// the property anchors. Anchor changes only execute on active links because
// only active links have causality rows to maintain; pending links defer
// anchor wiring to activation.
func builderToolUpdateLink(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	linkID := strings.TrimSpace(StrVal(args, "linkId"))
	if !IsValidUUID(linkID) {
		return M{"error": "linkId must be a valid UUID"}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return M{"error": "begin tx failed: " + err.Error()}
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 1. Ownership.
	var existingID, fromObjectID, toObjectID, name string
	var mark bool
	err = tx.QueryRowContext(ctx,
		`SELECT id, COALESCE(link_name,''), from_object_id, to_object_id, mark
		   FROM ont_link_type
		  WHERE id=$1 AND project_id=$2`,
		linkID, projectID).Scan(&existingID, &name, &fromObjectID, &toObjectID, &mark)
	if err == sql.ErrNoRows {
		return M{"error": "Link not found in this project"}
	}
	if err != nil {
		return M{"error": "ownership check failed: " + err.Error()}
	}

	// 2. Apply edits.
	updatedFields := []string{}
	if editsRaw, ok := args["edits"].(map[string]interface{}); ok && len(editsRaw) > 0 {
		setCols := []string{}
		setArgs := []interface{}{}
		appendCol := func(col string, val interface{}) {
			setCols = append(setCols, fmt.Sprintf("%s=$%d", col, len(setArgs)+1))
			setArgs = append(setArgs, val)
		}
		if v, ok := editsRaw["name"].(string); ok && v != "" {
			appendCol("link_name", v)
			updatedFields = append(updatedFields, "link_name")
		} else if v, ok := editsRaw["linkName"].(string); ok && v != "" {
			appendCol("link_name", v)
			updatedFields = append(updatedFields, "link_name")
		}
		if v, ok := editsRaw["description"].(string); ok {
			appendCol("description", v)
			updatedFields = append(updatedFields, "description")
		}
		if v, ok := editsRaw["fkColumn"].(string); ok {
			appendCol("fk_column", v)
			updatedFields = append(updatedFields, "fk_column")
		}
		if v, ok := editsRaw["cardinality"].(string); ok && v != "" {
			appendCol("cardinality", v)
			updatedFields = append(updatedFields, "cardinality")
		}
		if len(setCols) > 0 {
			setArgs = append(setArgs, linkID)
			q := fmt.Sprintf(`UPDATE ont_link_type SET %s, updated_at=now() WHERE id=$%d`,
				strings.Join(setCols, ", "), len(setArgs))
			if _, err := tx.ExecContext(ctx, q, setArgs...); err != nil {
				return M{"error": "apply Link edits failed: " + err.Error()}
			}
		}
	}

	// 3. Property anchor edits (active links only).
	anchorChanged := false
	anchorDeferred := false
	if anchorRaw, ok := args["propertyAnchorEdits"].(map[string]interface{}); ok && len(anchorRaw) > 0 {
		if !mark {
			anchorDeferred = true
		} else {
			newFromPID := strings.TrimSpace(StrVal(anchorRaw, "fromPropertyId"))
			newToPID := strings.TrimSpace(StrVal(anchorRaw, "toPropertyId"))
			if newFromPID != "" && !IsValidUUID(newFromPID) {
				return M{"error": "fromPropertyId must be a valid UUID"}
			}
			if newToPID != "" && !IsValidUUID(newToPID) {
				return M{"error": "toPropertyId must be a valid UUID"}
			}
			// Validate each provided property belongs to its expected object.
			if newFromPID != "" {
				var probe string
				err := tx.QueryRowContext(ctx,
					`SELECT id FROM ont_property
					  WHERE id=$1 AND object_type_id=$2 AND project_id=$3`,
					newFromPID, fromObjectID, projectID).Scan(&probe)
				if err == sql.ErrNoRows {
					return M{"error": "fromPropertyId does not belong to the link's from_object"}
				}
				if err != nil {
					return M{"error": "validate fromPropertyId failed: " + err.Error()}
				}
			}
			if newToPID != "" {
				var probe string
				err := tx.QueryRowContext(ctx,
					`SELECT id FROM ont_property
					  WHERE id=$1 AND object_type_id=$2 AND project_id=$3`,
					newToPID, toObjectID, projectID).Scan(&probe)
				if err == sql.ErrNoRows {
					return M{"error": "toPropertyId does not belong to the link's to_object"}
				}
				if err != nil {
					return M{"error": "validate toPropertyId failed: " + err.Error()}
				}
			}

			// Best-effort delete existing join_key causality rows linking
			// the two objects' properties.
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM ont_causality
				  WHERE project_id=$3 AND relation_type='join_key' AND (
				        from_knowledge_id IN (
				            SELECT id FROM ont_knowledge
				             WHERE anchor_type='property' AND anchor_id IN (
				                 SELECT id FROM ont_property WHERE object_type_id=$1
				             )
				        )
				    AND to_knowledge_id IN (
				            SELECT id FROM ont_knowledge
				             WHERE anchor_type='property' AND anchor_id IN (
				                 SELECT id FROM ont_property WHERE object_type_id=$2
				             )
				        )
				  )`, fromObjectID, toObjectID, projectID); err != nil {
				return M{"error": "delete existing causality rows failed: " + err.Error()}
			}

			// Ensure Ok knowledge exists for new anchors, then insert
			// causality. If only one side was provided, leave the other
			// side dangling — caller is responsible for full anchor pair.
			if newFromPID != "" && newToPID != "" {
				ensureKnowledge := func(propID string) (string, error) {
					var kid string
					_ = tx.QueryRowContext(ctx,
						`SELECT id FROM ont_knowledge
						  WHERE anchor_type='property' AND anchor_id=$1`,
						propID).Scan(&kid)
					if kid != "" {
						return kid, nil
					}
					var pname string
					if err := tx.QueryRowContext(ctx,
						`SELECT COALESCE(display_name, name, '')
						   FROM ont_property WHERE id=$1`,
						propID).Scan(&pname); err != nil {
						return "", fmt.Errorf("read property %s: %w", propID, err)
					}
					if pname == "" {
						pname = "property"
					}
					content := pname + " (auto-generated for join_key link)."
					err := tx.QueryRowContext(ctx,
						`INSERT INTO ont_knowledge
						   (project_id, anchor_type, anchor_id, entry_type,
						    title, summary, content, sort_order, mark, note)
						 VALUES ($1, 'property', $2, 'concept', $3, $4, $5, 0, true, '')
						 RETURNING id`,
						projectID, propID, pname, pname, content).Scan(&kid)
					return kid, err
				}
				fromKID, err := ensureKnowledge(newFromPID)
				if err != nil {
					return M{"error": "ensure from-property knowledge failed: " + err.Error()}
				}
				toKID, err := ensureKnowledge(newToPID)
				if err != nil {
					return M{"error": "ensure to-property knowledge failed: " + err.Error()}
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO ont_causality
					   (project_id, from_knowledge_id, to_knowledge_id,
					    relation_type, direction, description, mark)
					 VALUES ($1, $2, $3, 'join_key', 'positive',
					         'Updated via builder update_link.', true)`,
					projectID, fromKID, toKID); err != nil {
					return M{"error": "insert causality failed: " + err.Error()}
				}
				anchorChanged = true
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return M{"error": "commit failed: " + err.Error()}
	}
	committed = true

	resp := M{
		"linkId":        linkID,
		"updatedFields": updatedFields,
		"anchorChanged": anchorChanged,
		"summary_text":  fmt.Sprintf("Link %s 已更新", linkID),
	}
	if anchorDeferred {
		resp["warning"] = "Link is pending; property anchor changes are deferred to activation and were ignored"
	}
	return resp
}

// ─── 6. builderToolDeleteLink ───────────────────────────────────────────────

func builderToolDeleteLink(ctx context.Context, db *sql.DB, projectID string, args map[string]interface{}) M {
	linkID := strings.TrimSpace(StrVal(args, "linkId"))
	if !IsValidUUID(linkID) {
		return M{"error": "linkId must be a valid UUID"}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return M{"error": "begin tx failed: " + err.Error()}
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var name, fromObjectID, toObjectID string
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(link_name,''), from_object_id, to_object_id
		   FROM ont_link_type
		  WHERE id=$1 AND project_id=$2`,
		linkID, projectID).Scan(&name, &fromObjectID, &toObjectID)
	if err == sql.ErrNoRows {
		return M{"error": "Link not found in this project"}
	}
	if err != nil {
		return M{"error": "ownership check failed: " + err.Error()}
	}

	// Best-effort causality cleanup — match join_key rows whose endpoints'
	// property knowledge anchors lie in this link's from/to objects.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ont_causality
		  WHERE project_id=$3 AND relation_type='join_key' AND (
		        from_knowledge_id IN (
		            SELECT id FROM ont_knowledge
		             WHERE anchor_type='property' AND anchor_id IN (
		                 SELECT id FROM ont_property WHERE object_type_id=$1
		             )
		        )
		    AND to_knowledge_id IN (
		            SELECT id FROM ont_knowledge
		             WHERE anchor_type='property' AND anchor_id IN (
		                 SELECT id FROM ont_property WHERE object_type_id=$2
		             )
		        )
		  )`, fromObjectID, toObjectID, projectID); err != nil {
		return M{"error": "delete causality rows failed: " + err.Error()}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ont_link_type WHERE id=$1 AND project_id=$2`,
		linkID, projectID); err != nil {
		return M{"error": "delete Link failed: " + err.Error()}
	}

	if err := tx.Commit(); err != nil {
		return M{"error": "commit failed: " + err.Error()}
	}
	committed = true

	displayName := name
	if displayName == "" {
		displayName = linkID
	}
	return M{
		"linkId":       linkID,
		"name":         name,
		"summary_text": fmt.Sprintf("已删除 Link %s", displayName),
	}
}
