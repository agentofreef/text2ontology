package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/lakehouse2ontology/services/agent-server/ledger"
	"github.com/lakehouse2ontology/services/agent-server/recall"

	. "github.com/lakehouse2ontology/httputil"
)

// HandleLedgerDebug serves /api/ontology/_debug/ledger-rebuild.
//
// GET  ?threadId=<uuid>&save=0|1
//   - Loads the current ledger, runs RebuildFromSteps against the
//     thread's recent user-role steps (limit 20), and returns the
//     rebuilt ledger JSON.
//   - If save=1, persists the rebuilt ledger back to thread_state.
//     Default is save=0 (read-only preview).
//
// Intended for operator inspection during Phase 2 validation. Not
// wired into the main agent flow yet.
func HandleLedgerDebug(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		threadID := r.URL.Query().Get("threadId")
		if !IsValidUUID(threadID) {
			http.Error(w, "threadId is required (uuid)", http.StatusBadRequest)
			return
		}
		save := r.URL.Query().Get("save") == "1"

		// Look up project_id + active version for this thread.
		var projectID string
		if err := db.QueryRow(`SELECT project_id::text FROM ont_agent_thread WHERE id = $1`, threadID).Scan(&projectID); err != nil {
			http.Error(w, "thread not found: "+err.Error(), http.StatusNotFound)
			return
		}
		// Load existing ledger (may be empty for legacy threads).
		l, err := ledger.Load(r.Context(), db, threadID)
		if err != nil {
			http.Error(w, "ledger load: "+err.Error(), http.StatusInternalServerError)
			return
		}
		oldVersion := l.Version

		// Bind the tokenizer + recall as plain function pointers.
		// ledger/rebuild.go stays project-agnostic by design.
		tokenize := func(q string) []string {
			fewShots := loadAnnotationFewShots(db, projectID, q)
			return tokenizeWithAnnotationFewShots(db, projectID, q, fewShots)
		}
		doRecall := func(tokens []string, question string) recall.RecallResult {
			return recall.BuildLakehouseContext(r.Context(), db, projectID, tokens, question)
		}

		replayed, err := ledger.RebuildFromSteps(db, threadID, 20, tokenize, doRecall, l)
		if err != nil {
			http.Error(w, "rebuild: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if save {
			if err := ledger.Save(db, threadID, l, oldVersion); err != nil {
				http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		out := M{
			"threadId":      threadID,
			"projectId":     projectID,
			"stepsReplayed": replayed,
			"saved":          save,
			"ledgerVersion":  l.Version,
			"odCount":        len(l.Ods),
			"intentCount":    len(l.Intents),
			"tokenCount":     len(l.Tokens),
			"ledger":         l,
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}
