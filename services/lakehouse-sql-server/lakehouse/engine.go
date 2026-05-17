package lakehouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lakehouse2ontology/observability"
	"github.com/lakehouse2ontology/services/lakehouse-sql-server/smartquery"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Engine is the public facade for lakehouse SQL query execution.
//
// All query generation flows through the goqu-based v2 builder in
// sql_builder_v2.go (single/multi-Od, derived metrics) and dense_sql.go
// (dense GROUP BY mode). The legacy hand-rolled builder was retired in
// P1 because it could not quote MixedCase identifiers correctly.
type Engine struct {
	DB *sql.DB
}

// Execute runs Resolve → JoinResolve → PreloadIntermediateOds → BuildSQLV2 → ExecuteSQL.
func (e *Engine) Execute(ctx context.Context, spec smartquery.QuerySpec) LakehouseResult {
	// CR-10: generate_sql and execute_sql spans must be SIBLINGS (not nested),
	// so genSpan has a single explicit End() at the success point after SQL
	// build completes. Early-return error paths close the span inline. No
	// `defer genSpan.End()` here — duplicating End() would misreport span
	// duration to the execute_sql boundary.
	_, genSpan := observability.Tracer().Start(ctx, "smartquery.generate_sql",
		trace.WithAttributes(
			attribute.Bool("has_groupby", len(spec.GroupBy) > 0),
			attribute.Bool("has_filters", len(spec.Filters) > 0),
			attribute.Bool("has_derived", len(spec.Derived) > 0),
			attribute.Int("object_count", len(spec.Objects)),
		))

	corrector := &LakehouseCorrector{DB: e.DB}

	// Stage 0: Spec-level pipeline — Intent enforcement + bare-spec gate.
	// Runs before resolution because it only inspects spec strings, and
	// resolution would otherwise turn an LLM-omitted metric into a hard
	// "property not found" error before the gate could surface a clearer
	// message.
	if err := smartquery.DefaultPipeline.Run(&spec); err != nil {
		genSpan.End()
		return LakehouseResult{
			ErrorMessage: err.Error(),
			DebugInfo:    LakehouseDebugInfo{Warnings: []string{err.Error()}},
		}
	}

	// Stage 1+2: Resolve properties, metrics, filters (existence + syntax validation).
	rq, err := ResolveQuery(e.DB, spec, corrector)
	if err != nil {
		genSpan.End()
		return LakehouseResult{
			ErrorMessage: err.Error(),
			DebugInfo:    LakehouseDebugInfo{Warnings: []string{err.Error()}},
		}
	}

	// Stage 3: Resolve JOIN path (if multi-Od) — semantic validation.
	var joinPath []JoinEdge
	if len(rq.Objects) > 1 {
		var names []string
		for _, o := range rq.Objects {
			names = append(names, o.Name)
		}
		joinPath, err = ResolveJoinPath(e.DB, spec.ProjectID, names)
		if err != nil {
			genSpan.End()
			return LakehouseResult{
				ErrorMessage: err.Error(),
				DebugInfo:    LakehouseDebugInfo{ResolvedProps: rq.AllProps, KeywordCorrections: rq.KeywordCorrections, Warnings: append(rq.Warnings, err.Error())},
			}
		}

		// Bug #6 fix: pre-load intermediate Ods from join path that aren't in the query.
		for _, edge := range joinPath {
			for _, odName := range []string{edge.FromOd, edge.ToOd} {
				if !rq.HasObject(odName) {
					obj, loadErr := LoadSingleObject(e.DB, spec.ProjectID, odName)
					if loadErr != nil || obj.CanonicalQuery == "" {
						msg := fmt.Sprintf("JOIN 路径中的中间对象 %q 未配置 canonical_query。请先在 Lakehouse Objects 页面完成 SQL 映射。", odName)
						genSpan.End()
						return LakehouseResult{
							ErrorMessage: msg,
							DebugInfo:    LakehouseDebugInfo{ResolvedProps: rq.AllProps, JoinPath: joinPath, KeywordCorrections: rq.KeywordCorrections, Warnings: append(rq.Warnings, msg)},
						}
					}
					rq.Objects = append(rq.Objects, obj)
					rq.AllProps = append(rq.AllProps, obj.Props...)
				}
			}
		}
	}

	// Build Ontology SQL (Layer 1 — human-readable, uses Od/Property names only).
	// P4: this now goes through the same goqu builder as Physical, only the
	// FROM strategy differs. Errors here are non-fatal — the preview is
	// best-effort, the user still gets the executable layer below.
	ontologySQL, _ := BuildOntologySQLV2(rq, joinPath)

	// Build Physical SQL (Layer 2 — actual PostgreSQL with canonical_query).
	var sqlStr string
	if len(rq.Derived) > 0 {
		sqlStr, err = BuildWithDerivedV2(rq, joinPath)
	} else {
		sqlStr, err = BuildSQLV2(rq, joinPath)
	}
	if err != nil {
		genSpan.End()
		return LakehouseResult{
			ErrorMessage: err.Error(),
			DebugInfo:    LakehouseDebugInfo{ResolvedProps: rq.AllProps, JoinPath: joinPath, KeywordCorrections: rq.KeywordCorrections, Warnings: rq.Warnings},
		}
	}
	if sqlStr == "" {
		genSpan.End()
		return LakehouseResult{
			ErrorMessage: fmt.Sprintf("SQL 构建失败: objects=%v metric=%q", spec.Objects, spec.Metric),
			DebugInfo:    LakehouseDebugInfo{ResolvedProps: rq.AllProps, JoinPath: joinPath, KeywordCorrections: rq.KeywordCorrections, Warnings: rq.Warnings},
		}
	}

	// Generation complete — close the generate span before entering execute.
	genSpan.End()

	// smartquery.execute_sql — Postgres round-trip. Complexity bucket feeds
	// the Prometheus histogram label so dashboards can separate fast single-
	// Od queries from multi-Od join queries.
	_, execSpan := observability.Tracer().Start(ctx, "smartquery.execute_sql",
		trace.WithAttributes(
			attribute.Int("object_count", len(rq.Objects)),
			attribute.Int("join_path_len", len(joinPath)),
		))
	execStart := time.Now()
	ok, resultJSON, errMsg, durationMs := ExecuteSQL(e.DB, spec.ProjectID, sqlStr)
	observability.SmartqueryExecDuration.
		WithLabelValues(observability.ComplexityBucket(len(rq.Objects), len(spec.Filters) > 0)).
		Observe(float64(time.Since(execStart).Milliseconds()))
	execSpan.End()

	return LakehouseResult{
		OntologySQL:  ontologySQL,
		SQL:          sqlStr,
		ExecutionOK:  ok,
		ResultJSON:   resultJSON,
		ErrorMessage: errMsg,
		DurationMs:   durationMs,
		DebugInfo: LakehouseDebugInfo{
			ResolvedProps:      rq.AllProps,
			JoinPath:           joinPath,
			KeywordCorrections: rq.KeywordCorrections, // Bug #8 fix: populated during filter resolution
			Warnings:           rq.Warnings,
		},
	}
}

// GenerateSQL runs Resolve → JoinResolve → BuildSQLV2 without executing.
// Useful for preview/debug. Also handles derived metrics.
func (e *Engine) GenerateSQL(ctx context.Context, spec smartquery.QuerySpec) (string, LakehouseDebugInfo, error) {
	_, span := observability.Tracer().Start(ctx, "smartquery.generate_sql",
		trace.WithAttributes(
			attribute.Bool("has_groupby", len(spec.GroupBy) > 0),
			attribute.Bool("has_filters", len(spec.Filters) > 0),
			attribute.Bool("has_derived", len(spec.Derived) > 0),
			attribute.Int("object_count", len(spec.Objects)),
		))
	defer span.End()

	if err := smartquery.DefaultPipeline.Run(&spec); err != nil {
		return "", LakehouseDebugInfo{}, err
	}

	corrector := &LakehouseCorrector{DB: e.DB}

	rq, err := ResolveQuery(e.DB, spec, corrector)
	if err != nil {
		return "", LakehouseDebugInfo{}, err
	}

	var joinPath []JoinEdge
	if len(rq.Objects) > 1 {
		var names []string
		for _, o := range rq.Objects {
			names = append(names, o.Name)
		}
		joinPath, err = ResolveJoinPath(e.DB, spec.ProjectID, names)
		if err != nil {
			return "", LakehouseDebugInfo{ResolvedProps: rq.AllProps, KeywordCorrections: rq.KeywordCorrections}, err
		}

		// Pre-load intermediate Ods.
		for _, edge := range joinPath {
			for _, odName := range []string{edge.FromOd, edge.ToOd} {
				if !rq.HasObject(odName) {
					obj, loadErr := LoadSingleObject(e.DB, spec.ProjectID, odName)
					if loadErr != nil {
						return "", LakehouseDebugInfo{ResolvedProps: rq.AllProps, JoinPath: joinPath, KeywordCorrections: rq.KeywordCorrections}, loadErr
					}
					rq.Objects = append(rq.Objects, obj)
					rq.AllProps = append(rq.AllProps, obj.Props...)
				}
			}
		}
	}

	// Route through BuildWithDerivedV2 when derived metrics are present,
	// otherwise the plain v2 builder.
	var sqlStr string
	if len(rq.Derived) > 0 {
		sqlStr, err = BuildWithDerivedV2(rq, joinPath)
	} else {
		sqlStr, err = BuildSQLV2(rq, joinPath)
	}
	if err != nil {
		return "", LakehouseDebugInfo{ResolvedProps: rq.AllProps, JoinPath: joinPath, KeywordCorrections: rq.KeywordCorrections}, err
	}

	return sqlStr, LakehouseDebugInfo{ResolvedProps: rq.AllProps, JoinPath: joinPath, KeywordCorrections: rq.KeywordCorrections, Warnings: rq.Warnings}, nil
}

// NormalizeQuerySpec re-exports smartquery.NormalizeQuerySpec for convenience.
func NormalizeQuerySpec(raw map[string]interface{}) smartquery.QuerySpec {
	return smartquery.NormalizeQuerySpec(raw)
}

// ── Helpers ──

// isValidFilterOp validates filter operators at the engine level.
func isValidFilterOp(op string) bool {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "=", "<>", "!=", ">", ">=", "<", "<=",
		"contains", "not contains", "starts with", "ends with",
		"like", "not like",
		"is blank", "is not blank", "between", "in", "not in":
		return true
	}
	return false
}
