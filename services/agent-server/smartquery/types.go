package smartquery

import "github.com/lakehouse2ontology/contracts"

// The QuerySpec family of DTOs is owned by the shared pkg/contracts module
// (the single source of truth). The leaf types below are exposed under their
// historical smartquery.* names via type aliases so the hundreds of existing
// call sites compile unchanged while being structurally identical to the
// lakehouse-sql-server copy — they cannot drift because they ARE the same type.
//
// Two types stay concrete in this package because they carry package-local
// methods that Go does not permit on a cross-package alias:
//   - QuerySpec     (AppendGroupBy / HasGroupBy in engine.go)
//   - ResolveError  (Error in this file)
// Their structural consistency with contracts is enforced by the compile-time
// assertions at the bottom of this file.

// FilterItem is a raw filter as supplied by the LLM.
type FilterItem = contracts.FilterItem

// OrderByItem is a raw order-by as supplied by the LLM.
type OrderByItem = contracts.OrderByItem

// DerivedMetricDef defines an ad-hoc computed measure for the metric layer.
type DerivedMetricDef = contracts.DerivedMetricDef

// MetricFilter is a post-aggregation HAVING condition.
type MetricFilter = contracts.MetricFilter

// PropertyInfo is the bridge from ontology name to physical column.
type PropertyInfo = contracts.PropertyInfo

// ResolvedFilter is a FilterItem after the property has been bound to a real column
// and the value has optionally been corrected by the keyword hook.
type ResolvedFilter = contracts.ResolvedFilter

// ResolvedGroupBy carries the physical property, an optional date-format granularity
// string, and the OutputLabel used by the SELECT and ORDER BY clauses.
type ResolvedGroupBy = contracts.ResolvedGroupBy

// AggregateKind classifies how the SQL builder should emit a given aggregate.
// Lakehouse extends this enum with AggCustomSQL = 10 (lakehouse/types.go).
type AggregateKind = contracts.AggregateKind

const (
	AggStandard   = contracts.AggStandard   // emit: SUM/AVG/MAX/MIN/COUNT/DISTINCTCOUNT against a property
	AggCountRows  = contracts.AggCountRows   // emit: COUNT(*) over the source table
	AggDerivedRef = contracts.AggDerivedRef // emit: reference to a DEFINE-style derived measure
)

// ResolvedAggregate is a single aggregate column in the output.
type ResolvedAggregate = contracts.ResolvedAggregate

// OrderByKind distinguishes the three legal ORDER BY targets.
type OrderByKind = contracts.OrderByKind

const (
	OrderByProp        = contracts.OrderByProp        // ordinary column reference
	OrderByAggregate   = contracts.OrderByAggregate   // reference to a ResolvedAggregate by index
	OrderByCustomLabel = contracts.OrderByCustomLabel // arbitrary pre-computed label
)

// ResolvedOrderBy is a classified ORDER BY target.
type ResolvedOrderBy = contracts.ResolvedOrderBy

// KeywordCorrection records a filter value correction.
type KeywordCorrection = contracts.KeywordCorrection

// IntentHint mirrors the Metric-Intent metadata resolved here in agent-server
// (priority + keyword gate against lakehouse_metric_intent). Serialized over
// HTTP to lakehouse-sql-server, which applies it via PassApplyIntentHint.
type IntentHint = contracts.IntentHint

// IntentParameter mirrors one entry of lakehouse_metric_intent.parameters JSONB.
// See the contracts package for the full type semantics.
type IntentParameter = contracts.IntentParameter

// QuerySpec is the LLM-facing input (what v2ToolSmartQuery receives in args).
// All identifiers are string names — they have NOT yet been mapped to physical
// columns. Kept concrete (not a contracts alias) because AppendGroupBy /
// HasGroupBy are declared on it; the compile-time assertion below guarantees it
// stays structurally identical to contracts.QuerySpec.
type QuerySpec struct {
	ProjectID    string             `json:"projectId"`
	Objects      []string           `json:"objects"`
	Metric       string             `json:"metric"`
	GroupBy      []string           `json:"groupBy"`
	Filters      []FilterItem       `json:"filters"`
	OrderBy      []OrderByItem      `json:"orderBy"`
	Limit        int                `json:"limit"`
	Derived      []DerivedMetricDef `json:"derivedMetric,omitempty"`
	MetricFilter *MetricFilter      `json:"metricFilter,omitempty"`
	DisplayMode  string             `json:"displayMode"`
	// DenseGroups controls whether GROUP BY results include empty (zero) groups.
	// nil = default (true for lakehouse engine); false = disable (old SQL behavior,
	// only groups with existing rows appear).
	DenseGroups *bool `json:"denseGroups,omitempty"`

	// AddShareColumn appends a "share" column = primary_metric / SUM(primary_metric) OVER ()
	// to the query result. The denominator is computed across the un-LIMITed
	// rows but inside the WHERE clause — matching the universal "占比" rule
	// (filtered column / total of same filtered column without groupBy split).
	// Set when LLM detects "占比/比例/份额/share/distribution/percentage" in
	// user question without a Metric Intent already covering the share path.
	AddShareColumn bool `json:"addShareColumn,omitempty"`
	// ShareLabel names the share column (default "占比").
	ShareLabel string `json:"shareLabel,omitempty"`

	// IntentHint carries Metric-Intent metadata resolved here in
	// agent-server (priority + keyword gate against lakehouse_metric_intent).
	// Serialized over HTTP to lakehouse-sql-server, which applies it via its
	// PassApplyIntentHint pipeline pass. nil = no intent fired.
	IntentHint *IntentHint `json:"intentHint,omitempty"`
}

// ResolveError is a typed engine error so callers can distinguish "user fault"
// from "engine bug". Kept concrete (not a contracts alias) because Error() is
// declared on it; the compile-time assertion below guards structural drift.
type ResolveError struct {
	Code    string         `json:"code"` // "PROPERTY_NOT_FOUND" | "RELATIONSHIP_UNREACHABLE" | "OBJECT_NOT_FOUND" | "AMBIGUOUS_PROPERTY" | "MALFORMED_FILTER_VALUE" | "METRIC_AS_GROUPBY" | …
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

func (e *ResolveError) Error() string { return e.Message }

// Compile-time consistency assertions: these struct conversions only compile
// when the local concrete types stay field-for-field identical to the shared
// contracts source of truth, so the two service copies can never silently drift.
var (
	_ = func(s QuerySpec) contracts.QuerySpec { return contracts.QuerySpec(s) }
	_ = func(s contracts.QuerySpec) QuerySpec { return QuerySpec(s) }
	_ = func(e ResolveError) contracts.ResolveError { return contracts.ResolveError(e) }
)
