package contracts

// KeywordHit represents a keyword_explanation table match result.
//
// Tier "FUZZY_LIKE" is a synthetic tier produced by the recall layer when a
// single token's FUZZY hits on the same value-column exceed 4 — they are
// collapsed into one representative hit carrying FuzzyValueCount, and the
// LLM is told to use a `contains` (ILIKE) filter on the column instead of
// being shown all N enumerated values. EXACT and VEC tiers never collapse.
type KeywordHit struct {
	KeywordID       string  `json:"keywordId"`
	Keyword         string  `json:"keyword"`
	MappedTable     string  `json:"mappedTable"`
	MappedField     string  `json:"mappedField"`
	KeywordExplain  string  `json:"keywordExplain"`
	Tier            string  `json:"tier"`  // "EXACT" | "FUZZY" | "FUZZY_LIKE" | "VEC"
	Score           float64 `json:"score"` // 1.0 (exact), 0.75 (fuzzy), cosine (vec)
	MatchedToken    string  `json:"matchedToken"`
	IsColumnRef     bool    `json:"isColumnRef"`               // true = column name/alias, false = data value
	FuzzyValueCount int     `json:"fuzzyValueCount,omitempty"` // # of values collapsed into FUZZY_LIKE; 0 otherwise
}

// PropertyMatch represents an ont_property resolved from keyword_explanation.mapped_table/field.
type PropertyMatch struct {
	PropertyID   string       `json:"propertyId"`
	Name         string       `json:"name"`
	DisplayName  string       `json:"displayName"`
	SourceColumn string       `json:"sourceColumn"`
	DataType     string       `json:"dataType"`
	Description  string       `json:"description"`
	ObjectTypeID string       `json:"objectTypeId"`
	OkID         string       `json:"okId,omitempty"`
	OkTitle      string       `json:"okTitle,omitempty"`
	OkSummary    string       `json:"okSummary,omitempty"`
	OkDefs       []string     `json:"okDefs,omitempty"`
	Keywords     []KeywordHit `json:"keywords"` // which keywords led to this property
}

// OdBlock groups matched properties under a single Od (ont_object_type).
//
// MatchedVia records every channel through which this Od was reached during
// recall. Possible values (deduped, insertion-ordered):
//
//	"property"           — a property hit JOIN'd back to this Od
//	"od-alias-keyword"   — lakehouse_keyword(object_id) row matched a token
//	"name"               — token equals/contains ont_object_type.name
//	"display_name"       — token equals/contains ont_object_type.display_name
//	"alias"              — token equals/contains ont_alias.alias_text where target_kind='object_type'
//
// Empty slice means "not yet annotated" (legacy callers); UI should render
// at least one badge per Od for clarity.
type OdBlock struct {
	OdID         string            `json:"odId"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Description  string            `json:"description"`
	MatchedProps []PropertyMatch   `json:"matchedProps"`           // properties with keyword hits (show detail)
	AllPropNames []string          `json:"allPropNames"`           // all property names on this Od (for reference)
	AllPropDescs map[string]string `json:"allPropDescs,omitempty"` // prop display name → description (for context)
	Links        []OdLink          `json:"links,omitempty"`
	MatchedVia   []string          `json:"matchedVia,omitempty"` // channels: property | od-alias-keyword | name | display_name | alias
}

// OdLink represents a relationship between two Od objects.
type OdLink struct {
	TargetOdName string `json:"targetOdName"`
	Cardinality  string `json:"cardinality"`
}

// OkEntry represents a non-property knowledge entry (concept/playbook) from the fallback path.
type OkEntry struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Tokens  []string `json:"tokens"` // which tokens triggered this
}

// OlEntry represents a confirmed learned fact (Ol) matched during recall.
// Match tier cascade: TAG_EXACT → TAG_FUZZY → VEC.
type OlEntry struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
	Tier    string   `json:"tier"`   // "TAG_EXACT" | "TAG_FUZZY" | "VEC"
	Score   float64  `json:"score"`  // 1.0 (exact), 0.75 (fuzzy), cosine (vec)
	Tokens  []string `json:"tokens"` // which tokens triggered this match
}

// AmbiguityCandidate is one branch of a genuinely ambiguous keyword —
// the keyword hit a property on this Od, but another unrelated Od also had a hit.
type AmbiguityCandidate struct {
	OdID          string `json:"odId"`
	OdName        string `json:"odName"`
	OdDescription string `json:"odDescription"`
	PropertyName  string `json:"propertyName"`
	PropertyDesc  string `json:"propertyDesc"`
}

// Ambiguity represents one ambiguous keyword: hit multiple Ods that do NOT
// share a common 1-end parent (cannot be auto-resolved via link topology).
type Ambiguity struct {
	Keyword    string               `json:"keyword"`
	Candidates []AmbiguityCandidate `json:"candidates"`
}

// FilterSpec is one canonical filter row inside a MetricIntent template.
// Mirrors the shape LLM writes into smartquery.filters so it can be copied verbatim.
type FilterSpec struct {
	Prop  string `json:"prop"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// MetricIntent represents a recalled "query intent shortcut" — a canonical
// smartquery template bound to natural-language trigger terms.
//
// Example: token "early order" → Intent "Order.Total" carries
//
//	metric=sum(Order_Quantity), filters=[], auto_group_by=[Order_Type],
//	response_template="共 {total}..."
//
// The LLM is expected to copy CanonicalMetric / CanonicalFilters / AutoGroupBy
// verbatim into smartquery args, then append the user's additional dimensions.
type MetricIntent struct {
	IntentID         string       `json:"intentId"`
	Name             string       `json:"name"`        // "Order.Quantity"
	DisplayName      string       `json:"displayName"` // "订单数量口径"
	ObjectID         string       `json:"objectId"`
	ObjectName       string       `json:"objectName"` // Od English name, feeds smartquery.objects
	CanonicalMetric  string       `json:"canonicalMetric"`
	CanonicalFilters []FilterSpec `json:"canonicalFilters"`
	AutoGroupBy      []string     `json:"autoGroupBy"`
	// Pivot config: if PivotOn is set, the smartquery executor pivots the
	// result JSON on that column. PivotValues fixes the column order (absent
	// → data-derived). PivotTotalLabel names the synthetic sum column.
	PivotOn            string   `json:"pivotOn,omitempty"`
	PivotValues        []string `json:"pivotValues,omitempty"`
	PivotTotalLabel    string   `json:"pivotTotalLabel,omitempty"`
	PivotPercentAxis   string   `json:"pivotPercentAxis,omitempty"`   // "row" (default) | "column"
	PivotPercentScope  string   `json:"pivotPercentScope,omitempty"`  // "filtered" (default) | "global"
	PivotPercentSuffix string   `json:"pivotPercentSuffix,omitempty"` // column suffix, e.g. "转化率"
	ResponseTemplate   string   `json:"responseTemplate"`
	Description        string   `json:"description"`
	Priority           int      `json:"priority"`
	Tier               string   `json:"tier"`          // "EXACT" | "FUZZY"
	MatchedTokens      []string `json:"matchedTokens"` // deduped tokens that hit this intent
}

// CachedContext is a read-only snapshot of a thread's prior recalls,
// supplied by the ledger layer to BuildLakehouseContextCached so recall
// can skip DB work for tokens already strongly resolved earlier in the
// conversation.
//
// Recall does not know where this comes from; the ledger package owns
// construction. Keeping the type inside recall avoids a circular import
// (ledger → recall).
//
// Semantics of "cold" (skip DB): a token is cold when
// CachedContext.Tokens[token].StrongHit == true. FUZZY/VEC-only hits
// are NOT strong, so a weak early match doesn't permanently poison
// the thread — see ledger/types.go for the full rule.
type CachedContext struct {
	Tokens    map[string]CachedToken  `json:"tokens,omitempty"`
	Ods       map[string]OdBlock      `json:"ods,omitempty"`
	Intents   map[string]MetricIntent `json:"intents,omitempty"`
	OkEntries map[string]OkEntry      `json:"okEntries,omitempty"`
	OlEntries map[string]OlEntry      `json:"olEntries,omitempty"`
}

// CachedToken mirrors ledger.LedgerToken's back-refs. recall reads it
// to decide whether a token is cold and which cached entities to
// splice in.
type CachedToken struct {
	StrongHit        bool            `json:"strongHit"`
	MatchedOdIDs     []string        `json:"matchedOdIds,omitempty"`
	MatchedIntentIDs []string        `json:"matchedIntentIds,omitempty"`
	MatchedProps     []CachedPropRef `json:"matchedProps,omitempty"`
}

// CachedPropRef is a denormalised property pointer.
type CachedPropRef struct {
	PropID string `json:"propId"`
	OdID   string `json:"odId"`
}

// IsCold reports whether the token should skip fresh DB recall. Used
// by BuildLakehouseContextCached's partition step.
func (c *CachedContext) IsCold(token string) bool {
	if c == nil || c.Tokens == nil {
		return false
	}
	t, ok := c.Tokens[token]
	return ok && t.StrongHit
}

// RecallResult is the complete output of a token recall operation.
type RecallResult struct {
	OdBlocks      []OdBlock               `json:"odBlocks"`
	OkEntries     []OkEntry               `json:"okEntries"`
	OlEntries     []OlEntry               `json:"olEntries"` // confirmed learned facts matched via tags/vector
	DirectOds     []OdBlock               `json:"directOds"`
	MetricIntents []MetricIntent          `json:"metricIntents,omitempty"` // canonical query templates
	HasMatches    bool                    `json:"hasMatches"`
	TokenDetails  map[string][]KeywordHit `json:"tokenDetails"` // token → keyword hits (for debug)
	ContextMD     string                  `json:"contextMarkdown"`
	Ambiguities   []Ambiguity             `json:"ambiguities,omitempty"` // genuine cross-Od ambiguities
}
