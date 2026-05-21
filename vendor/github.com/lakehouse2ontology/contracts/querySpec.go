// Package contracts contains shared DTO types mirrored from services/agent-server + lakehouse-sql-server + recall-server
// for future use across services. All types are frozen at Phase 0 of the
// arch-split plan; only additive changes (new fields) are permitted until
// Phase 5 semver graduation.
package contracts

// QuerySpec is the LLM-facing input (what v2ToolSmartQuery receives in args).
// All identifiers are string names — they have NOT yet been mapped to physical columns.
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

	// IntentHint carries Metric-Intent metadata resolved upstream by the agent
	// (priority + keyword gate against lakehouse_metric_intent). It is
	// serialized over HTTP to lakehouse-sql-server, which applies it via its
	// PassApplyIntentHint pipeline pass. nil = no intent fired.
	IntentHint *IntentHint `json:"intentHint,omitempty"`
}

// IntentHint carries the spec-level fields of a Metric Intent the agent has
// selected for the current question. It is purely additive input — the
// lakehouse-sql-server consumes it via applyIntentHint to mutate the spec
// deterministically, without querying lakehouse_metric_intent itself. The JSON
// field names are stable so the agent-server and lakehouse-sql-server marshal
// the identical shape over the wire.
type IntentHint struct {
	IntentID            string       `json:"intentId,omitempty"`
	Name                string       `json:"name,omitempty"`
	CanonicalMetric     string       `json:"canonicalMetric,omitempty"`
	CanonicalFilters    []FilterItem `json:"canonicalFilters,omitempty"`
	AutoGroupBy         []string     `json:"autoGroupBy,omitempty"`
	ReplaceGroupBy      bool         `json:"replaceGroupBy,omitempty"`
	AddShareColumnSafe  bool         `json:"addShareColumnSafe,omitempty"`
	DefaultOrderByLabel string       `json:"defaultOrderByLabel,omitempty"`
	DefaultOrderByDir   string       `json:"defaultOrderByDir,omitempty"`
	DefaultLimit        int          `json:"defaultLimit,omitempty"`
}

// IntentParameter mirrors one entry of lakehouse_metric_intent.parameters JSONB.
// It declares a typed, user-level knob the LLM fills when calling smartquery in
// strict-mode dispatch — the contract the binder (BindIntentParams) consumes.
//
// Type semantics (v1):
//
//	"int":             numeric value; binder writes to spec.Limit
//	"string":          if Property set, treats as filter value on Property using Op
//	                   (default "="); reserved for future custom routing otherwise
//	"property_filter": LLM provides a value; binder appends spec.Filters with
//	                   {Prop=Property, Op=Op (default "="), Value, FuzzyMatch}
//
// Default applies when the LLM omits the param. Required (Optional=false) +
// nil Default → PARAM_REQUIRED at bind time.
type IntentParameter struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Property    string      `json:"property,omitempty"`
	Op          string      `json:"op,omitempty"`
	Default     interface{} `json:"default,omitempty"`
	Optional    bool        `json:"optional,omitempty"`
	Description string      `json:"description,omitempty"`
	FuzzyMatch  bool        `json:"fuzzyMatch,omitempty"`

	// AllowedValues is a runtime-only view (not persisted in JSON). For
	// Type=="enum_ref" the caller (handler) populates this slice from the
	// project's lakehouse_keyword table. json:"-" so it doesn't accidentally
	// serialize back into Intent records.
	AllowedValues []string `json:"-"`
}

// FilterItem is a raw filter as supplied by the LLM.
type FilterItem struct {
	Prop       string `json:"prop"`
	Op         string `json:"op"`
	Value      string `json:"value"`
	FuzzyMatch bool   `json:"fuzzyMatch,omitempty"`
}

// OrderByItem is a raw order-by as supplied by the LLM.
type OrderByItem struct {
	Prop string `json:"prop"`
	Dir  string `json:"dir"` // "ASC" | "DESC"
}

// DerivedMetricDef defines an ad-hoc computed measure for the metric layer.
type DerivedMetricDef struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	BaseTable  string `json:"baseTable"`
}

// MetricFilter is a post-aggregation HAVING condition.
type MetricFilter struct {
	Op    string `json:"op"`
	Value string `json:"value"`
}

// PropertyInfo is the bridge from ontology name to physical column.
type PropertyInfo struct {
	Name       string `json:"name"`
	DataType   string `json:"dataType"`
	TableName  string `json:"tableName"`
	ColumnName string `json:"columnName"`
	ObjectName string `json:"objectName"`
	ObjectID   string `json:"objectId"`
}

// ResolvedFilter is a FilterItem after the property has been bound to a real column
// and the value has optionally been corrected by the keyword hook.
type ResolvedFilter struct {
	Prop          PropertyInfo `json:"prop"`
	Op            string       `json:"op"`
	Value         string       `json:"value"`
	OriginalValue string       `json:"originalValue"`
	FuzzyMatch    bool         `json:"fuzzyMatch,omitempty"`
}

// ResolvedGroupBy carries the physical property, an optional date-format granularity
// string, and the OutputLabel used by the SELECT and ORDER BY clauses.
type ResolvedGroupBy struct {
	Prop          PropertyInfo `json:"prop"`
	Granularity   string       `json:"granularity"`   // "" | "YYYY-MM" | "YYYY" | "YYYY-MM-DD" | "YYYY-Q" | "YYYY-WW" | "YYYY-MM-DD HH"
	OutputLabel   string       `json:"outputLabel"`   // stable alias used for FORMAT() + ORDER BY
	OriginalToken string       `json:"originalToken"` // e.g. "Order_Receiving_Date(月)"
}

// AggregateKind classifies how the SQL builder should emit a given aggregate.
// Lakehouse extends this enum with AggCustomSQL = 10 (lakehouse/types.go).
type AggregateKind int

const (
	AggStandard   AggregateKind = iota // emit: SUM/AVG/MAX/MIN/COUNT/DISTINCTCOUNT against a property
	_                                  // reserved (was AggCustomDAX, removed when DAX path was deleted)
	AggCountRows                       // emit: COUNT(*) over the source table
	AggDerivedRef                      // emit: reference to a DEFINE-style derived measure
)

// ResolvedAggregate is a single aggregate column in the output.
type ResolvedAggregate struct {
	Kind  AggregateKind `json:"kind"`
	Prop  *PropertyInfo `json:"prop,omitempty"`
	Func  string        `json:"func"`  // "SUM" | "AVG" | "MAX" | "MIN" | "COUNT" | "DISTINCTCOUNT"
	Label string        `json:"label"` // human-visible output column name
	Expr  string        `json:"expr"`  // expression string: SQL for AggCustomSQL (lakehouse), measure ref for AggDerivedRef
	Table string        `json:"table"` // for AggCountRows: the source table to count
}

// OrderByKind distinguishes the three legal ORDER BY targets.
type OrderByKind int

const (
	OrderByProp        OrderByKind = iota // ordinary column reference
	OrderByAggregate                      // reference to a ResolvedAggregate by index
	OrderByCustomLabel                    // arbitrary pre-computed label
)

// ResolvedOrderBy is a classified ORDER BY target.
type ResolvedOrderBy struct {
	Kind     OrderByKind   `json:"kind"`
	Prop     *PropertyInfo `json:"prop,omitempty"`
	AggIndex int           `json:"aggIndex,omitempty"`
	Label    string        `json:"label,omitempty"`
	Dir      string        `json:"dir"` // "ASC" | "DESC"
}

// KeywordCorrection records a filter value correction.
type KeywordCorrection struct {
	Prop      string `json:"prop"`
	UserValue string `json:"userValue"`
	DBValue   string `json:"dbValue"`
	Status    string `json:"status"` // "matched" | "machineCode_fuzzy" | "no-match" | "noop"
}

// ResolveError is a typed engine error so callers can distinguish "user fault" from "engine bug".
type ResolveError struct {
	Code    string         `json:"code"` // "PROPERTY_NOT_FOUND" | "RELATIONSHIP_UNREACHABLE" | "OBJECT_NOT_FOUND" | "AMBIGUOUS_PROPERTY" | "MALFORMED_FILTER_VALUE" | "METRIC_AS_GROUPBY" | …
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

func (e *ResolveError) Error() string { return e.Message }
