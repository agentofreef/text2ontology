package core

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
	"github.com/lakehouse2ontology/llmclient"
)

func RegisterLLMConfigRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/llm-config", handleLLMConfig(db))
	mux.HandleFunc("/api/llm-config/models", handleLLMConfigModels(db))
	mux.HandleFunc("/api/llm-config/test-chat", handleLLMConfigTestChat(db))
	mux.HandleFunc("/api/llm-config/test-embedding", handleLLMConfigTestEmbedding(db))
	mux.HandleFunc("/api/llm-config/", handleLLMConfigByID(db))
	mux.HandleFunc("/api/llm-role-binding", handleRoleBinding(db))
	mux.HandleFunc("/api/llm-role-binding/", handleRoleBindingByName(db))
}

func handleLLMConfig(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		if r.Method == http.MethodPost {
			body := ReadBody(r)
			var id string
			err := db.QueryRow(`INSERT INTO llm_config (config_type, vendor, base_url, api_key, model_name, is_thinking, is_tool_call, vector_dim, is_active, note, alias, proxy_url, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, false, $9, NULLIF($10,''), $11, 'a0000000-0000-0000-0000-000000000001') RETURNING id`,
				StrVal(body, "configType"), StrVal(body, "vendor"), StrVal(body, "baseUrl"),
				StrVal(body, "apiKey"), StrVal(body, "modelName"), BoolVal(body, "isThinking"),
				BoolVal(body, "isToolCall"), body["vectorDim"], StrVal(body, "note"),
				StrVal(body, "alias"),
				NilIfEmpty(StrVal(body, "proxyUrl"))).Scan(&id)
			if err != nil {
				JsonError(w, http.StatusInternalServerError, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"id": id})
			return
		}

		configType := r.URL.Query().Get("configType")
		q := `SELECT id, config_type, vendor, base_url, COALESCE(api_key,''), model_name,
			is_thinking, COALESCE(is_tool_call, false), vector_dim, is_active, COALESCE(note,''), COALESCE(alias,''), COALESCE(proxy_url,''), created_at, updated_at FROM llm_config`
		args := []interface{}{}
		if configType != "" {
			q += " WHERE config_type = $1"
			args = append(args, configType)
		}
		q += " ORDER BY created_at DESC"

		rows, err := db.Query(q, args...)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var configs []M
		for rows.Next() {
			var id, ct, vendor, baseURL, apiKey, modelName, note, alias, proxyURL string
			var isThinking, isToolCall, isActive bool
			var vectorDim sql.NullInt64
			var createdAt, updatedAt time.Time
			rows.Scan(&id, &ct, &vendor, &baseURL, &apiKey, &modelName,
				&isThinking, &isToolCall, &vectorDim, &isActive, &note, &alias, &proxyURL, &createdAt, &updatedAt)
			dim := interface{}(nil)
			if vectorDim.Valid {
				dim = vectorDim.Int64
			}
			configs = append(configs, M{
				"id": id, "configType": ct, "vendor": vendor, "baseUrl": baseURL,
				"apiKey": llmclient.MaskApiKey(apiKey), "modelName": modelName,
				"isThinking": isThinking, "isToolCall": isToolCall, "vectorDim": dim, "isActive": isActive,
				"note": note, "alias": alias, "proxyUrl": proxyURL, "createdAt": createdAt.Format(time.RFC3339),
				"updatedAt": updatedAt.Format(time.RFC3339),
			})
		}
		if configs == nil {
			configs = []M{}
		}
		ListResp(w, configs, len(configs))
	}
}

func handleLLMConfigModels(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		baseURL := r.URL.Query().Get("baseUrl")
		apiKey := r.URL.Query().Get("apiKey")
		vendor := r.URL.Query().Get("vendor")
		if baseURL == "" {
			w.WriteHeader(http.StatusBadRequest)
			JsonResp(w, M{"error": "baseUrl is required"})
			return
		}

		// Anthropic doesn't have a /models endpoint — return known models
		if llmclient.IsAnthropic(vendor) {
			JsonResp(w, M{"data": []M{
				{"id": "claude-sonnet-4-20250514"},
				{"id": "claude-opus-4-20250514"},
				{"id": "claude-haiku-4-20250506"},
				{"id": "claude-3-7-sonnet-20250219"},
				{"id": "claude-3-5-haiku-20241022"},
			}})
			return
		}

		url := llmclient.BuildURL(baseURL, "/models")
		req, _ := http.NewRequest("GET", url, nil)
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: llmclient.TLSClientConfig()}}
		resp, err := client.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			JsonResp(w, M{"error": fmt.Sprintf("Failed to reach %s: %v", url, err)})
			return
		}
		defer resp.Body.Close()

		var result json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			w.WriteHeader(http.StatusBadGateway)
			JsonResp(w, M{"error": "Failed to parse model list response"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		CorsHeaders(w)
		w.Write(result)
	}
}

// runChatTest performs a one-shot "say hello" chat call and returns a result
// map plus an HTTP status. Shared by the ad-hoc /test-chat endpoint (called
// from the creation modal) and the by-id /test endpoint (called from the row
// button on the config list).
func runChatTest(baseURL, apiKey, modelName, vendor, proxyURL string, isThinking bool) (M, int) {
	messages := []M{{"role": "user", "content": "Say hello in one sentence."}}
	chatBody := M{"model": modelName, "messages": messages, "max_tokens": 100, "_vendor": vendor}
	if isThinking {
		chatBody["enable_thinking"] = true
		chatBody["thinking_budget"] = 200
	}
	start := time.Now()
	content, _, err := llmclient.DoChatFull(baseURL, apiKey, chatBody, proxyURL)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return M{"error": fmt.Sprintf("Connection failed: %v", err), "latencyMs": latency}, http.StatusBadGateway
	}
	return M{"success": true, "latencyMs": latency, "response": content, "model": modelName}, http.StatusOK
}

// runEmbeddingTest performs a one-shot embedding call and returns result +
// status. Same sharing pattern as runChatTest.
func runEmbeddingTest(baseURL, apiKey, modelName, proxyURL string) (M, int) {
	url := llmclient.BuildURL(baseURL, "/embeddings")
	embBody := M{"model": modelName, "input": []string{"test embedding dimension"}}
	reqBytes, _ := json.Marshal(embBody)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(reqBytes))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := llmclient.MakeClientPublic(15*time.Second, proxyURL)
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return M{"error": fmt.Sprintf("Connection failed: %v", err), "latencyMs": latency}, http.StatusBadGateway
	}
	defer resp.Body.Close()
	var embResp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil || len(embResp.Data) == 0 {
		return M{"error": "Failed to get embedding response", "latencyMs": latency}, http.StatusBadGateway
	}
	dim := len(embResp.Data[0].Embedding)
	return M{"success": true, "latencyMs": latency, "dimension": dim, "model": modelName}, http.StatusOK
}

func handleLLMConfigTestChat(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body := ReadBody(r)
		resp, status := runChatTest(
			StrVal(body, "baseUrl"),
			StrVal(body, "apiKey"),
			StrVal(body, "modelName"),
			StrVal(body, "vendor"),
			StrVal(body, "proxyUrl"),
			BoolVal(body, "isThinking"),
		)
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		JsonResp(w, resp)
	}
}

func handleLLMConfigTestEmbedding(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body := ReadBody(r)
		resp, status := runEmbeddingTest(
			StrVal(body, "baseUrl"),
			StrVal(body, "apiKey"),
			StrVal(body, "modelName"),
			StrVal(body, "proxyUrl"),
		)
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		JsonResp(w, resp)
	}
}

func handleLLMConfigByID(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		path := r.URL.Path

		if strings.HasSuffix(path, "/activate") {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			id := ExtractID(path, "/api/llm-config")
			id = strings.TrimSuffix(id, "/activate")

			// Toggle active — no longer deactivating same-type configs
			_, err := db.Exec(`UPDATE llm_config SET is_active = TRUE, updated_at = now() WHERE id = $1`, id)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		// POST /api/llm-config/{id}/test — probe the config's endpoint using
		// the real api_key stored in DB (the list endpoint masks it).
		if strings.HasSuffix(path, "/test") {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			id := ExtractID(path, "/api/llm-config")
			id = strings.TrimSuffix(id, "/test")

			var configType, vendor, baseURL, apiKey, modelName, proxyURL string
			var isThinking bool
			err := db.QueryRow(`SELECT config_type, vendor, base_url, COALESCE(api_key,''), model_name,
				is_thinking, COALESCE(proxy_url,'') FROM llm_config WHERE id = $1`, id).Scan(
				&configType, &vendor, &baseURL, &apiKey, &modelName, &isThinking, &proxyURL)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				JsonResp(w, M{"error": "config not found"})
				return
			}
			var resp M
			var status int
			if configType == "embedding" {
				resp, status = runEmbeddingTest(baseURL, apiKey, modelName, proxyURL)
			} else {
				resp, status = runChatTest(baseURL, apiKey, modelName, vendor, proxyURL, isThinking)
			}
			if status != http.StatusOK {
				w.WriteHeader(status)
			}
			JsonResp(w, resp)
			return
		}

		id := ExtractID(path, "/api/llm-config")

		// POST /api/llm-config/{id}/impact — pre-delete cascade preview
		if strings.HasSuffix(path, "/impact") {
			if r.Method != http.MethodPost && r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			impactID := ExtractID(path, "/api/llm-config")
			impactID = strings.TrimSuffix(impactID, "/impact")
			var testRunCount, roleBindingCount int
			_ = db.QueryRow(`SELECT count(*) FROM ont_test_run WHERE llm_config_id = $1`, impactID).Scan(&testRunCount)
			_ = db.QueryRow(`SELECT count(*) FROM llm_role_binding WHERE config_id = $1`, impactID).Scan(&roleBindingCount)
			roleNames := []string{}
			if rbRows, rbErr := db.Query(`SELECT role_name FROM llm_role_binding WHERE config_id = $1 ORDER BY role_name`, impactID); rbErr == nil {
				for rbRows.Next() {
					var rn string
					rbRows.Scan(&rn)
					roleNames = append(roleNames, rn)
				}
				rbRows.Close()
			}
			JsonResp(w, M{
				"testRuns":     testRunCount,     // SET NULL — snapshot retained, FK blanked
				"roleBindings": roleBindingCount, // CASCADE — silently deleted
				"roleNames":    roleNames,        // human-readable list of cascading bindings
				"canDelete":    true,             // never blocked; UI just informs
			})
			return
		}

		// GET — return full config including API key for editing
		if r.Method == http.MethodGet {
			var configType, vendor, baseURL, apiKey, modelName, note, alias, proxyURL string
			var isThinking, isToolCall, isActive bool
			var vectorDim sql.NullInt64
			err := db.QueryRow(`SELECT config_type, vendor, base_url, api_key, model_name,
				is_thinking, COALESCE(is_tool_call, false), vector_dim, is_active, COALESCE(note,''), COALESCE(alias,''), COALESCE(proxy_url,'')
				FROM llm_config WHERE id = $1`, id).Scan(
				&configType, &vendor, &baseURL, &apiKey, &modelName,
				&isThinking, &isToolCall, &vectorDim, &isActive, &note, &alias, &proxyURL)
			if err != nil {
				JsonError(w, http.StatusNotFound, M{"error": "config not found"})
				return
			}
			dim := interface{}(nil)
			if vectorDim.Valid {
				dim = vectorDim.Int64
			}
			JsonResp(w, M{
				"id": id, "configType": configType, "vendor": vendor, "baseUrl": baseURL,
				// Never echo the raw vendor API key on read. Frontend only
				// needs to know whether one is set; PUT sends the new key
				// when actually rotating. Returning the raw value let any
				// authenticated user exfiltrate billable LLM credentials.
				"apiKey": llmclient.MaskApiKey(apiKey), "modelName": modelName, "isThinking": isThinking,
				"isToolCall": isToolCall, "vectorDim": dim, "isActive": isActive,
				"note": note, "alias": alias, "proxyUrl": proxyURL,
			})
			return
		}

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			_, err := db.Exec(`UPDATE llm_config SET vendor=$1, base_url=$2, api_key=$3, model_name=$4,
				is_thinking=$5, is_tool_call=$6, vector_dim=$7, note=$8, alias=NULLIF($9,''), updated_at=now() WHERE id=$10`,
				StrVal(body, "vendor"), StrVal(body, "baseUrl"), StrVal(body, "apiKey"),
				StrVal(body, "modelName"), BoolVal(body, "isThinking"), BoolVal(body, "isToolCall"),
				body["vectorDim"], StrVal(body, "note"), StrVal(body, "alias"), id)
			if err != nil {
				JsonError(w, http.StatusInternalServerError, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		if r.Method == http.MethodDelete {
			// Test-run references no longer block deletion: ont_test_run.llm_config_id
			// is ON DELETE SET NULL and snapshots vendor/model/alias at run time,
			// so historical records survive the FK going to NULL.
			// Role bindings still cascade silently (CASCADE FK) — the UI
			// surfaces them via /impact before calling DELETE.
			_, err := db.Exec(`DELETE FROM llm_config WHERE id = $1`, id)
			if err != nil {
				JsonError(w, http.StatusInternalServerError, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func handleRoleBinding(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		if r.Method == http.MethodPut {
			body := ReadBody(r)
			roleName := StrVal(body, "roleName")
			configID := StrVal(body, "configId")
			if roleName == "" || configID == "" {
				w.WriteHeader(http.StatusBadRequest)
				JsonResp(w, M{"error": "roleName and configId are required"})
				return
			}
			_, err := db.Exec(`INSERT INTO llm_role_binding (role_name, config_id, updated_at)
				VALUES ($1, $2, now())
				ON CONFLICT (role_name) DO UPDATE SET config_id=$2, updated_at=now()`,
				roleName, configID)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		// GET — list all bindings with joined config info
		rows, err := db.Query(`SELECT r.role_name, r.config_id,
			c.model_name, c.vendor, c.base_url, COALESCE(c.is_thinking, false),
			COALESCE(c.is_tool_call, false), c.vector_dim, r.updated_at
			FROM llm_role_binding r
			JOIN llm_config c ON r.config_id = c.id
			ORDER BY r.role_name`)
		if err != nil {
			ListResp(w, []M{}, 0)
			return
		}
		defer rows.Close()

		var bindings []M
		for rows.Next() {
			var roleName, configID, modelName, vendor, baseURL string
			var isThinking, isToolCall bool
			var vectorDim sql.NullInt64
			var updatedAt time.Time
			rows.Scan(&roleName, &configID, &modelName, &vendor, &baseURL,
				&isThinking, &isToolCall, &vectorDim, &updatedAt)
			dim := interface{}(nil)
			if vectorDim.Valid {
				dim = vectorDim.Int64
			}
			bindings = append(bindings, M{
				"roleName":   roleName,
				"configId":   configID,
				"modelName":  modelName,
				"vendor":     vendor,
				"baseUrl":    baseURL,
				"isThinking": isThinking,
				"isToolCall": isToolCall,
				"vectorDim":  dim,
				"updatedAt":  updatedAt.Format(time.RFC3339),
			})
		}
		if bindings == nil {
			bindings = []M{}
		}
		ListResp(w, bindings, len(bindings))
	}
}

func handleRoleBindingByName(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			HandleOptions(w)
			return
		}
		CorsHeaders(w)

		roleName := strings.TrimPrefix(r.URL.Path, "/api/llm-role-binding/")
		if roleName == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if r.Method == http.MethodDelete {
			_, err := db.Exec(`DELETE FROM llm_role_binding WHERE role_name = $1`, roleName)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				JsonResp(w, M{"error": err.Error()})
				return
			}
			JsonResp(w, M{"success": true})
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
