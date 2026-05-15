package wizard

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lakehouse2ontology/contracts"
)

// RegisterRoutes mounts wizard routes on mux.
//
//	GET  /api/connector/wizard/{id}/state    — read current wizard state
//	POST /api/connector/wizard/{id}/state    — update wizard state
//	POST /api/connector/wizard/{id}/confirm  — finalize wizard (mark completed)
func RegisterRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/connector/wizard/", handleWizard(db))
}

// jsonResp writes status + JSON body.
func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func handleWizard(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Path: /api/connector/wizard/{id}/state  or  /api/connector/wizard/{id}/confirm
		rest := strings.TrimPrefix(r.URL.Path, "/api/connector/wizard/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		id, sub := parts[0], parts[1]
		if _, err := uuid.Parse(id); err != nil {
			http.Error(w, "invalid source id", http.StatusBadRequest)
			return
		}

		switch {
		case sub == "state" && r.Method == http.MethodGet:
			st, err := Get(r.Context(), db, id)
			if err != nil {
				jsonResp(w, http.StatusNotFound, contracts.ErrorEnvelope{
					Code: "NOT_FOUND", Message: err.Error(),
				})
				return
			}
			jsonResp(w, http.StatusOK, st)

		case sub == "state" && r.Method == http.MethodPost:
			var st contracts.WizardStateUpdate
			if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
				jsonResp(w, http.StatusBadRequest, contracts.ErrorEnvelope{
					Code: "BAD_REQUEST", Message: err.Error(),
				})
				return
			}
			if err := Update(r.Context(), db, id, st); err != nil {
				jsonResp(w, http.StatusInternalServerError, contracts.ErrorEnvelope{
					Code: "DB_ERROR", Message: err.Error(),
				})
				return
			}
			jsonResp(w, http.StatusOK, map[string]any{"ok": true})

		case sub == "confirm" && r.Method == http.MethodPost:
			// Optional body — if the caller sends table_roles / column_roles /
			// link_decisions inline (the simplified wizard does this so users
			// can submit in one click), persist them to wizard_state FIRST.
			// Confirm() then reads from wizard_state in DB; without this the
			// body would be silently ignored and ontology generation would
			// see an empty catalog. Empty body is fine — Confirm uses the
			// existing wizard_state from prior /state POSTs.
			if r.ContentLength != 0 {
				var st contracts.WizardStateUpdate
				if err := json.NewDecoder(r.Body).Decode(&st); err == nil &&
					(len(st.TableRoles) > 0 || len(st.ColumnRoles) > 0 || len(st.LinkDecisions) > 0) {
					if err := Update(r.Context(), db, id, st); err != nil {
						jsonResp(w, http.StatusInternalServerError, contracts.ErrorEnvelope{
							Code: "DB_ERROR", Message: "save wizard_state: " + err.Error(),
						})
						return
					}
				}
			}
			if err := Confirm(r.Context(), db, id); err != nil {
				if errors.Is(err, ErrAlreadyCompleted) {
					jsonResp(w, http.StatusConflict, contracts.ErrorEnvelope{
						Code: "ALREADY_COMPLETED", Message: err.Error(),
					})
					return
				}
				jsonResp(w, http.StatusInternalServerError, contracts.ErrorEnvelope{
					Code: "DB_ERROR", Message: err.Error(),
				})
				return
			}
			jsonResp(w, http.StatusOK, map[string]any{"ok": true, "status": "completed"})

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}
