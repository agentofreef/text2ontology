package handler

// handler_missions.go — read endpoint for the missions a thread has
// produced. Wire to GET /api/ontology/lakehouse-missions?thread_id={uuid}
// (frontend) and GET /internal/agent/missions (internal). The full
// mission JSON is returned per row; frontend renders the ledger.
//
// Empty list is the legitimate response for:
//   - threads that ran with USE_MISSION_ACT off (no shadow record)
//   - pre-MissionAct threads (mission_id NULL on every step)

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/lakehouse2ontology/authmw"
	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/mission"
)

// HandleMissionsByThread returns the missions persisted for a thread,
// newest first. Read-only; no auth check at handler level (the surface
// is gated by the shared authmw middleware on the mux).
func HandleMissionsByThread(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			JsonResp(w, M{"error": "method not allowed"})
			return
		}
		threadID := r.URL.Query().Get("thread_id")
		if threadID == "" {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"error": "thread_id is required"})
			return
		}
		// Cross-project IDOR guard: confirm the caller can access this
		// thread's project before listing its missions.
		if !authmw.EnforceEntityProject(w, r, db, "ont_agent_thread", "id", threadID) {
			return
		}
		store := mission.NewStore(db)
		missions, err := store.ListByThread(r.Context(), threadID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			JsonResp(w, M{"error": err.Error()})
			return
		}
		if missions == nil {
			missions = []*mission.Mission{}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"missions": missions})
	}
}
