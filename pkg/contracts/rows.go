package contracts

// LakehouseResult is the engine output returned by the lakehouse SQL execution layer.
// Mirrored from services/lakehouse-sql-server/lakehouse/types.go:LakehouseResult.
// The result rows are carried as a JSON string in ResultJSON (a []map[string]any
// marshaled by the executor) rather than a typed slice, because column types are
// dynamic (schema-per-project). Callers unmarshal ResultJSON themselves.
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
// Mirrored from services/lakehouse-sql-server/lakehouse/types.go:LakehouseDebugInfo.
type LakehouseDebugInfo struct {
	ResolvedProps      []PropertyInfo      `json:"resolvedProps,omitempty"`
	JoinPath           []JoinEdge          `json:"joinPath,omitempty"`
	KeywordCorrections []KeywordCorrection `json:"keywordCorrections,omitempty"`
	Warnings           []string            `json:"warnings,omitempty"`
}

// JoinEdge represents a single JOIN between two Ods via property columns.
// Mirrored from services/lakehouse-sql-server/lakehouse/types.go:JoinEdge.
type JoinEdge struct {
	FromOd      string `json:"fromOd"`
	FromProp    string `json:"fromProp"`
	ToOd        string `json:"toOd"`
	ToProp      string `json:"toProp"`
	Cardinality string `json:"cardinality"` // "1:N", "N:1", "1:1", "N:N"
}
