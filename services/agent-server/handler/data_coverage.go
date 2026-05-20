package handler

// data_coverage.go — computes the actual time window the project's data
// covers, so the agent prompt can tell the LLM where the data really lives
// instead of letting it default to the wall-clock year.
//
// Why this exists: the "## 日期参考" block injects time.Now(). When the
// data lags the wall clock (a demo dataset frozen at 2025, queried in 2026),
// a question with no explicit time silently resolves to the current year and
// returns zero rows — and the cell-ref renderer then leaks the raw
// 「sum(tN.col)」 token because there is nothing to compute. Surfacing the
// real coverage window lets the LLM default to the latest available period.
//
// Best-effort: any failure returns "" and the caller simply omits the line,
// preserving the previous behavior. One query per turn, cached per project.

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/lakehouse2ontology/services/agent-server/recall"
)

// coverageCache memoises the computed window per project for a short TTL —
// data coverage rarely changes within a session and the MIN/MAX scan, while
// cheap, is not free over a multi-million-row fact table.
var (
	coverageCacheMu  sync.Mutex
	coverageCache    = map[string]coverageEntry{}
	coverageCacheTTL = 10 * time.Minute
)

type coverageEntry struct {
	window   string
	computed time.Time
}

// dataCoverageWindow returns a human string like "2024-01-01 ~ 2025-12-30"
// spanning every date/timestamp column of the objects the recalled Intents
// reference, or "" when it cannot be determined.
func dataCoverageWindow(db *sql.DB, projectID string, intents []recall.MetricIntent) string {
	if db == nil || projectID == "" || len(intents) == 0 {
		return ""
	}

	coverageCacheMu.Lock()
	if e, ok := coverageCache[projectID]; ok && time.Since(e.computed) < coverageCacheTTL {
		coverageCacheMu.Unlock()
		return e.window
	}
	coverageCacheMu.Unlock()

	window := computeDataCoverage(db, projectID, intents)

	coverageCacheMu.Lock()
	coverageCache[projectID] = coverageEntry{window: window, computed: time.Now()}
	coverageCacheMu.Unlock()
	return window
}

func computeDataCoverage(db *sql.DB, projectID string, intents []recall.MetricIntent) string {
	// 1. Resolve the project's physical lakehouse schema.
	var schema string
	if err := db.QueryRow(`SELECT COALESCE(lakehouse_schema,'') FROM project WHERE id = $1`, projectID).Scan(&schema); err != nil || schema == "" {
		return ""
	}

	// 2. Collect the source tables of the objects the recalled Intents touch,
	//    AND the property names those Intents actually filter on. We only
	//    measure coverage over the time columns the Intents FILTER on (e.g.
	//    placed_at, posted_date) — never over every date column on the table.
	//    Entity tables carry lifecycle dates (a store's open_date / close_date)
	//    that have nothing to do with how much fact data exists; including them
	//    would stretch the window to a store opened in 2020 or closing in 2027
	//    and defeat the whole point.
	objectIDs := make([]string, 0, len(intents))
	seenObj := map[string]bool{}
	filterProps := map[string]bool{}
	for _, mi := range intents {
		if mi.ObjectID != "" && !seenObj[mi.ObjectID] {
			seenObj[mi.ObjectID] = true
			objectIDs = append(objectIDs, mi.ObjectID)
		}
		for _, p := range mi.Parameters {
			if p.Property != "" {
				filterProps[p.Property] = true
			}
		}
	}
	if len(objectIDs) == 0 || len(filterProps) == 0 {
		return ""
	}
	props := make([]string, 0, len(filterProps))
	for p := range filterProps {
		props = append(props, p)
	}

	tableRows, err := db.Query(
		`SELECT DISTINCT source_table FROM ont_object_type
		 WHERE project_id = $1 AND id = ANY($2) AND COALESCE(source_table,'') <> ''`,
		projectID, pq.Array(objectIDs))
	if err != nil {
		return ""
	}
	var tables []string
	for tableRows.Next() {
		var t string
		if err := tableRows.Scan(&t); err == nil && t != "" {
			tables = append(tables, t)
		}
	}
	tableRows.Close()
	if len(tables) == 0 {
		return ""
	}

	// 3. Of those tables' columns, keep only the ones that (a) an Intent
	//    actually filters on AND (b) are date/timestamp typed.
	colRows, err := db.Query(
		`SELECT table_name, column_name FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = ANY($2) AND column_name = ANY($3)
		   AND data_type IN ('date','timestamp without time zone','timestamp with time zone')`,
		schema, pq.Array(tables), pq.Array(props))
	if err != nil {
		return ""
	}
	type tc struct{ table, col string }
	var datecols []tc
	for colRows.Next() {
		var t, c string
		if err := colRows.Scan(&t, &c); err == nil {
			datecols = append(datecols, tc{table: t, col: c})
		}
	}
	colRows.Close()
	if len(datecols) == 0 {
		return ""
	}

	// 4. One query: per (table,col) min/max, then global min/max.
	var parts []string
	for _, dc := range datecols {
		parts = append(parts, fmt.Sprintf(
			`SELECT MIN(%s) lo, MAX(%s) hi FROM %s.%s`,
			pq.QuoteIdentifier(dc.col), pq.QuoteIdentifier(dc.col),
			pq.QuoteIdentifier(schema), pq.QuoteIdentifier(dc.table)))
	}
	query := fmt.Sprintf(
		`SELECT MIN(lo)::date::text, MAX(hi)::date::text FROM (%s) u`,
		strings.Join(parts, " UNION ALL "))

	var lo, hi sql.NullString
	if err := db.QueryRow(query).Scan(&lo, &hi); err != nil {
		return ""
	}
	if !lo.Valid || !hi.Valid || lo.String == "" || hi.String == "" {
		return ""
	}
	return lo.String + " ~ " + hi.String
}
