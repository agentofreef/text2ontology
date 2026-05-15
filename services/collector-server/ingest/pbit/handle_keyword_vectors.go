package pbit

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/lakehouse2ontology/llmclient"
)

// RecomputeVectorsForProject embeds all lakehouse_keyword rows (and their
// aliases) that are missing a vector for the given project. It is safe to call
// from a goroutine after a terminal transaction has committed — it does NOT run
// inside any transaction and uses its own short-lived DB connections.
//
// If the LLM embedding model is not configured or unavailable, the function
// logs a warning and returns nil (best-effort — does not fail the import).
func RecomputeVectorsForProject(ctx context.Context, db *sql.DB, projectID string) error {
	// Pre-flight: confirm embedding is configured.
	if _, err := llmclient.EmbedTexts(db, []string{"ping"}); err != nil {
		log.Printf("[pbitlakehouse] auto vector compute: embedding not available, skipping (project=%s): %v", projectID, err)
		return nil
	}

	// Self-heal: backfill child alias rows that exist in the parent array but
	// are missing from the child table (same logic as handleComputeVectors).
	if _, err := db.ExecContext(ctx, `
		INSERT INTO lakehouse_keyword_alias_vector (keyword_id, alias, alias_vector)
		SELECT lk.id, a, NULL
		  FROM lakehouse_keyword lk
		  CROSS JOIN LATERAL unnest(COALESCE(lk.aliases, '{}'::text[])) AS a
		 WHERE lk.project_id = $1
		   AND lk.aliases IS NOT NULL
		   AND array_length(lk.aliases, 1) > 0
		ON CONFLICT (keyword_id, alias) DO NOTHING`, projectID); err != nil {
		log.Printf("[pbitlakehouse] auto vector compute: alias backfill failed (project=%s): %v", projectID, err)
		// non-fatal — continue with whatever's already in the child table
	}

	jobs, err := collectVectorJobs(db, projectID)
	if err != nil {
		return fmt.Errorf("RecomputeVectorsForProject: collect jobs: %w", err)
	}
	if len(jobs) == 0 {
		log.Printf("[pbitlakehouse] auto vector compute: no missing vectors (project=%s)", projectID)
		return nil
	}

	log.Printf("[pbitlakehouse] auto vector compute: embedding %d rows (project=%s)", len(jobs), projectID)

	const batchSize = 32
	embedded, failed := 0, 0
	for start := 0; start < len(jobs); start += batchSize {
		// Respect context cancellation between batches.
		select {
		case <-ctx.Done():
			return fmt.Errorf("RecomputeVectorsForProject: context cancelled after %d/%d rows", embedded, len(jobs))
		default:
		}

		end := start + batchSize
		if end > len(jobs) {
			end = len(jobs)
		}
		batch := jobs[start:end]

		texts := make([]string, len(batch))
		for i, j := range batch {
			texts[i] = j.text
		}

		vecs, err := llmclient.EmbedTexts(db, texts)
		if err != nil || len(vecs) != len(batch) {
			msg := "embed batch failed"
			if err != nil {
				msg = err.Error()
			}
			log.Printf("[pbitlakehouse] auto vector compute: batch %d-%d: %s", start, end, msg)
			failed += len(batch)
			continue
		}

		n, err := writeVectorsBack(db, batch, vecs)
		if err != nil {
			log.Printf("[pbitlakehouse] auto vector compute: writeback error: %v", err)
			failed += len(batch) - n
		}
		embedded += n
	}

	log.Printf("[pbitlakehouse] auto vector compute: done (project=%s, embedded=%d, failed=%d)", projectID, embedded, failed)
	return nil
}

// ─── GET /api/pbit-lakehouse/lakehouse-keywords/vector-status?projectId=xxx ──
//
// Returns the current vector coverage so the UI can render the right-hand
// status badge:
//
//	{
//	  "keywords":     {"total":11399, "withVector":0, "missing":11399},
//	  "aliases":      {"total":23,    "withVector":0, "missing":23},
//	  "needsCompute": 11422
//	}
//
// "missing" + "withVector" = "total" by construction. "needsCompute" is the
// sum of the two missings — what the compute button enqueues.
func handleVectorStatus(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pid := strings.TrimSpace(r.URL.Query().Get("projectId"))
		if pid == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}

		// MC keywords/aliases are excluded from both totals and missing — they
		// never participate in vector matching, so counting them would make the
		// badge perpetually look "incomplete" and confuse the compute button.
		var kTotal, kMissing, aTotal, aMissing int
		err := db.QueryRow(`
			WITH k AS (
			    SELECT COUNT(*)                                       AS total,
			           COUNT(*) FILTER (WHERE lk.keyword_vector IS NULL) AS missing
			      FROM lakehouse_keyword lk
			      LEFT JOIN ont_property p ON p.id = lk.property_id
			     WHERE lk.project_id = $1
			       AND COALESCE(p.is_machine_code, false) = false
			       AND COALESCE(lk.is_machine_code, false) = false
			),
			a AS (
			    SELECT COUNT(*)                                       AS total,
			           COUNT(*) FILTER (WHERE la.alias_vector IS NULL) AS missing
			      FROM lakehouse_keyword_alias_vector la
			      JOIN lakehouse_keyword lk ON lk.id = la.keyword_id
			      LEFT JOIN ont_property p ON p.id = lk.property_id
			     WHERE lk.project_id = $1
			       AND COALESCE(p.is_machine_code, false) = false
			       AND COALESCE(lk.is_machine_code, false) = false
			)
			SELECT k.total, k.missing, a.total, a.missing FROM k, a`,
			pid,
		).Scan(&kTotal, &kMissing, &aTotal, &aMissing)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}

		jsonResp(w, 200, map[string]interface{}{
			"keywords": map[string]int{
				"total":      kTotal,
				"withVector": kTotal - kMissing,
				"missing":    kMissing,
			},
			"aliases": map[string]int{
				"total":      aTotal,
				"withVector": aTotal - aMissing,
				"missing":    aMissing,
			},
			"needsCompute": kMissing + aMissing,
		})
	}
}

// ─── POST /api/pbit-lakehouse/lakehouse-keywords/compute-vectors?projectId=xxx
//
// SSE stream that embeds every keyword/alias missing a vector for the given
// project. Events:
//
//	{"type":"start","total":N}
//	{"type":"progress","done":k,"total":N}
//	{"type":"error","msg":"..."}      // recoverable per-batch failure
//	{"type":"done","embedded":N,"failed":0}
//
// Batches are 32 texts per EmbedTexts call. A batch failure is reported via an
// "error" event and the loop continues — partial success is allowed (the
// failed rows simply remain NULL and will show up on the next status check).
//
// Idempotent: re-running only picks up rows whose vector is still NULL. Safe
// to interrupt halfway.
func handleComputeVectors(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pid := strings.TrimSpace(r.URL.Query().Get("projectId"))
		if pid == "" {
			jsonResp(w, 400, map[string]string{"error": "projectId required"})
			return
		}

		sseSetup(w)

		// Pre-flight: confirm embedding is configured. Without this we'd silently
		// fail every batch with no signal to the UI.
		if _, err := llmclient.EmbedTexts(db, []string{"ping"}); err != nil {
			writeSSE(w, map[string]interface{}{
				"type": "error",
				"msg":  "embedding 模型不可用: " + err.Error(),
			})
			writeSSE(w, map[string]interface{}{"type": "done", "embedded": 0, "failed": 0})
			return
		}

		// Self-heal: backfill child rows for any alias present in the parent
		// array but missing from the child table. Keeps things consistent even
		// if aliases were inserted via direct SQL or pre-date the child table.
		if _, err := db.Exec(`
			INSERT INTO lakehouse_keyword_alias_vector (keyword_id, alias, alias_vector)
			SELECT lk.id, a, NULL
			  FROM lakehouse_keyword lk
			  CROSS JOIN LATERAL unnest(COALESCE(lk.aliases, '{}'::text[])) AS a
			 WHERE lk.project_id = $1
			   AND lk.aliases IS NOT NULL
			   AND array_length(lk.aliases, 1) > 0
			ON CONFLICT (keyword_id, alias) DO NOTHING`, pid); err != nil {
			log.Printf("compute-vectors: alias backfill failed: %v", err)
			// non-fatal — continue with whatever's already in the child table
		}

		jobs, err := collectVectorJobs(db, pid)
		if err != nil {
			writeSSE(w, map[string]interface{}{"type": "error", "msg": err.Error()})
			writeSSE(w, map[string]interface{}{"type": "done", "embedded": 0, "failed": 0})
			return
		}

		total := len(jobs)
		writeSSE(w, map[string]interface{}{"type": "start", "total": total})
		if total == 0 {
			writeSSE(w, map[string]interface{}{"type": "done", "embedded": 0, "failed": 0})
			return
		}

		const batchSize = 32
		done, failed := 0, 0
		for start := 0; start < total; start += batchSize {
			end := start + batchSize
			if end > total {
				end = total
			}
			batch := jobs[start:end]

			texts := make([]string, len(batch))
			for i, j := range batch {
				texts[i] = j.text
			}

			vecs, err := llmclient.EmbedTexts(db, texts)
			if err != nil || len(vecs) != len(batch) {
				msg := "embed batch failed"
				if err != nil {
					msg = err.Error()
				}
				writeSSE(w, map[string]interface{}{"type": "error", "msg": msg})
				failed += len(batch)
				continue
			}

			n, err := writeVectorsBack(db, batch, vecs)
			if err != nil {
				log.Printf("compute-vectors writeback error: %v", err)
				writeSSE(w, map[string]interface{}{"type": "error", "msg": err.Error()})
				failed += len(batch) - n
			}
			done += n
			writeSSE(w, map[string]interface{}{
				"type":  "progress",
				"done":  done,
				"total": total,
			})
		}

		writeSSE(w, map[string]interface{}{
			"type":     "done",
			"embedded": done,
			"failed":   failed,
		})
	}
}

// vectorJob represents one row that needs embedding. kind="keyword" updates
// lakehouse_keyword.keyword_vector by id; kind="alias" updates
// lakehouse_keyword_alias_vector.alias_vector by (keyword_id, alias).
type vectorJob struct {
	kind      string // "keyword" | "alias"
	keywordID string // lakehouse_keyword.id (always populated)
	alias     string // only for kind=="alias"
	text      string // text to embed
}

// collectVectorJobs scans both tables for rows with NULL vectors. Order:
// keywords first, then aliases — keeps the UI's progress bar stable.
//
// Machine-code keywords are excluded — their values are opaque IDs (e.g.
// '12345', 'ABC123') for which embedding similarity is meaningless. They are
// never used by Tier 4 correction nor by Tier 3 VEC recall, so embedding
// them just wastes API calls and bloats the badge "missing" count.
func collectVectorJobs(db *sql.DB, projectID string) ([]vectorJob, error) {
	var jobs []vectorJob

	// Keywords missing a vector (non-MC only).
	rows, err := db.Query(`
		SELECT lk.id::text, lk.keyword
		  FROM lakehouse_keyword lk
		  LEFT JOIN ont_property p ON p.id = lk.property_id
		 WHERE lk.project_id = $1
		   AND lk.keyword_vector IS NULL
		   AND lk.keyword IS NOT NULL AND lk.keyword <> ''
		   AND COALESCE(p.is_machine_code, false) = false
		   AND COALESCE(lk.is_machine_code, false) = false
		 ORDER BY lk.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("scan keywords: %w", err)
	}
	for rows.Next() {
		var id, kw string
		if err := rows.Scan(&id, &kw); err == nil {
			jobs = append(jobs, vectorJob{kind: "keyword", keywordID: id, text: kw})
		}
	}
	rows.Close()

	// Aliases missing a vector. Filter by parent's project_id and skip MC.
	rows, err = db.Query(`
		SELECT lk.id::text, la.alias
		  FROM lakehouse_keyword_alias_vector la
		  JOIN lakehouse_keyword lk ON lk.id = la.keyword_id
		  LEFT JOIN ont_property p ON p.id = lk.property_id
		 WHERE lk.project_id = $1
		   AND la.alias_vector IS NULL
		   AND la.alias IS NOT NULL AND la.alias <> ''
		   AND COALESCE(p.is_machine_code, false) = false
		   AND COALESCE(lk.is_machine_code, false) = false
		 ORDER BY lk.id, la.alias`, projectID)
	if err != nil {
		return nil, fmt.Errorf("scan aliases: %w", err)
	}
	for rows.Next() {
		var id, alias string
		if err := rows.Scan(&id, &alias); err == nil {
			jobs = append(jobs, vectorJob{kind: "alias", keywordID: id, alias: alias, text: alias})
		}
	}
	rows.Close()

	return jobs, nil
}

// writeVectorsBack does two bulk UPDATEs (one per target table), wrapped in a
// transaction. Returns the number of rows successfully updated.
func writeVectorsBack(db *sql.DB, batch []vectorJob, vecs [][]float64) (int, error) {
	// Partition by kind.
	type kRow struct {
		id     string
		vecStr string
	}
	type aRow struct {
		id, alias, vecStr string
	}
	var kRows []kRow
	var aRows []aRow
	for i, j := range batch {
		if i >= len(vecs) || len(vecs[i]) == 0 {
			continue
		}
		vecStr := vectorToPgString(vecs[i])
		switch j.kind {
		case "keyword":
			kRows = append(kRows, kRow{id: j.keywordID, vecStr: vecStr})
		case "alias":
			aRows = append(aRows, aRow{id: j.keywordID, alias: j.alias, vecStr: vecStr})
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("tx begin: %w", err)
	}
	defer tx.Rollback()

	written := 0

	// Bulk UPDATE keywords via VALUES.
	if len(kRows) > 0 {
		var parts []string
		var args []interface{}
		argIdx := 1
		for _, r := range kRows {
			parts = append(parts, fmt.Sprintf("($%d::uuid, $%d::vector)", argIdx, argIdx+1))
			args = append(args, r.id, r.vecStr)
			argIdx += 2
		}
		q := fmt.Sprintf(`
			UPDATE lakehouse_keyword
			   SET keyword_vector = v.vec, updated_at = now()
			  FROM (VALUES %s) AS v(id, vec)
			 WHERE lakehouse_keyword.id = v.id`,
			strings.Join(parts, ", "))
		if res, err := tx.Exec(q, args...); err != nil {
			return 0, fmt.Errorf("update keyword_vector: %w", err)
		} else if n, _ := res.RowsAffected(); n > 0 {
			written += int(n)
		}
	}

	// Bulk UPDATE alias vectors via VALUES.
	if len(aRows) > 0 {
		var parts []string
		var args []interface{}
		argIdx := 1
		for _, r := range aRows {
			parts = append(parts, fmt.Sprintf("($%d::uuid, $%d::text, $%d::vector)", argIdx, argIdx+1, argIdx+2))
			args = append(args, r.id, r.alias, r.vecStr)
			argIdx += 3
		}
		q := fmt.Sprintf(`
			UPDATE lakehouse_keyword_alias_vector
			   SET alias_vector = v.vec, updated_at = now()
			  FROM (VALUES %s) AS v(keyword_id, alias, vec)
			 WHERE lakehouse_keyword_alias_vector.keyword_id = v.keyword_id
			   AND lakehouse_keyword_alias_vector.alias      = v.alias`,
			strings.Join(parts, ", "))
		if res, err := tx.Exec(q, args...); err != nil {
			return 0, fmt.Errorf("update alias_vector: %w", err)
		} else if n, _ := res.RowsAffected(); n > 0 {
			written += int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("tx commit: %w", err)
	}
	return written, nil
}

// vectorToPgString formats a float vector as the pgvector text literal
// "[v1,v2,...]". Matches the format vectordb.ProcessBatch uses.
func vectorToPgString(vec []float64) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
