package lakehouse

import (
	"strings"

	"github.com/lakehouse2ontology/services/agent-server/smartquery"
)

// AggCustomSQL is a lakehouse-specific aggregate kind for ont_metric.sql_expression.
// Defined locally to avoid modifying smartquery. Value 10 avoids collision with
// smartquery iota (AggStandard=0, _ reserved=1, AggCountRows=2, AggDerivedRef=3).
// Reuses the Expr field on ResolvedAggregate to store the SQL expression string.
//
// Per ADR-003 (type-ownership inversion): the 12 LLM-facing types (QuerySpec,
// FilterItem, ResolvedGroupBy, ResolvedFilter, ResolvedAggregate, ResolvedOrderBy,
// PropertyInfo, KeywordCorrection, MetricFilter, DerivedMetricDef, OrderByItem,
// ResolveError) live canonically in smartquery. AggCustomSQL stays here because
// it is a downstream extension of smartquery.AggregateKind specific to the
// SQL-expression metric path and not part of the smartquery enum.
const AggCustomSQL smartquery.AggregateKind = 10

// LakehouseResult is the engine output.
type LakehouseResult struct {
	OntologySQL  string             `json:"ontologySQL"` // Layer 1: clean SQL using Od/Property names
	SQL          string             `json:"sql"`         // Layer 2: physical PostgreSQL with canonical_query
	ExecutionOK  bool               `json:"executionOk"`
	ResultJSON   string             `json:"resultJson"`
	ErrorMessage string             `json:"errorMessage,omitempty"`
	DurationMs   int64              `json:"durationMs"`
	DebugInfo    LakehouseDebugInfo `json:"debugInfo"`
}

// LakehouseDebugInfo carries observability data.
type LakehouseDebugInfo struct {
	ResolvedProps      []smartquery.PropertyInfo      `json:"resolvedProps,omitempty"`
	JoinPath           []JoinEdge                     `json:"joinPath,omitempty"`
	KeywordCorrections []smartquery.KeywordCorrection `json:"keywordCorrections,omitempty"`
	Warnings           []string                       `json:"warnings,omitempty"`
}

// JoinEdge represents a single JOIN between two Ods via property columns.
type JoinEdge struct {
	FromOd      string `json:"fromOd"`
	FromProp    string `json:"fromProp"`
	ToOd        string `json:"toOd"`
	ToProp      string `json:"toProp"`
	Cardinality string `json:"cardinality"` // "1:N", "N:1", "1:1", "N:N"
}

// ResolvedLakehouseQuery is the fully resolved query ready for the SQL builder.
type ResolvedLakehouseQuery struct {
	ProjectID          string
	Objects            []ObjectInfo              // resolved objects with canonical_query
	AllProps           []smartquery.PropertyInfo // all resolved properties
	GroupByCols        []smartquery.ResolvedGroupBy
	FilterItems        []smartquery.ResolvedFilter
	Aggregates         []smartquery.ResolvedAggregate
	OrderByCols        []smartquery.ResolvedOrderBy
	Derived            []smartquery.DerivedMetricDef
	MetricFilter       *smartquery.MetricFilter
	KeywordCorrections []smartquery.KeywordCorrection // collected during filter resolution for traceability
	Limit              int
	Warnings           []string
	DenseGroups        *bool // nil = default (dense on); *false = disable; *true = force-on
	// Universal "占比" — when true, dense_sql wraps the result with a share
	// column = first_metric / SUM(first_metric) OVER (). LIMIT/ORDER BY relocate
	// to the outer query so Top-N happens AFTER share is computed (avoids
	// LIMIT-then-renormalise bug).
	AddShareColumn bool
	ShareLabel     string
}

// HasObject returns true if the resolved query already contains the named Od.
func (rq *ResolvedLakehouseQuery) HasObject(name string) bool {
	for _, o := range rq.Objects {
		if strings.EqualFold(o.Name, name) {
			return true
		}
	}
	return false
}

// ObjectInfo holds a resolved Od with its canonical query.
type ObjectInfo struct {
	ID             string
	Name           string
	CanonicalQuery string
	Props          []smartquery.PropertyInfo

	// ActualColumns is the real output column list of the original
	// canonical_query, obtained via probeCanonicalColumns at load time.
	// Populated even when canonical_query is later rewritten to align with
	// property names — it describes the raw upstream shape for diagnostics.
	ActualColumns []string

	// UnmatchedProps lists property names that have no case/whitespace/
	// underscore-insensitive match in ActualColumns. Loading these properties
	// is tolerated (they might never be referenced) but any filter / groupBy
	// / aggregate / orderBy that actually uses one becomes a ResolveError
	// with a message listing ActualColumns to help the user reconcile.
	UnmatchedProps []string
}
