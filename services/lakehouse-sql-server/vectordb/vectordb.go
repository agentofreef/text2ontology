package vectordb

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/llmclient"
)

type TextRow struct {
	ID   string
	Text string
}

func ComputeVectors(db *sql.DB, table, textCol, vecCol, projectId string) (int, error) {
	return ComputeVectorsWithProgress(db, table, textCol, vecCol, projectId, nil)
}

func ComputeVectorsWithProgress(db *sql.DB, table, textCol, vecCol, projectId string, progressFn func(done, total int)) (int, error) {
	var pending []TextRow

	// 1) Rows with non-empty text but no vector
	q := fmt.Sprintf(`SELECT id, %s FROM %s WHERE project_id = $1 AND %s IS NULL AND %s IS NOT NULL AND %s != ''`,
		textCol, table, vecCol, textCol, textCol)
	rows, err := db.Query(q, projectId)
	if err != nil {
		return 0, fmt.Errorf("query rows: %w", err)
	}
	for rows.Next() {
		var tr TextRow
		rows.Scan(&tr.ID, &tr.Text)
		if tr.Text != "" {
			pending = append(pending, tr)
		}
	}
	rows.Close()

	// 2) Rows with empty text and no vector — use fallback text
	if table == "column_explanation" {
		fallbackQ := `SELECT ce.id, st.table_name || ': ' || ce.column_name
			FROM column_explanation ce JOIN semantic_table st ON ce.table_id = st.id
			WHERE ce.project_id = $1 AND ce.column_vector IS NULL AND (ce.column_explain IS NULL OR ce.column_explain = '')`
		fRows, err := db.Query(fallbackQ, projectId)
		if err == nil {
			for fRows.Next() {
				var tr TextRow
				fRows.Scan(&tr.ID, &tr.Text)
				if tr.Text != "" {
					pending = append(pending, tr)
				}
			}
			fRows.Close()
		}
	} else if table == "measure_explanation" {
		fallbackQ := fmt.Sprintf(`SELECT id, measure_name FROM %s WHERE project_id = $1 AND %s IS NULL AND (measure_explain IS NULL OR measure_explain = '')`, table, vecCol)
		fRows, err := db.Query(fallbackQ, projectId)
		if err == nil {
			for fRows.Next() {
				var tr TextRow
				fRows.Scan(&tr.ID, &tr.Text)
				if tr.Text != "" {
					pending = append(pending, tr)
				}
			}
			fRows.Close()
		}
	} else if table == "keyword_explanation" {
		fallbackQ := fmt.Sprintf(`SELECT id, keyword FROM %s WHERE project_id = $1 AND %s IS NULL AND (keyword_explain IS NULL OR keyword_explain = '')`, table, vecCol)
		fRows, err := db.Query(fallbackQ, projectId)
		if err == nil {
			for fRows.Next() {
				var tr TextRow
				fRows.Scan(&tr.ID, &tr.Text)
				if tr.Text != "" {
					pending = append(pending, tr)
				}
			}
			fRows.Close()
		}
	} else if table == "lakehouse_keyword" {
		fallbackQ := fmt.Sprintf(`SELECT id, keyword FROM %s WHERE project_id = $1 AND %s IS NULL AND keyword != ''`, table, vecCol)
		fRows, err := db.Query(fallbackQ, projectId)
		if err == nil {
			for fRows.Next() {
				var tr TextRow
				fRows.Scan(&tr.ID, &tr.Text)
				if tr.Text != "" {
					pending = append(pending, tr)
				}
			}
			fRows.Close()
		}
	}

	if len(pending) == 0 {
		return 0, nil
	}

	// Determine embedding endpoint
	embBaseURL, embAPIKey, embModel := llmclient.GetActiveEmbeddingConfig(db)
	if embBaseURL == "" {
		embBaseURL = "http://localhost:8132"
		embModel = "bge-large-zh-v1.5"
	}
	embURL := llmclient.BuildURL(embBaseURL, "/embeddings")

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 8, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	// Split into batches
	batchSize := 32
	type batch struct {
		items []TextRow
	}
	var batches []batch
	for i := 0; i < len(pending); i += batchSize {
		end := i + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batches = append(batches, batch{items: pending[i:end]})
	}

	// Concurrent worker pool
	numWorkers := 4
	if len(batches) < numWorkers {
		numWorkers = len(batches)
	}
	var vectorized int64
	var done int64
	total := len(pending)

	ch := make(chan batch, len(batches))
	for _, b := range batches {
		ch <- b
	}
	close(ch)

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range ch {
				n := ProcessBatch(client, embURL, embAPIKey, embModel, table, vecCol, db, b.items)
				atomic.AddInt64(&vectorized, int64(n))
				newDone := atomic.AddInt64(&done, int64(len(b.items)))
				if progressFn != nil {
					progressFn(int(newDone), total)
				}
			}
		}()
	}
	wg.Wait()

	return int(vectorized), nil
}

// ProcessBatch calls embedding API for a batch and does a bulk UPDATE.
func ProcessBatch(client *http.Client, embURL, embAPIKey, embModel, table, vecCol string, db *sql.DB, items []TextRow) int {
	var texts []string
	for _, tr := range items {
		texts = append(texts, tr.Text)
	}

	reqBody, _ := json.Marshal(M{
		"model": embModel,
		"input": texts,
	})

	req, _ := http.NewRequest("POST", embURL, strings.NewReader(string(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	if embAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+embAPIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Embedding API error: %v", err)
		return 0
	}
	defer resp.Body.Close()

	var embResp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		log.Printf("Embedding decode error: %v", err)
		return 0
	}

	if len(embResp.Data) == 0 {
		return 0
	}

	var valParts []string
	var args []interface{}
	argIdx := 1
	for _, emb := range embResp.Data {
		if emb.Index >= len(items) {
			continue
		}
		tr := items[emb.Index]
		parts := make([]string, len(emb.Embedding))
		for j, v := range emb.Embedding {
			parts[j] = strconv.FormatFloat(v, 'f', -1, 64)
		}
		vecStr := "[" + strings.Join(parts, ",") + "]"
		valParts = append(valParts, fmt.Sprintf("($%d::uuid, $%d::vector)", argIdx, argIdx+1))
		args = append(args, tr.ID, vecStr)
		argIdx += 2
	}

	if len(valParts) == 0 {
		return 0
	}

	updateQ := fmt.Sprintf(`UPDATE %s SET %s = v.vec, updated_at = now() FROM (VALUES %s) AS v(id, vec) WHERE %s.id = v.id`,
		table, vecCol, strings.Join(valParts, ", "), table)
	_, err = db.Exec(updateQ, args...)
	if err != nil {
		log.Printf("Batch vector update error for %s: %v", table, err)
		return 0
	}

	return len(valParts)
}
