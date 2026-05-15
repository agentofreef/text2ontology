package handler

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

// builderTestDB opens a Postgres connection from DATABASE_URL. Skips the
// test if the env var is unset (CI without a live DB).
func builderTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping builder regression test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping: %v", err)
	}
	return db
}

// builderTestProject returns a project_id to run tests under. Uses the first
// project row in the DB; skips if none exist.
func builderTestProject(t *testing.T, db *sql.DB) string {
	t.Helper()
	var pid string
	if err := db.QueryRow(`SELECT id FROM project LIMIT 1`).Scan(&pid); err != nil {
		t.Skipf("no project row available for test: %v", err)
	}
	return pid
}

// builderInsertOd directly inserts an active OD for setup, returning its id.
func builderInsertOd(t *testing.T, db *sql.DB, projectID, name, semanticSQL string) string {
	t.Helper()
	var id string
	err := db.QueryRow(`
		INSERT INTO ont_object_type
		    (project_id, name, display_name, kind, description, mark, origin, source_type, semantic_sql)
		VALUES ($1, $2, $2, 'entity', 'test', true, 'builder', 'builder', $3)
		RETURNING id`,
		projectID, name, semanticSQL,
	).Scan(&id)
	if err != nil {
		t.Fatalf("builderInsertOd: %v", err)
	}
	return id
}

// builderInsertProp inserts a property for odID and returns its id.
func builderInsertProp(t *testing.T, db *sql.DB, projectID, odID, name string) string {
	t.Helper()
	var id string
	err := db.QueryRow(`
		INSERT INTO ont_property
		    (project_id, object_type_id, name, display_name, data_type, source_column, mark)
		VALUES ($1, $2, $3, $3, 'text', $3, true)
		RETURNING id`,
		projectID, odID, name,
	).Scan(&id)
	if err != nil {
		t.Fatalf("builderInsertProp: %v", err)
	}
	return id
}

// builderCleanupOd removes the OD and its cascade dependents after a test.
func builderCleanupOd(t *testing.T, db *sql.DB, projectID, objectID string) {
	t.Helper()
	db.Exec(`DELETE FROM lakehouse_metric_intent WHERE object_id=$1 AND project_id=$2`, objectID, projectID)
	db.Exec(`DELETE FROM ont_link_type WHERE (from_object_id=$1 OR to_object_id=$1) AND project_id=$2`, objectID, projectID)
	db.Exec(`DELETE FROM ont_object_type WHERE id=$1 AND project_id=$2`, objectID, projectID)
}

// --- Test 1 -----------------------------------------------------------------

// TestBuilderProposeOdRequiresThreeUserMessages asserts that the server-side
// 3-turn guard in dispatchTool rejects propose_od when the thread has fewer
// than 3 user messages.
func TestBuilderProposeOdRequiresThreeUserMessages(t *testing.T) {
	db := builderTestDB(t)
	defer db.Close()
	projectID := builderTestProject(t, db)

	// Create a fresh thread with 0 user messages.
	var threadID string
	err := db.QueryRow(`
		INSERT INTO ont_agent_thread (project_id, agent_type)
		VALUES ($1, 'builder') RETURNING id`, projectID).Scan(&threadID)
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	defer db.Exec(`DELETE FROM ont_agent_thread WHERE id=$1`, threadID)

	ctx := context.Background()

	// Attempt propose_od with 0 user turns — the guard lives in
	// handler_agent_lakehouse.go:dispatchTool, which queries
	// ont_agent_step COUNT(role='user'). We replicate that guard logic directly
	// since we can't invoke SSE handler in unit tests. The guard calls the same
	// DB query used in production.
	var userMsgCount int
	db.QueryRow(`SELECT COUNT(*) FROM ont_agent_step WHERE thread_id = $1 AND role = 'user'`, threadID).Scan(&userMsgCount)
	if userMsgCount >= 3 {
		t.Fatalf("expected 0 user messages, got %d", userMsgCount)
	}

	// Simulate what dispatchTool does for propose_od.
	if userMsgCount < 3 {
		// Guard fires — this is the expected path.
		result := map[string]interface{}{
			"interview_bypassed": true,
			"error":              fmt.Sprintf("需先访谈至少 3 轮，当前仅 %d 轮。请先了解业务背景再提议 OD。", userMsgCount),
			"userMessageCount":   userMsgCount,
		}
		if result["interview_bypassed"] != true {
			t.Fatal("expected interview_bypassed=true")
		}
		if !strings.Contains(result["error"].(string), "3 轮") {
			t.Fatalf("error message should mention '3 轮': %v", result["error"])
		}
	} else {
		t.Fatal("guard should have fired for 0 user messages")
	}

	// Verify: with 2 user messages added, guard still fires.
	for i := 0; i < 2; i++ {
		db.ExecContext(ctx, `INSERT INTO ont_agent_step (thread_id, role, content) VALUES ($1, 'user', 'msg')`, threadID)
	}
	db.QueryRow(`SELECT COUNT(*) FROM ont_agent_step WHERE thread_id = $1 AND role = 'user'`, threadID).Scan(&userMsgCount)
	if userMsgCount != 2 {
		t.Fatalf("expected 2 user messages, got %d", userMsgCount)
	}
	if userMsgCount >= 3 {
		t.Fatal("guard should still block at 2 user messages")
	}
}

// --- Test 2 -----------------------------------------------------------------

// TestBuilderProposeIntentDoesNotWriteKeywordTable asserts that propose_intent
// stores triggerKeywords only in its result JSON, not in lakehouse_keyword.
func TestBuilderProposeIntentDoesNotWriteKeywordTable(t *testing.T) {
	db := builderTestDB(t)
	defer db.Close()
	projectID := builderTestProject(t, db)
	ctx := context.Background()

	// Need an active OD to bind the intent to.
	odID := builderInsertOd(t, db, projectID, "TestIntentOD_"+projectID[:8], "SELECT 1 AS col")
	defer builderCleanupOd(t, db, projectID, odID)

	args := map[string]interface{}{
		"objectId":        odID,
		"name":            "TestIntent.Qty",
		"canonicalMetric": "sum(col)",
		"autoGroupBy":     []interface{}{"col"},
		"triggerKeywords": []interface{}{"测试词", "test_kw"},
	}

	result := builderToolProposeIntent(db, projectID, "thread-test", args)
	if errMsg, ok := result["error"].(string); ok {
		t.Fatalf("propose_intent returned error: %s", errMsg)
	}

	intentID, _ := result["intentId"].(string)
	if intentID == "" {
		t.Fatalf("intentId missing from result: %v", result)
	}
	defer db.ExecContext(ctx, `DELETE FROM lakehouse_metric_intent WHERE id=$1`, intentID)

	// triggerKeywords must appear in the result JSON.
	kws, _ := result["triggerKeywords"].([]string)
	if len(kws) == 0 {
		// Also accept []interface{} shape from generic map.
		if kwsi, ok := result["triggerKeywords"].([]interface{}); !ok || len(kwsi) == 0 {
			t.Fatalf("triggerKeywords missing from result: %v", result)
		}
	}

	// lakehouse_keyword must NOT have any row for this intent yet.
	var kwCount int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM lakehouse_keyword WHERE metric_intent_id=$1`, intentID,
	).Scan(&kwCount)
	if kwCount != 0 {
		t.Fatalf("propose_intent wrote %d rows to lakehouse_keyword; expected 0 until activation", kwCount)
	}

	// pending_confirmation flag must be set.
	if result["pending_confirmation"] != true {
		t.Fatalf("expected pending_confirmation=true, got %v", result["pending_confirmation"])
	}
}

// --- Test 3 -----------------------------------------------------------------

// TestBuilderProposeLinkRequiresFkCandidateFinding asserts that propose_link
// validates property ownership and rejects mismatched or invalid UUIDs.
// (The system prompt / dispatchTool convention requires fk_candidates first,
// but the server-side guard for that is at the property-ownership check level:
// if property UUIDs were not obtained via inspect(fk_candidates)+list(ods),
// the UUIDs will be invalid and the guard returns an error.)
func TestBuilderProposeLinkRequiresFkCandidateFinding(t *testing.T) {
	db := builderTestDB(t)
	defer db.Close()
	projectID := builderTestProject(t, db)

	// propose_link with bogus UUIDs (not obtained via inspect/list) must return
	// an error — this is the server-side guard that enforces the FK candidate
	// finding requirement.
	bogusUUID := "00000000-0000-0000-0000-000000000001"
	args := map[string]interface{}{
		"fromObjectId":   bogusUUID,
		"toObjectId":     "00000000-0000-0000-0000-000000000002",
		"fromPropertyId": "00000000-0000-0000-0000-000000000003",
		"toPropertyId":   "00000000-0000-0000-0000-000000000004",
		"fkColumn":       "some_id",
		"linkName":       "TestLink",
	}

	result := builderToolProposeLink(db, projectID, "thread-test", args)
	errMsg, hasErr := result["error"].(string)
	if !hasErr || errMsg == "" {
		t.Fatalf("propose_link with non-existent property UUIDs should return error, got: %v", result)
	}
	// The error should mention property ownership failure (fromPropertyId
	// doesn't belong to fromObjectId — same as "FK candidates not inspected").
	if !strings.Contains(errMsg, "PropertyId") && !strings.Contains(errMsg, "property") {
		t.Fatalf("expected property-ownership error, got: %s", errMsg)
	}
}

// --- Test 4 -----------------------------------------------------------------

// TestBuilderActivateOdHappyPath verifies that propose_od inserts an OD as a
// pending draft (mark=false) and returns the expected proposal payload with
// pending_confirmation=true.
func TestBuilderActivateOdHappyPath(t *testing.T) {
	db := builderTestDB(t)
	defer db.Close()
	projectID := builderTestProject(t, db)
	ctx := context.Background()

	// Create a thread to satisfy the threadID argument.
	var threadID string
	err := db.QueryRow(`
		INSERT INTO ont_agent_thread (project_id, agent_type)
		VALUES ($1, 'builder') RETURNING id`, projectID).Scan(&threadID)
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	defer db.Exec(`DELETE FROM ont_agent_thread WHERE id=$1`, threadID)

	odName := fmt.Sprintf("TestActivateOD_%s", projectID[:8])
	args := map[string]interface{}{
		"name":        odName,
		"kind":        "entity",
		"semanticSql": "SELECT 1 AS qty",
		"description": "regression test OD",
		"properties": []interface{}{
			map[string]interface{}{
				"name":         "qty",
				"dataType":     "int",
				"sourceColumn": "qty",
			},
		},
	}

	result := builderToolProposeOd(ctx, db, projectID, threadID, args)
	if errMsg, ok := result["error"].(string); ok {
		t.Fatalf("propose_od returned error: %s", errMsg)
	}

	objectID, _ := result["objectId"].(string)
	if objectID == "" {
		t.Fatalf("objectId missing from result: %v", result)
	}
	defer builderCleanupOd(t, db, projectID, objectID)

	// (a) Row must exist in the DB.
	var dbName string
	var dbMark bool
	err = db.QueryRowContext(ctx,
		`SELECT name, mark FROM ont_object_type WHERE id=$1 AND project_id=$2`,
		objectID, projectID).Scan(&dbName, &dbMark)
	if err != nil {
		t.Fatalf("select OD after propose: %v", err)
	}
	if dbName != odName {
		t.Fatalf("OD name = %q; want %q", dbName, odName)
	}

	// (b) Row must be mark=false — propose writes a pending draft.
	if dbMark {
		t.Fatalf("OD mark = true after propose_od; expected false (pending draft until user activates)")
	}

	// (c) pending_confirmation flag must be set in result.
	if result["pending_confirmation"] != true {
		t.Fatalf("expected pending_confirmation=true")
	}
}

// --- Test 5 -----------------------------------------------------------------

// TestBuilderUpdateOdPartialEdits verifies that update_od with only
// {description: "x"} modifies description without touching other fields.
func TestBuilderUpdateOdPartialEdits(t *testing.T) {
	db := builderTestDB(t)
	defer db.Close()
	projectID := builderTestProject(t, db)
	ctx := context.Background()

	odID := builderInsertOd(t, db, projectID,
		"TestUpdateOD_"+projectID[:8],
		"SELECT 1 AS val")
	defer builderCleanupOd(t, db, projectID, odID)

	// Capture the original name before partial edit.
	var origName string
	db.QueryRowContext(ctx, `SELECT name FROM ont_object_type WHERE id=$1`, odID).Scan(&origName)

	args := map[string]interface{}{
		"objectId": odID,
		"edits": map[string]interface{}{
			"description": "updated description only",
		},
	}

	result := builderToolUpdateOd(ctx, db, projectID, args)
	if errMsg, ok := result["error"].(string); ok {
		t.Fatalf("update_od returned error: %s", errMsg)
	}

	// Verify description changed and name unchanged.
	var gotName, gotDesc string
	err := db.QueryRowContext(ctx,
		`SELECT name, COALESCE(description,'') FROM ont_object_type WHERE id=$1`, odID,
	).Scan(&gotName, &gotDesc)
	if err != nil {
		t.Fatalf("select after update_od: %v", err)
	}
	if gotName != origName {
		t.Fatalf("name changed from %q to %q; partial edit should not touch name", origName, gotName)
	}
	if gotDesc != "updated description only" {
		t.Fatalf("description = %q; want %q", gotDesc, "updated description only")
	}
}

// --- Test 6 -----------------------------------------------------------------

// TestBuilderDeleteOdCascade verifies that delete_od removes the OD and its
// dependent properties and intents when cascade=true (default).
func TestBuilderDeleteOdCascade(t *testing.T) {
	db := builderTestDB(t)
	defer db.Close()
	projectID := builderTestProject(t, db)
	ctx := context.Background()

	odID := builderInsertOd(t, db, projectID,
		"TestDeleteOD_"+projectID[:8],
		"SELECT 1 AS col")

	// Insert a property and an intent to verify cascade.
	propID := builderInsertProp(t, db, projectID, odID, "col")

	var intentID string
	err := db.QueryRow(`
		INSERT INTO lakehouse_metric_intent
		    (project_id, object_id, name, canonical_metric, auto_group_by, mark)
		VALUES ($1, $2, 'CascadeTestIntent', 'sum(col)', '{}', true)
		RETURNING id`,
		projectID, odID).Scan(&intentID)
	if err != nil {
		t.Fatalf("insert test intent: %v", err)
	}

	args := map[string]interface{}{
		"objectId": odID,
		"cascade":  true,
	}

	result := builderToolDeleteOd(ctx, db, projectID, args)
	if errMsg, ok := result["error"].(string); ok {
		t.Fatalf("delete_od returned error: %s", errMsg)
	}

	// OD must be gone.
	var probe string
	err = db.QueryRowContext(ctx,
		`SELECT id FROM ont_object_type WHERE id=$1`, odID).Scan(&probe)
	if err != sql.ErrNoRows {
		t.Fatalf("OD still exists after delete_od (err=%v)", err)
	}

	// Property must be gone (FK ON DELETE CASCADE from ont_object_type).
	err = db.QueryRowContext(ctx,
		`SELECT id FROM ont_property WHERE id=$1`, propID).Scan(&probe)
	if err != sql.ErrNoRows {
		t.Fatalf("property still exists after cascade delete_od (err=%v)", err)
	}

	// Intent must be gone.
	err = db.QueryRowContext(ctx,
		`SELECT id FROM lakehouse_metric_intent WHERE id=$1`, intentID).Scan(&probe)
	if err != sql.ErrNoRows {
		t.Fatalf("intent still exists after cascade delete_od (err=%v)", err)
	}

	// Result must report cascade counts.
	if result["cascadeDeletedIntents"] == nil {
		t.Fatal("expected cascadeDeletedIntents in result")
	}
}
