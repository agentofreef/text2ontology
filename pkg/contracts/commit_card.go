package contracts

// ParameterSpec mirrors one element of lakehouse_metric.parameters (JSONB array).
// Field shape is kept compatible with the existing IntentParameter persisted
// shape in querySpec.go — kept separate so CommitCardPayload depends on a
// small, explicit subset of fields the explore-mode LLM emits.
type ParameterSpec struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Required    bool        `json:"required,omitempty"`
	Description string      `json:"description,omitempty"`
	Default     interface{} `json:"default,omitempty"`
}

// MeasureSpec is the structured aggregate the explore LLM emits for an
// intent=aggregate card: a single function over a single column. The server
// derives the canonical_metric string ("SUM(amount)") from it — the LLM never
// writes the SQL expression itself. Agg ∈ {SUM,COUNT,AVG,MIN,MAX,COUNT_DISTINCT}.
// Column is empty only for COUNT (→ COUNT(*)).
type MeasureSpec struct {
	Agg    string `json:"agg"`
	Column string `json:"column,omitempty"`
}

// CommitFilter is one structured scoping filter {prop, op, value}. Resolved
// against the OD's properties server-side; cross-OD "OD.prop" prefixes are
// rejected (single-OD invariant).
type CommitFilter struct {
	Prop  string `json:"prop"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// CommitCardPayload is the SSE event body for `data: {"type":"commit_card", ...}`
// emitted by services/agent-server when explore-mode LLM converges on a metric.
// Frontend mirrors this shape in TS at
// frontend/src/components/lakehouse-agent/CommitCard.tsx.
//
// The LLM emits the STRUCTURED fields (Measure/Dimensions/Filters); the
// deterministic SmartQuery engine compiles the SQL. CanonicalMetric / QuerySql
// / AutoGroupBy are then DERIVED + populated server-side (canonical_metric from
// Measure, querySql from the engine's OntologySQL) so the accept-PUT, the
// metric editor, and TestRunPanel keep consuming the same wire shape.
type CommitCardPayload struct {
	ID               string          `json:"id"`               // pre-created mark=false row id
	DraftID          string          `json:"draftId"`          // unique per-emit; identifies the bubble
	Name             string          `json:"name"`             // editable; AC-11 fixture prefix "FIXTURE_LLM_DETERMINISTIC_<uuid>"
	DisplayName      string          `json:"displayName"`
	PrimaryOD        string          `json:"primaryOd"`
	// Intent classifies WHAT the user asked for, so the deterministic side can
	// pick the right SQL shape instead of inferring it from the SQL text:
	//   "aggregate" (default) — a measure: SUM/COUNT/AVG ... [GROUP BY dim].
	//                           querySql MUST pass ParseBareMetricSQL.
	//   "enumerate"           — a list of distinct dimension values
	//                           ("有哪些X / 列出 / 哪些种类"). querySql is a
	//                           SELECT DISTINCT <dim> FROM <OD> [WHERE ...];
	//                           NO aggregate, persisted as a level='sql' metric.
	// This is the explicit-intent field that removes the count-vs-enumerate
	// guesswork the LLM otherwise has to encode implicitly in the SQL shape.
	Intent           string          `json:"intent,omitempty"`
	// Structured spec the LLM emits (the contract). The server compiles these.
	Measure          *MeasureSpec    `json:"measure,omitempty"`    // intent=aggregate
	Dimensions       []string        `json:"dimensions,omitempty"` // group-by (aggregate) / DISTINCT cols (enumerate)
	Filters          []CommitFilter  `json:"filters,omitempty"`    // scoping filters
	// Derived server-side from the structured fields above (kept for the
	// frontend accept-PUT / editor / TestRunPanel wire compatibility).
	CanonicalMetric  string          `json:"canonicalMetric"`
	QuerySql         string          `json:"querySql"` // engine OntologySQL (aggregate) / built DISTINCT (enumerate)
	AutoGroupBy      []string        `json:"autoGroupBy,omitempty"`
	Parameters       []ParameterSpec `json:"parameters,omitempty"`
	TriggerKeywords  []string        `json:"triggerKeywords,omitempty"`
	ResponseTemplate string          `json:"responseTemplate,omitempty"`
	Description      string          `json:"description,omitempty"`
	Provenance       string          `json:"provenance,omitempty"`
}
