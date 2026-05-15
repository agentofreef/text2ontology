package contracts

// SchemaVersion is bumped when the JSON shape changes incompatibly.
// On Load, an older SchemaVersion triggers a rebuild rather than a
// best-effort unmarshal (simpler than migration, cheap given rebuild
// is bounded to ~20 steps).
const SchemaVersion = 1

// Ledger is the top-level accumulator persisted at
// thread_state->'ledger'. All collection fields are keyed by stable
// UUIDs (odId / intentId) or canonical strings (tokens), so merging
// the same entity twice is a no-op — this is the merge-idempotence
// invariant. Pointer maps let callers detect presence with `_, ok :=`
// without worrying about zero-value vs. absent distinction.
type Ledger struct {
	Version         int `json:"version"`
	SchemaVersion   int `json:"schemaVersion"`
	TurnCount       int `json:"turnCount"`
	RebuiltFromStep int `json:"rebuiltFromStep,omitempty"`

	Ods       map[string]*LedgerOd     `json:"ods,omitempty"`
	Intents   map[string]*LedgerIntent `json:"intents,omitempty"`
	OkEntries map[string]*LedgerOk     `json:"okEntries,omitempty"`
	OlEntries map[string]*LedgerOl     `json:"olEntries,omitempty"`
	Tokens    map[string]*LedgerToken  `json:"tokens,omitempty"`

	AmbiguitiesResolved []LedgerAmbigResolved `json:"ambiguitiesResolved,omitempty"`
}

// LedgerOd wraps OdBlock with provenance fields. Embedding means
// JSON output has OdBlock fields at the top level (odId, name, kind,
// …) alongside loadedInTurn / loadMethod — callers don't need to reach
// into a nested object to read an Od's name.
type LedgerOd struct {
	OdBlock
	LoadedInTurn int    `json:"loadedInTurn"`
	LoadMethod   string `json:"loadMethod"` // "lookup" | "recall-hit" | "recall-fallback" | "legacy-migrated"
}

// LedgerIntent wraps MetricIntent with first-seen tracking.
type LedgerIntent struct {
	MetricIntent
	FirstSeenInTurn int `json:"firstSeenInTurn"`
}

// LedgerOk wraps OkEntry with first-seen tracking.
type LedgerOk struct {
	OkEntry
	FirstSeenInTurn int `json:"firstSeenInTurn"`
}

// LedgerOl wraps OlEntry with first-seen tracking.
type LedgerOl struct {
	OlEntry
	FirstSeenInTurn int `json:"firstSeenInTurn"`
}

// LedgerToken records what a token has hit across turns. StrongHit
// marks a token as "cold" for partition purposes — recall can skip DB
// work and re-emit the cached OdBlocks / Intents directly.
//
// Definition of StrongHit (aka "established enough to cache"):
//
//   - At least one EXACT-tier keyword hit mapping to an Od or property, OR
//   - At least one MetricIntent hit.
//
// FUZZY / VEC-only tokens are NOT strong; they remain hot so a later
// turn with sharper context can refresh them. This prevents an early
// weak match from permanently poisoning the thread.
type LedgerToken struct {
	FirstSeen      int             `json:"firstSeen"`
	LastSeen       int             `json:"lastSeen"`
	StrongHit      bool            `json:"strongHit"`
	MatchedOds     []string        `json:"matchedOds,omitempty"`     // odIds
	MatchedIntents []string        `json:"matchedIntents,omitempty"` // intentIds
	MatchedProps   []LedgerPropRef `json:"matchedProps,omitempty"`
}

// LedgerPropRef is a denormalised pointer from a token to a property
// on a specific Od. Carrying both IDs avoids an extra map lookup when
// rendering / merging.
type LedgerPropRef struct {
	PropID string `json:"propId"`
	OdID   string `json:"odId"`
}

// LedgerAmbigResolved records a disambiguation decision the user has
// made on a prior turn. If the same keyword re-appears, the resolved
// odId is preferred over re-prompting. Currently unused (disambiguation
// is inline in the main thread), but reserved for the clarify_and_branch
// path when it's re-enabled.
type LedgerAmbigResolved struct {
	Keyword        string `json:"keyword"`
	ResolvedToOdID string `json:"resolvedToOdId"`
	ResolvedInTurn int    `json:"resolvedInTurn"`
}
