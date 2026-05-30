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

// MeasureSpec is the structured aggregate for intent=aggregate (server derives
// the canonical_metric string; the LLM never writes SQL). Agg ∈
// {SUM,COUNT,AVG,MIN,MAX,COUNT_DISTINCT}; Column empty only for COUNT.
type MeasureSpec struct {
	Agg    string `json:"agg"`
	Column string `json:"column,omitempty"`
}

// CommitFilter is one structured scoping filter {prop, op, value}.
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
// LLM emits the STRUCTURED fields (Measure/Dimensions/Filters); the engine
// compiles SQL. CanonicalMetric/QuerySql/AutoGroupBy are DERIVED server-side.
type CommitCardPayload struct {
	ID               string          `json:"id"`               // pre-created mark=false row id
	DraftID          string          `json:"draftId"`          // unique per-emit; identifies the bubble
	Name             string          `json:"name"`             // editable; AC-11 fixture prefix "FIXTURE_LLM_DETERMINISTIC_<uuid>"
	DisplayName      string          `json:"displayName"`
	PrimaryOD        string          `json:"primaryOd"`
	// Intent: "aggregate" (default) | "enumerate".
	Intent           string          `json:"intent,omitempty"`
	Measure          *MeasureSpec    `json:"measure,omitempty"`
	Dimensions       []string        `json:"dimensions,omitempty"`
	Filters          []CommitFilter  `json:"filters,omitempty"`
	CanonicalMetric  string          `json:"canonicalMetric"`  // derived
	QuerySql         string          `json:"querySql"`         // derived (engine OntologySQL / built DISTINCT)
	AutoGroupBy      []string        `json:"autoGroupBy,omitempty"`
	Parameters       []ParameterSpec `json:"parameters,omitempty"`
	TriggerKeywords  []string        `json:"triggerKeywords,omitempty"`
	ResponseTemplate string          `json:"responseTemplate,omitempty"`
	Description      string          `json:"description,omitempty"`
	Provenance       string          `json:"provenance,omitempty"`
}
