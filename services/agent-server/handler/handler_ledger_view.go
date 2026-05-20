package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/lakehouse2ontology/authmw"
	"github.com/lakehouse2ontology/services/agent-server/ledger"

	. "github.com/lakehouse2ontology/httputil"
)

// Named row types for ledger-view projection. Exported so JSON tags
// render consistently; the handler below populates and sorts these.
//
// Kept slim by design — the UI panel only needs summary fields, not
// the full Od/Intent definitions. Clicking through to full detail is
// still done by calling lookup or navigating to the Od page.

type ledgerOdRow struct {
	OdID              string   `json:"odId"`
	Name              string   `json:"name"`
	Kind              string   `json:"kind"`
	Description       string   `json:"description"`
	LoadedInTurn      int      `json:"loadedInTurn"`
	LoadMethod        string   `json:"loadMethod"`
	MatchedPropsCount int      `json:"matchedPropsCount"`
	AllPropNamesCount int      `json:"allPropNamesCount"`
	LinkCount         int      `json:"linkCount"`
	MatchedPropNames  []string `json:"matchedPropNames"`
}

type ledgerIntentRow struct {
	IntentID        string   `json:"intentId"`
	Name            string   `json:"name"`
	CanonicalMetric string   `json:"canonicalMetric"`
	AutoGroupBy     []string `json:"autoGroupBy"`
	MatchedTokens   []string `json:"matchedTokens"`
	FirstSeenInTurn int      `json:"firstSeenInTurn"`
}

type ledgerTokenRow struct {
	Token            string   `json:"token"`
	FirstSeen        int      `json:"firstSeen"`
	LastSeen         int      `json:"lastSeen"`
	StrongHit        bool     `json:"strongHit"`
	MatchedOdIDs     []string `json:"matchedOdIds"`
	MatchedIntentIDs []string `json:"matchedIntentIds"`
	MatchedPropCount int      `json:"matchedPropCount"`
}

type ledgerOkRow struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	FirstSeenInTurn int    `json:"firstSeenInTurn"`
}

// HandleLakehouseLedgerGet serves GET /api/ontology/lakehouse-ledger?threadId=<uuid>.
//
// Returns the current thread's ledger in a UI-friendly shape. Read-only:
// does not rebuild, does not save. Designed for the front-end "🧠 记忆"
// panel on /lakehouse/ontology/lakehouse-agent.
//
// Ordering: ods by loadedInTurn ASC; intents by firstSeenInTurn ASC;
// tokens by lastSeen DESC (most recently active first).
func HandleLakehouseLedgerGet(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		threadID := r.URL.Query().Get("threadId")
		if !IsValidUUID(threadID) {
			http.Error(w, "threadId is required (uuid)", http.StatusBadRequest)
			return
		}

		// Cross-project IDOR guard: confirm the caller can access this
		// thread's project before reading its ledger.
		if !authmw.EnforceEntityProject(w, r, db, "ont_agent_thread", "id", threadID) {
			return
		}

		// Resolve the thread's project for the response envelope.
		var projectID string
		if err := db.QueryRow(`SELECT project_id::text FROM ont_agent_thread WHERE id = $1`, threadID).Scan(&projectID); err != nil {
			http.Error(w, "thread not found", http.StatusNotFound)
			return
		}

		l, err := ledger.Load(r.Context(), db, threadID)
		if err != nil {
			http.Error(w, "ledger load: "+err.Error(), http.StatusInternalServerError)
			return
		}
		l.EnsureMaps()

		strongCount := 0
		for _, t := range l.Tokens {
			if t.StrongHit {
				strongCount++
			}
		}

		ods := make([]ledgerOdRow, 0, len(l.Ods))
		for _, od := range l.Ods {
			propNames := make([]string, 0, len(od.MatchedProps))
			for _, p := range od.MatchedProps {
				n := p.DisplayName
				if n == "" {
					n = p.Name
				}
				propNames = append(propNames, n)
			}
			ods = append(ods, ledgerOdRow{
				OdID:              od.OdID,
				Name:              od.Name,
				Kind:              od.Kind,
				Description:       od.Description,
				LoadedInTurn:      od.LoadedInTurn,
				LoadMethod:        od.LoadMethod,
				MatchedPropsCount: len(od.MatchedProps),
				AllPropNamesCount: len(od.AllPropNames),
				LinkCount:         len(od.Links),
				MatchedPropNames:  propNames,
			})
		}
		sort.Slice(ods, func(i, j int) bool {
			if ods[i].LoadedInTurn != ods[j].LoadedInTurn {
				return ods[i].LoadedInTurn < ods[j].LoadedInTurn
			}
			return ods[i].Name < ods[j].Name
		})

		intents := make([]ledgerIntentRow, 0, len(l.Intents))
		for _, mi := range l.Intents {
			// Normalise nil slices to []string{} so JSON marshals as [] not
			// null — the frontend reads `.length` / `.map` on these directly.
			autoGB := mi.AutoGroupBy
			if autoGB == nil {
				autoGB = []string{}
			}
			matchedToks := mi.MatchedTokens
			if matchedToks == nil {
				matchedToks = []string{}
			}
			intents = append(intents, ledgerIntentRow{
				IntentID:        mi.IntentID,
				Name:            mi.Name,
				CanonicalMetric: mi.CanonicalMetric,
				AutoGroupBy:     autoGB,
				MatchedTokens:   matchedToks,
				FirstSeenInTurn: mi.FirstSeenInTurn,
			})
		}
		sort.Slice(intents, func(i, j int) bool {
			if intents[i].FirstSeenInTurn != intents[j].FirstSeenInTurn {
				return intents[i].FirstSeenInTurn < intents[j].FirstSeenInTurn
			}
			return intents[i].Name < intents[j].Name
		})

		tokens := make([]ledgerTokenRow, 0, len(l.Tokens))
		for k, t := range l.Tokens {
			// Same nil→[] normalisation — frontend maps over these.
			mOds := t.MatchedOds
			if mOds == nil {
				mOds = []string{}
			}
			mIntents := t.MatchedIntents
			if mIntents == nil {
				mIntents = []string{}
			}
			tokens = append(tokens, ledgerTokenRow{
				Token:            k,
				FirstSeen:        t.FirstSeen,
				LastSeen:         t.LastSeen,
				StrongHit:        t.StrongHit,
				MatchedOdIDs:     mOds,
				MatchedIntentIDs: mIntents,
				MatchedPropCount: len(t.MatchedProps),
			})
		}
		sort.Slice(tokens, func(i, j int) bool {
			if tokens[i].LastSeen != tokens[j].LastSeen {
				return tokens[i].LastSeen > tokens[j].LastSeen
			}
			return tokens[i].Token < tokens[j].Token
		})

		oks := make([]ledgerOkRow, 0, len(l.OkEntries))
		for _, e := range l.OkEntries {
			oks = append(oks, ledgerOkRow{ID: e.ID, Title: e.Title, Summary: e.Summary, FirstSeenInTurn: e.FirstSeenInTurn})
		}
		ols := make([]ledgerOkRow, 0, len(l.OlEntries))
		for _, e := range l.OlEntries {
			ols = append(ols, ledgerOkRow{ID: e.ID, Title: e.Title, Summary: e.Summary, FirstSeenInTurn: e.FirstSeenInTurn})
		}

		out := M{
			"threadId":  threadID,
			"projectId": projectID,
			"summary": M{
				"version":          l.Version,
				"turnCount":        l.TurnCount,
				"odCount":          len(l.Ods),
				"intentCount":      len(l.Intents),
				"tokenCount":       len(l.Tokens),
				"strongTokenCount": strongCount,
				"okCount":          len(l.OkEntries),
				"olCount":          len(l.OlEntries),
				"rebuiltFromStep":  l.RebuiltFromStep,
			},
			"ods":       ods,
			"intents":   intents,
			"tokens":    tokens,
			"okEntries": oks,
			"olEntries": ols,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}
