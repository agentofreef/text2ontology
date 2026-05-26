package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/services/agent-server/smartquery"
	"github.com/lakehouse2ontology/sqlrewrite"
)

// metricPreviewFilter is one structured filter in the preview request body,
// matching the editor's canonicalFilters shape {prop, op, value}.
type metricPreviewFilter struct {
	Prop  string `json:"prop"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// metricPreviewOrderBy mirrors one smartquery `orderBy` entry {label, dir}.
type metricPreviewOrderBy struct {
	Label string `json:"label"`
	Dir   string `json:"dir"`
}

// metricPreviewRequest is the body of POST /api/ontology/lakehouse-metric-preview.
// It drives the editor's "Run" button: render the metric exactly as the agent
// runtime would, using sample params. level=='sql' executes the human-authored
// SQL via the same execute-sql path the agent uses; otherwise it composes a
// structured QuerySpec and runs the engine.
type metricPreviewRequest struct {
	ProjectID       string                `json:"projectId"`
	Level           string                `json:"level"`
	ODName          string                `json:"odName"`
	QuerySQL        string                `json:"querySql"`
	CanonicalMetric string                `json:"canonicalMetric"`
	CanonicalFilters []metricPreviewFilter `json:"canonicalFilters"`
	AutoGroupBy     []string              `json:"autoGroupBy"`
	DefaultLimit    int                   `json:"defaultLimit"`
	Parameters      []smartquery.IntentParameter `json:"parameters"`
	// SampleParams values are scalar (string) OR list (string[]); a list binds to
	// a `= ANY({sys.x.NAME})` param as a Postgres array.
	SampleParams    map[string]interface{} `json:"sampleParams"`
	// DryRun (level=sql only): render + RejectDDL and return the pruned SQL
	// WITHOUT executing — powers the editor's live "rendered SQL" preview.
	DryRun          bool                  `json:"dryRun"`
	// OrderBy mirrors the smartquery function-call `orderBy` param so the
	// standalone simulator can exercise it. Each item targets a result column
	// label with a direction.
	OrderBy         []metricPreviewOrderBy `json:"orderBy"`
	// GroupBy is the simulator's explicit dimension-only columns (the function
	// call's `groupBy` — bare or "OD.Column"). Appended to spec.GroupBy on top
	// of the metric's autoGroupBy (deduped). A cross-OD ref triggers the JOIN.
	GroupBy         []string              `json:"groupBy"`
}

// HandleLakehouseMetricPreview is the editor-only preview endpoint. It mirrors
// the runtime dispatch so the editor's "Run" button shows exactly what the
// agent would produce, without persisting anything.
//
//   level=='sql': sampleParams → ExecuteSQLMetric(projectId, querySql, params).
//                 The values bind via $N driver args service-side (never
//                 concatenated); RejectDDL runs after placeholder substitution.
//   else:         build a structured QuerySpec from {odName, canonicalMetric,
//                 canonicalFilters + sampleParams (folded as = filters),
//                 autoGroupBy, defaultLimit} → Execute(spec).
//
// Auth/scoping matches the other agent public handlers: user-bearer (applied by
// authmw.Wrap on the mux) + per-project access enforcement on the body's
// projectId (which bypasses the middleware's ?projectId= query gate).
func HandleLakehouseMetricPreview(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			JsonResp(w, M{"ok": false, "error": "POST only"})
			return
		}

		var req metricPreviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"ok": false, "error": "invalid JSON body"})
			return
		}
		req.ProjectID = strings.TrimSpace(req.ProjectID)
		if req.ProjectID == "" {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"ok": false, "error": "projectId required"})
			return
		}
		// Body projectId bypasses the middleware's ?projectId= query gate; verify
		// per-project access explicitly before touching project data.
		if !authmw.EnforceProjectAccess(w, r, db, req.ProjectID) {
			return
		}

		ctx := r.Context()

		if req.Level == "sql" {
			if strings.TrimSpace(req.QuerySQL) == "" {
				JsonResp(w, M{"ok": false, "error": "querySql required for level=sql"})
				return
			}
			params := req.SampleParams
			if params == nil {
				params = map[string]interface{}{}
			}
			// DryRun: render + RejectDDL only, return the pruned OD-name SQL the
			// editor displays as "实际执行的 SQL（裁剪后）". No execution, no CTE wrap
			// (the OD-name form is the readable one). RenderSysParams is a pure
			// pkg/* function so we can call it here without a service round-trip.
			if req.DryRun {
				rendered, _, missingRequired := sqlrewrite.RenderSysParams(req.QuerySQL, params)
				ddlErr := ""
				if err := sqlrewrite.RejectDDL(rendered); err != nil {
					ddlErr = err.Error()
				}
				JsonResp(w, M{
					"ok":              ddlErr == "" && len(missingRequired) == 0,
					"sql":             rendered,
					"dryRun":          true,
					"missingRequired": missingRequired,
					"error":           ddlErr,
				})
				return
			}
			res := smartqueryExec(db).ExecuteSQLMetric(ctx, req.ProjectID, req.QuerySQL, params)
			rows := []interface{}{}
			if res.ResultJSON != "" {
				_ = json.Unmarshal([]byte(res.ResultJSON), &rows)
			}
			JsonResp(w, M{
				"ok":       res.ExecutionOK,
				"sql":      res.SQL,
				"columns":  []string{}, // SQL path returns rows-as-maps; column order is the SELECT list
				"rows":     rows,
				"rowCount": len(rows),
				"error":    res.ErrorMessage,
			})
			return
		}

		// Live-edit support: when the editor sends a fresh querySql, parse it
		// SERVER-SIDE and refresh canonicalMetric + autoGroupBy from the typed
		// SQL. This lets the simulator reflect what the author is editing
		// without forcing a save first. Parse failures fall silently back to
		// the body fields — mid-edit drafts still preview cleanly.
		if req.Level != "sql" && strings.TrimSpace(req.QuerySQL) != "" {
			if _, measure, baseDims, err := sqlrewrite.ParseBareMetricSQL(req.QuerySQL); err == nil {
				req.CanonicalMetric = measure
				req.AutoGroupBy = baseDims
			}
		}

		// Structured (simple/plan-less) preview: compose a QuerySpec the same way
		// the runtime would for a curated metric, folding sampleParams in as
		// equality filters so the editor can exercise parameterized filters.
		spec := smartquery.QuerySpec{
			ProjectID:   req.ProjectID,
			Objects:     []string{req.ODName},
			Metric:      req.CanonicalMetric,
			GroupBy:     req.AutoGroupBy,
			Limit:       req.DefaultLimit,
			DisplayMode: "table",
		}
		for _, f := range req.CanonicalFilters {
			op := f.Op
			if op == "" {
				op = "="
			}
			spec.Filters = append(spec.Filters, smartquery.FilterItem{Prop: f.Prop, Op: op, Value: f.Value})
		}
		// gbHas reports whether a column (bare key, case-insensitive) is already a
		// GROUP BY dimension — so a dimension-only add doesn't duplicate one the
		// metric's autoGroupBy already lists (a dup groupBy → engine 0-rows).
		gbHas := func(name string) bool {
			bare := strings.ToLower(name)
			if i := strings.LastIndex(bare, "."); i >= 0 {
				bare = bare[i+1:]
			}
			for _, g := range spec.GroupBy {
				gb := strings.ToLower(g)
				if i := strings.LastIndex(gb, "."); i >= 0 {
					gb = gb[i+1:]
				}
				if gb == bare {
					return true
				}
			}
			return false
		}
		for name, val := range req.SampleParams {
			if val == nil {
				continue
			}
			// Structured mode folds sample params as equality filters; a scalar is
			// used verbatim, a list is comma-joined (structured filters take a
			// string value — lists are a SQL-mode `= ANY()` concept).
			var sv string
			switch t := val.(type) {
			case []interface{}:
				parts := make([]string, 0, len(t))
				for _, e := range t {
					if e != nil {
						parts = append(parts, fmt.Sprint(e))
					}
				}
				sv = strings.Join(parts, ",")
			case []string:
				sv = strings.Join(t, ",")
			default:
				sv = fmt.Sprint(val)
			}
			if strings.TrimSpace(sv) == "" {
				// "存在但无值" — the column is named with NO filter value. This is
				// the dimension-only case ("某 GEO 有哪些"): break down by the
				// column (add to GROUP BY) without a WHERE predicate. A cross-OD
				// "OD.Column" ref still triggers ensureObjectsCoverReferencedProps
				// → the engine joins that OD.
				if !gbHas(name) {
					spec.GroupBy = append(spec.GroupBy, name)
				}
				continue
			}
			spec.Filters = append(spec.Filters, smartquery.FilterItem{Prop: name, Op: "=", Value: sv})
		}

		// Explicit groupBy (standalone simulator): dimension-only columns the
		// caller chose. Appended on top of autoGroupBy, deduped by bare key.
		for _, g := range req.GroupBy {
			g = strings.TrimSpace(g)
			if g != "" && !gbHas(g) {
				spec.GroupBy = append(spec.GroupBy, g)
			}
		}

		// orderBy (standalone simulator): each {label, dir} → spec.OrderBy. The
		// label targets a result column; invalid dir defaults to DESC.
		for _, ob := range req.OrderBy {
			label := strings.TrimSpace(ob.Label)
			if label == "" {
				continue
			}
			dir := strings.ToUpper(strings.TrimSpace(ob.Dir))
			if dir != "ASC" && dir != "DESC" {
				dir = "DESC"
			}
			spec.OrderBy = append(spec.OrderBy, smartquery.OrderByItem{Prop: label, Dir: dir})
		}

		// Match the agent runtime's assembly EXACTLY so the editor's "Run" shows
		// what a real query would produce:
		//   · promoteFilterPropsToGroupBy — "过滤即展示": every eq/IN filter column
		//     also becomes a GROUP BY dimension (the metric's three-state).
		//   · ensureObjectsCoverReferencedProps — when a filter/dim column lives on
		//     another OD (e.g. "PRODUCT.PRODUCT_BRAND"), auto-add that OD to
		//     spec.Objects so the engine's ResolveJoinPath joins it (cross-OD).
		// Both are the same package-level helpers lakehouseToolSmartQuery uses.
		promoteFilterPropsToGroupBy(&spec)
		_ = ensureObjectsCoverReferencedProps(db, req.ProjectID, &spec)

		result := smartqueryExec(db).Execute(ctx, spec)
		rows := []interface{}{}
		if result.ResultJSON != "" {
			_ = json.Unmarshal([]byte(result.ResultJSON), &rows)
		}
		JsonResp(w, M{
			"ok":       result.ExecutionOK,
			"sql":      result.OntologySQL,
			"columns":  []string{},
			"rows":     rows,
			"rowCount": len(rows),
			"error":    result.ErrorMessage,
		})
	}
}

// distinctColRe validates the column for the distinct-values endpoint — a bare
// identifier (same shape as a {sys.req/opt.NAME} param name), so it can be safely
// interpolated as a quoted identifier with no injection surface.
var distinctColRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type metricDistinctRequest struct {
	ProjectID string `json:"projectId"`
	ODName    string `json:"odName"`
	Column    string `json:"column"`
	Limit     int    `json:"limit"`
}

// HandleLakehouseMetricDistinct powers the editor's SAMPLE VALUE suggestions: for
// a param whose NAME is a column of one of the metric's Ods, it returns the
// DISTINCT values of that column so the author can pick a real value. It runs
// `select distinct "col" from "od" …` through the SAME read-only OD-CTE path as
// execute-sql (od validated against known Ods, RejectDDL, read-only tx). The
// column is a validated bare identifier and the Od name is validated by the
// execute-sql layer (unknown Od → error) — no injection surface.
func HandleLakehouseMetricDistinct(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			JsonResp(w, M{"ok": false, "error": "POST only"})
			return
		}
		var req metricDistinctRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"ok": false, "error": "invalid JSON body"})
			return
		}
		req.ProjectID = strings.TrimSpace(req.ProjectID)
		req.ODName = strings.TrimSpace(req.ODName)
		req.Column = strings.TrimSpace(req.Column)
		if req.ProjectID == "" || req.ODName == "" || req.Column == "" {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"ok": false, "error": "projectId, odName and column required"})
			return
		}
		if !distinctColRe.MatchString(req.Column) {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"ok": false, "error": "invalid column"})
			return
		}
		if !authmw.EnforceProjectAccess(w, r, db, req.ProjectID) {
			return
		}
		limit := req.Limit
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		col := req.Column
		sqlText := fmt.Sprintf(
			`select distinct "%s" as v from "%s" where "%s" is not null and "%s"::text <> '' order by 1 limit %d`,
			col, req.ODName, col, col, limit)
		res := smartqueryExec(db).ExecuteSQLMetric(r.Context(), req.ProjectID, sqlText, nil)
		values := []string{}
		if res.ResultJSON != "" {
			var rowsMaps []map[string]interface{}
			if json.Unmarshal([]byte(res.ResultJSON), &rowsMaps) == nil {
				for _, row := range rowsMaps {
					if v, ok := row["v"]; ok && v != nil {
						values = append(values, fmt.Sprint(v))
					}
				}
			}
		}
		JsonResp(w, M{"ok": res.ExecutionOK, "values": values, "error": res.ErrorMessage})
	}
}
