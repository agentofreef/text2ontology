// Package ledger implements the Thread Memory Ledger: a per-thread,
// append-and-merge accumulator of every Od / Intent / Ok / Ol / token
// recall and lookup has ever surfaced in this thread. It lives inside
// ont_agent_thread.thread_state JSONB under the "ledger" key, alongside
// the existing parent_thread_id / seed_system_prompt / status fields.
//
// The ledger exists to solve two compounding problems in the lakehouse
// agent:
//
//  1. LLM repeatedly calls lookup for the same Od across turns because
//     cross-turn replay truncated tool results to 500 chars (fix: ledger
//     carries the structured truth across turns; replay drops the stub).
//  2. Single-turn recall was filtering out previously-loaded Ods via a
//     fullyLoadedSet mask, so those Ods became invisible on turn N+1
//     (fix: ledger-aware recall re-emits cached Ods directly; nothing
//     is filtered).
//
// Invariants:
//
//   - Ledger is a DENSE snapshot of what the thread has seen, not an
//     event log. Two concurrent turns merging the same token must
//     commute (merge is idempotent on token key).
//   - Stale entries (referencing deleted Ods/Intents) are lazily cleaned
//     up at lookup time — the agent's lookup tool returns "not found"
//     and the ledger drops the dangling pointer on its next merge.
//   - Ledger.Version is an integer that monotonically increases on each
//     successful Save; optimistic concurrency rejects writes where the
//     DB's current version != the caller's old version.
package ledger

import "github.com/lakehouse2ontology/services/agent-server/recall"

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

// New returns a fully-initialised empty Ledger safe for immediate use.
// All maps are non-nil so callers can write without a nil-check.
func New() *Ledger {
	return &Ledger{
		Version:       0,
		SchemaVersion: SchemaVersion,
		Ods:           map[string]*LedgerOd{},
		Intents:       map[string]*LedgerIntent{},
		OkEntries:     map[string]*LedgerOk{},
		OlEntries:     map[string]*LedgerOl{},
		Tokens:        map[string]*LedgerToken{},
	}
}

// IsEmpty reports whether the ledger has no content worth persisting.
// Used by the handler to decide whether to trigger a lazy rebuild from
// ont_agent_step history.
func (l *Ledger) IsEmpty() bool {
	if l == nil {
		return true
	}
	return len(l.Ods) == 0 && len(l.Intents) == 0 &&
		len(l.OkEntries) == 0 && len(l.OlEntries) == 0 &&
		len(l.Tokens) == 0
}

// EnsureMaps initialises any nil map in place. JSON unmarshal into a
// zero-value Ledger leaves missing collections as nil, which breaks
// the "write without nil-check" contract expected by merge helpers.
func (l *Ledger) EnsureMaps() {
	if l.Ods == nil {
		l.Ods = map[string]*LedgerOd{}
	}
	if l.Intents == nil {
		l.Intents = map[string]*LedgerIntent{}
	}
	if l.OkEntries == nil {
		l.OkEntries = map[string]*LedgerOk{}
	}
	if l.OlEntries == nil {
		l.OlEntries = map[string]*LedgerOl{}
	}
	if l.Tokens == nil {
		l.Tokens = map[string]*LedgerToken{}
	}
}

// LedgerOd wraps recall.OdBlock with provenance fields. Embedding means
// JSON output has OdBlock fields at the top level (odId, name, kind,
// …) alongside loadedInTurn / loadMethod — callers don't need to reach
// into a nested object to read an Od's name.
type LedgerOd struct {
	recall.OdBlock
	LoadedInTurn int    `json:"loadedInTurn"`
	LoadMethod   string `json:"loadMethod"` // "lookup" | "recall-hit" | "recall-fallback" | "legacy-migrated"
}

// LedgerIntent wraps recall.MetricIntent with first-seen tracking.
type LedgerIntent struct {
	recall.MetricIntent
	FirstSeenInTurn int `json:"firstSeenInTurn"`
}

// LedgerOk wraps recall.OkEntry with first-seen tracking.
type LedgerOk struct {
	recall.OkEntry
	FirstSeenInTurn int `json:"firstSeenInTurn"`
}

// LedgerOl wraps recall.OlEntry with first-seen tracking.
type LedgerOl struct {
	recall.OlEntry
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
