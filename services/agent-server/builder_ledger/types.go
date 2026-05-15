// Package builder_ledger provides per-thread memory for Builder Agent.
//
// While Query mode's ledger tracks Od/Intent/Token recall, Builder mode tracks
// the AI's exploration of the lakehouse + the drafts it's proposed during the
// session. The cross-turn benefit: re-asking analyze_table for an already-explored
// table returns a ledger summary instead of re-running the SQL probe, and the
// LLM's context only carries compact summaries of historical tool calls
// instead of full multi-KB JSON results.
//
// The ledger lives in ont_agent_thread.thread_state->'builder_ledger' (a
// sibling key to the query mode's 'ledger' key — no schema change needed).
package builder_ledger

import "time"

// SchemaVersion is bumped when the JSON shape changes incompatibly.
// On Load, a mismatch triggers a reset to New() (no migration — cheaper
// than maintaining migration code for a single-user session store).
const SchemaVersion = 1

// BuilderLedger is the top-level accumulator for a builder agent thread.
// All collection fields use string keys for idempotent merge semantics —
// re-merging the same tool result is always a no-op on the keyed fields.
type BuilderLedger struct {
	Version       int       `json:"version"`       // optimistic concurrency counter
	SchemaVersion int       `json:"schemaVersion"`
	TurnCount     int       `json:"turnCount"`
	UpdatedAt     time.Time `json:"updatedAt"`

	// What the AI has discovered about the lakehouse.
	TablesExplored        map[string]*TableExplored        `json:"tablesExplored"`
	RelationshipsAnalyzed map[string]*RelationshipAnalyzed `json:"relationshipsAnalyzed"`
	LakehouseTables       *TablesIndex                     `json:"lakehouseTables,omitempty"`
	SearchKeywords        map[string]*SearchKeyword        `json:"searchKeywords"`

	// What drafts have been proposed in this thread (and their status).
	DraftsProposed map[string]*DraftProposed `json:"draftsProposed"`

	// Snapshot of project's existing ontology (refreshed on list_ods/intents/links).
	OntologySnapshot *OntologySnapshot `json:"ontologySnapshot,omitempty"`
}

// TableExplored holds the ledger summary for one analyzed table.
// Extracted from builderToolAnalyzeTable result JSON.
type TableExplored struct {
	Table              string       `json:"table"`
	RowCount           int64        `json:"rowCount"`
	ColumnCount        int          `json:"columnCount"`
	TruncatedColumns   bool         `json:"truncatedColumns,omitempty"`
	Hypotheses         []string     `json:"hypotheses"`
	KeyColumns         []KeyColumn  `json:"keyColumns"`         // PK/FK/MC/TS candidates
	LowCardinalityCols []ColumnEnum `json:"lowCardinalityCols"` // value distribution for ≤30-card cols
	ExploredInTurn     int          `json:"exploredInTurn"`
}

// KeyColumn captures one column that heuristics flagged as structurally
// significant (primary key, foreign key, machine-code enum, or timestamp).
type KeyColumn struct {
	Name        string  `json:"name"`
	DataType    string  `json:"dataType"`
	Cardinality int64   `json:"cardinality"`
	UniqueRatio float64 `json:"uniqueRatio"`
	IsLikelyPK  bool    `json:"isLikelyPK,omitempty"`
	IsLikelyFK  bool    `json:"isLikelyFK,omitempty"`
	IsLikelyMC  bool    `json:"isLikelyMachineCode,omitempty"`
	IsLikelyTS  bool    `json:"isLikelyTimestamp,omitempty"`
}

// ColumnEnum captures the value distribution for a low-cardinality column
// (cardinality ≤ 30). Kept in the ledger so the LLM doesn't need to re-probe.
type ColumnEnum struct {
	Name              string       `json:"name"`
	Cardinality       int          `json:"cardinality"`
	ValueDistribution []ValueCount `json:"valueDistribution"`
}

// ValueCount is one row from a GROUP BY frequency query.
type ValueCount struct {
	Value string  `json:"value"`
	Count int64   `json:"count"`
	Pct   float64 `json:"pct"`
}

// RelationshipAnalyzed summarises a multi-table FK discovery run.
// Key: sorted(tables) joined by "|".
type RelationshipAnalyzed struct {
	Tables          []string                `json:"tables"`           // sorted
	TopCandidates   []RelationshipCandidate `json:"topCandidates"`    // up to 5 highest-confidence
	AnalyzedInTurn  int                     `json:"analyzedInTurn"`
	TotalCandidates int                     `json:"totalCandidates"`
}

// RelationshipCandidate is one FK pairing extracted from analyze_relationships.
type RelationshipCandidate struct {
	FromTable    string  `json:"fromTable"`
	FromColumn   string  `json:"fromColumn"`
	ToTable      string  `json:"toTable"`
	ToColumn     string  `json:"toColumn"`
	Confidence   float64 `json:"confidence"`
	ValueOverlap float64 `json:"valueOverlap"`
	NameSim      float64 `json:"nameSimilarity"`
	Cardinality  string  `json:"cardinality"` // many_to_one / one_to_many / many_to_many / one_to_one
}

// TablesIndex is the cached result of list_lakehouse_tables.
type TablesIndex struct {
	Tables       []TableSummary `json:"tables"`
	LoadedInTurn int            `json:"loadedInTurn"`
}

// TableSummary is one row from the table listing.
type TableSummary struct {
	Name          string `json:"name"`
	Type          string `json:"type"` // table / view
	EstimatedRows int64  `json:"estimatedRows"`
}

// SearchKeyword holds the result of a query_data keyword_search call.
// Key: keyword + ":" + inTable (allows multiple keywords per table).
type SearchKeyword struct {
	Keyword        string             `json:"keyword"`
	InTable        string             `json:"inTable"`
	Matches        []SearchMatchedCol `json:"matches"`
	SearchedInTurn int                `json:"searchedInTurn"`
}

// SearchMatchedCol is one matched column from a keyword_search result.
type SearchMatchedCol struct {
	Column           string `json:"column"`
	TotalOccurrences int64  `json:"totalOccurrences"`
	SampleValueCount int    `json:"sampleValueCount"`
}

// DraftProposed tracks one entity (OD/Intent/Link) proposed in this thread.
// Key: the UUID returned by the propose tool (objectId/intentId/linkId).
type DraftProposed struct {
	ID                string `json:"id"`
	Type              string `json:"type"`   // "od" | "intent" | "link"
	Name              string `json:"name"`
	Status            string `json:"status"` // "pending" | "activated" | "deleted"
	Kind              string `json:"kind,omitempty"`
	SemanticSqlPreview string `json:"semanticSqlPreview,omitempty"` // first 100 chars
	Summary           string `json:"summary"`                        // one-line natural language
	ProposedInTurn    int    `json:"proposedInTurn"`
	LastUpdatedInTurn int    `json:"lastUpdatedInTurn"`
	// Intent-specific
	LinkedOdName    string `json:"linkedOdName,omitempty"`
	CanonicalMetric string `json:"canonicalMetric,omitempty"`
	// Link-specific
	FromOdName string `json:"fromOdName,omitempty"`
	ToOdName   string `json:"toOdName,omitempty"`
}

// OntologySnapshot is a compact view of the project's existing ontology,
// refreshed whenever the LLM calls list_ods/intents/links. Stale snapshots
// are acceptable — the LLM can call list_* again to refresh explicitly.
type OntologySnapshot struct {
	Ods               []OdSummary     `json:"ods"`
	Intents           []IntentSummary `json:"intents"`
	Links             []LinkSummary   `json:"links"`
	SnapshottedInTurn int             `json:"snapshottedInTurn"`
}

// OdSummary is one row from the list_ods result.
type OdSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	PropCount   int    `json:"propCount"`
	SourceTable string `json:"sourceTable,omitempty"`
	Mark        bool   `json:"mark"`
}

// IntentSummary is one row from the list_intents result.
type IntentSummary struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ObjectName      string `json:"objectName"`
	CanonicalMetric string `json:"canonicalMetric"`
	Mark            bool   `json:"mark"`
}

// LinkSummary is one row from the list_links result.
type LinkSummary struct {
	ID         string `json:"id"`
	FromOdName string `json:"fromOdName"`
	ToOdName   string `json:"toOdName"`
	FkColumn   string `json:"fkColumn,omitempty"`
	Mark       bool   `json:"mark"`
}

// New returns a fully-initialised empty BuilderLedger safe for immediate use.
// All maps are non-nil so callers can write without nil-checks.
func New() *BuilderLedger {
	return &BuilderLedger{
		Version:               0,
		SchemaVersion:         SchemaVersion,
		TablesExplored:        map[string]*TableExplored{},
		RelationshipsAnalyzed: map[string]*RelationshipAnalyzed{},
		SearchKeywords:        map[string]*SearchKeyword{},
		DraftsProposed:        map[string]*DraftProposed{},
	}
}

// IsEmpty reports whether the ledger has no content worth persisting.
func (l *BuilderLedger) IsEmpty() bool {
	if l == nil {
		return true
	}
	return len(l.TablesExplored) == 0 && len(l.DraftsProposed) == 0 &&
		len(l.RelationshipsAnalyzed) == 0 && len(l.SearchKeywords) == 0 &&
		l.OntologySnapshot == nil && l.LakehouseTables == nil
}

// EnsureMaps initialises any nil map in place (needed after JSON unmarshal
// into a zero-value BuilderLedger where missing collections become nil).
func (l *BuilderLedger) EnsureMaps() {
	if l.TablesExplored == nil {
		l.TablesExplored = map[string]*TableExplored{}
	}
	if l.RelationshipsAnalyzed == nil {
		l.RelationshipsAnalyzed = map[string]*RelationshipAnalyzed{}
	}
	if l.SearchKeywords == nil {
		l.SearchKeywords = map[string]*SearchKeyword{}
	}
	if l.DraftsProposed == nil {
		l.DraftsProposed = map[string]*DraftProposed{}
	}
}
