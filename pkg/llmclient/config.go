package llmclient

import "database/sql"

// GetConfigForRole looks up the llm_role_binding for roleName, joins to llm_config,
// and returns the config. Falls back to the legacy is_active logic if no binding exists.
func GetConfigForRole(db *sql.DB, roleName string) (baseURL, apiKey, modelName string, isThinking, isToolCall bool, vendor string) {
	var proxyURL string
	baseURL, apiKey, modelName, isThinking, isToolCall, proxyURL, vendor = GetConfigForRoleWithProxy(db, roleName)
	_ = proxyURL
	return
}

// GetConfigForRoleWithProxy returns all config fields including proxy_url and vendor.
func GetConfigForRoleWithProxy(db *sql.DB, roleName string) (baseURL, apiKey, modelName string, isThinking, isToolCall bool, proxyURL, vendor string) {
	var proxy sql.NullString
	err := db.QueryRow(`
		SELECT c.base_url, COALESCE(c.api_key,''), c.model_name,
		       COALESCE(c.is_thinking, false), COALESCE(c.is_tool_call, false),
		       c.proxy_url, COALESCE(c.vendor,'')
		FROM llm_role_binding r
		JOIN llm_config c ON r.config_id = c.id
		WHERE r.role_name = $1`, roleName).
		Scan(&baseURL, &apiKey, &modelName, &isThinking, &isToolCall, &proxy, &vendor)
	if err == nil {
		if proxy.Valid {
			proxyURL = proxy.String
		}
		return
	}

	if roleName == "embedding" {
		err = db.QueryRow(`SELECT base_url, COALESCE(api_key,''), model_name,
			COALESCE(is_thinking, false), COALESCE(is_tool_call, false), proxy_url, COALESCE(vendor,'')
			FROM llm_config WHERE config_type='embedding' AND is_active=TRUE LIMIT 1`).
			Scan(&baseURL, &apiKey, &modelName, &isThinking, &isToolCall, &proxy, &vendor)
	} else {
		err = db.QueryRow(`SELECT base_url, COALESCE(api_key,''), model_name,
			COALESCE(is_thinking, false), COALESCE(is_tool_call, false), proxy_url, COALESCE(vendor,'')
			FROM llm_config WHERE config_type='chat' AND is_active=TRUE LIMIT 1`).
			Scan(&baseURL, &apiKey, &modelName, &isThinking, &isToolCall, &proxy, &vendor)
	}
	if err != nil {
		return "", "", "", false, false, "", ""
	}
	if proxy.Valid {
		proxyURL = proxy.String
	}
	return
}

// GetConfigByID loads a single llm_config row by primary key.
// Returns the same field set as GetConfigForRoleWithProxy. Returns all-zero values if not found.
func GetConfigByID(db *sql.DB, configID string) (baseURL, apiKey, modelName string, isThinking, isToolCall bool, proxyURL, vendor string) {
	var proxy sql.NullString
	err := db.QueryRow(`
		SELECT base_url, COALESCE(api_key,''), model_name,
		       COALESCE(is_thinking, false), COALESCE(is_tool_call, false),
		       COALESCE(proxy_url,''), COALESCE(vendor,'')
		FROM llm_config WHERE id = $1`, configID).
		Scan(&baseURL, &apiKey, &modelName, &isThinking, &isToolCall, &proxy, &vendor)
	if err != nil {
		return "", "", "", false, false, "", ""
	}
	if proxy.Valid {
		proxyURL = proxy.String
	}
	return
}

// GetActiveEmbeddingConfig returns base_url, api_key, model_name from the active embedding config.
func GetActiveEmbeddingConfig(db *sql.DB) (string, string, string) {
	url, key, model, _, _, _ := GetConfigForRole(db, "embedding")
	return url, key, model
}

// GetActiveChatConfig returns base_url, api_key, model_name, is_thinking, vendor from the active chat config.
func GetActiveChatConfig(db *sql.DB) (string, string, string, bool, string) {
	url, key, model, thinking, _, vendor := GetConfigForRole(db, "agent")
	return url, key, model, thinking, vendor
}
