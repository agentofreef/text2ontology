package llmclient

import (
	"bytes"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	. "github.com/lakehouse2ontology/httputil"
)

// llmHeaderTimeout bounds time-to-first-byte (TTFT) for every LLM HTTP call.
// Slow aggregators (siliconflow, openrouter, …) can take 60-150s to start
// responding for big/reasoning models, so this is intentionally generous —
// it only fires when the provider sends no headers at all.
const llmHeaderTimeout = 180 * time.Second

// httpClientCache caches *http.Client instances keyed by "<timeoutSeconds>|<proxyURL>".
// Reusing clients lets the underlying connection pool reuse keep-alive sockets
// across LLM calls — critical when worker pools fan out 10+ concurrent requests.
var httpClientCache sync.Map

// MakeClientPublic builds an http.Client with optional proxy support (exported for handler use).
func MakeClientPublic(timeout time.Duration, proxyURL string) *http.Client {
	return makeClient(timeout, proxyURL)
}

// hasVersionPrefix returns true if the URL path already contains a versioned API
// segment like /v1/, /v4/, etc. This allows providers like GLM (zhipu) whose
// base URL is https://open.bigmodel.cn/api/paas/v4 to work without having
// /v1 appended again.
func hasVersionPrefix(baseURL string) bool {
	// Trim scheme to inspect path only
	u := strings.TrimRight(baseURL, "/")
	for _, prefix := range []string{"/v1", "/v2", "/v3", "/v4", "/v5"} {
		if strings.HasSuffix(u, prefix) {
			return true
		}
	}
	return false
}

// BuildURL constructs a full API endpoint URL.
// If baseURL already ends with a version prefix (e.g. /v4), the endpoint is
// appended directly; otherwise /v1 is prepended to the endpoint.
//
//	BuildURL("http://localhost:8132", "/chat/completions")
//	  → "http://localhost:8132/v1/chat/completions"
//
//	BuildURL("https://open.bigmodel.cn/api/paas/v4", "/chat/completions")
//	  → "https://open.bigmodel.cn/api/paas/v4/chat/completions"
func BuildURL(baseURL, endpoint string) string {
	base := strings.TrimRight(baseURL, "/")
	if hasVersionPrefix(base) {
		return base + endpoint
	}
	return base + "/v1" + endpoint
}

// makeClient builds (or returns a cached) http.Client with optional proxy support.
// proxyURL can be http://, https://, or socks5:// URL. Empty means no proxy.
//
// Clients are cached per (timeout, proxyURL) tuple via httpClientCache so the
// underlying http.Transport's idle connection pool is shared across LLM calls.
// Without this, every call paid the TLS-handshake cost.
func makeClient(timeout time.Duration, proxyURL string) *http.Client {
	key := fmt.Sprintf("%d|%s", int64(timeout/time.Second), proxyURL)
	if v, ok := httpClientCache.Load(key); ok {
		return v.(*http.Client)
	}
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
		// Bound TTFT separately from the overall Client.Timeout so a provider
		// that accepts the connection but never replies still fails fast.
		ResponseHeaderTimeout: llmHeaderTimeout,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	} else {
		transport.Proxy = nil
	}
	client := &http.Client{Timeout: timeout, Transport: transport}
	actual, _ := httpClientCache.LoadOrStore(key, client)
	return actual.(*http.Client)
}

// makeStreamClient builds an http.Client tuned for streaming (SSE) LLM calls.
// Client.Timeout is 0 — a streaming body legitimately stays open for minutes,
// so an overall deadline would truncate long agent turns mid-stream. Instead
// the transport bounds connect, TLS handshake and TTFT; once headers arrive
// the body is read for as long as the model keeps streaming.
func makeStreamClient(proxyURL string) *http.Client {
	key := "stream|" + proxyURL
	if v, ok := httpClientCache.Load(key); ok {
		return v.(*http.Client)
	}
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: llmHeaderTimeout,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	} else {
		transport.Proxy = nil
	}
	client := &http.Client{Timeout: 0, Transport: transport}
	actual, _ := httpClientCache.LoadOrStore(key, client)
	return actual.(*http.Client)
}

// EmbedTexts calls the active embedding model to embed a list of texts.
// Returns vectors. Truncates each text to 500 chars, batches ≤4.
func EmbedTexts(db *sql.DB, texts []string) ([][]float64, error) {
	bURL, aKey, mName, _, _, proxyURL, _ := GetConfigForRoleWithProxy(db, "embedding")
	baseURL, apiKey, modelName := bURL, aKey, mName
	if baseURL == "" || modelName == "" {
		return nil, fmt.Errorf("no active embedding config")
	}

	truncated := make([]string, len(texts))
	for i, t := range texts {
		if utf8.RuneCountInString(t) > 500 {
			runes := []rune(t)
			truncated[i] = string(runes[:500])
		} else {
			truncated[i] = t
		}
	}

	client := makeClient(30*time.Second, proxyURL)
	allVecs := make([][]float64, len(truncated))

	for start := 0; start < len(truncated); start += 4 {
		end := start + 4
		if end > len(truncated) {
			end = len(truncated)
		}
		batch := truncated[start:end]

		reqBody := M{"model": modelName, "input": batch}
		reqBytes, _ := json.Marshal(reqBody)
		url := BuildURL(baseURL, "/embeddings")
		req, _ := http.NewRequest("POST", url, bytes.NewReader(reqBytes))
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("embedding request failed: %w", err)
		}
		var result struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
				Index     int       `json:"index"`
			} `json:"data"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		for _, d := range result.Data {
			allVecs[start+d.Index] = d.Embedding
		}
	}

	return allVecs, nil
}

// CallChatLLM makes a non-streaming LLM call and returns the content (think tags stripped).
func CallChatLLM(db *sql.DB, systemPrompt, userPrompt string) (string, error) {
	bURL, aKey, mName, isThinking, _, proxyURL, vendor := GetConfigForRoleWithProxy(db, "agent")
	baseURL, apiKey, modelName := bURL, aKey, mName
	if baseURL == "" || modelName == "" {
		return "", fmt.Errorf("no active chat config")
	}

	messages := []M{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}
	chatBody := M{"model": modelName, "messages": messages, "max_tokens": 4096, "temperature": 0, "_vendor": vendor}
	if isThinking {
		chatBody["max_tokens"] = 16384
	}

	content, err := DoChatWithProxy(baseURL, apiKey, chatBody, proxyURL)
	if err != nil {
		return "", err
	}
	return StripThinkTags(content), nil
}

// StripThinkTags removes <think>...</think> tags and returns trimmed content.
func StripThinkTags(content string) string {
	if idx := strings.Index(content, "</think>"); idx >= 0 {
		content = content[idx+len("</think>"):]
	}
	return strings.TrimSpace(content)
}

// ExtractJSON extracts JSON content from markdown code fences if present.
func ExtractJSON(content string) string {
	if !strings.Contains(content, "```") {
		return content
	}
	lines := strings.Split(content, "\n")
	var jsonLines []string
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inBlock = !inBlock
			continue
		}
		if inBlock {
			jsonLines = append(jsonLines, line)
		}
	}
	if len(jsonLines) > 0 {
		return strings.Join(jsonLines, "\n")
	}
	return content
}

// ExtractChatContent extracts content string from an OpenAI-compatible chat response.
func ExtractChatContent(chatResp M) string {
	if choices, ok := chatResp["choices"].([]interface{}); ok && len(choices) > 0 {
		if msg, ok := choices[0].(map[string]interface{}); ok {
			if m, ok := msg["message"].(map[string]interface{}); ok {
				return fmt.Sprintf("%v", m["content"])
			}
		}
	}
	return ""
}

// DoChat makes a raw chat completion call with the given body to the specified endpoint.
// Returns the content string from the response.
// TokenUsage holds token consumption from LLM API response.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func DoChat(baseURL, apiKey string, chatBody M) (string, error) {
	content, _, err := DoChatFull(baseURL, apiKey, chatBody, "")
	return content, err
}

// DoChatWithUsage returns content + token usage.
func DoChatWithUsage(baseURL, apiKey string, chatBody M) (string, *TokenUsage, error) {
	return DoChatFull(baseURL, apiKey, chatBody, "")
}

// DoChatWithProxy makes a chat completion call with optional proxy support.
func DoChatWithProxy(baseURL, apiKey string, chatBody M, proxyURL string) (string, error) {
	content, _, err := DoChatFull(baseURL, apiKey, chatBody, proxyURL)
	return content, err
}

// isReasoningModel checks if the model is a reasoning model that has parameter restrictions.
func isReasoningModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "gpt-5") ||
		strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4")
}

// NormalizeMaxTokens converts max_tokens → max_completion_tokens for models that require it.
// Also removes unsupported parameters (temperature) for reasoning models.
// Modifies chatBody in place.
func NormalizeMaxTokens(chatBody M) {
	model, _ := chatBody["model"].(string)
	if !isReasoningModel(model) {
		return
	}
	// max_tokens → max_completion_tokens
	if v, ok := chatBody["max_tokens"]; ok {
		chatBody["max_completion_tokens"] = v
		delete(chatBody, "max_tokens")
	}
	// Reasoning models only support temperature=1 (default); remove it
	delete(chatBody, "temperature")
}

// DoChatFull returns content + token usage + error.
// If chatBody contains "_vendor" = "anthropic", routes to the Anthropic Messages API.
func DoChatFull(baseURL, apiKey string, chatBody M, proxyURL string) (string, *TokenUsage, error) {
	vendor, _ := chatBody["_vendor"].(string)
	delete(chatBody, "_vendor")

	// Per-baseURL rate limit — blocks here when the worker pool fans out
	// faster than the provider's RPM allows. See ratelimit.go.
	waitLLMRate(baseURL)

	if IsAnthropic(vendor) {
		return doAnthropicChat(baseURL, apiKey, chatBody, proxyURL)
	}

	// OpenAI-compatible path
	NormalizeMaxTokens(chatBody)
	reqBytes, _ := json.Marshal(chatBody)
	// 300s overall cap: a non-streamed reasoning-model reply can take minutes
	// to fully render. TTFT is bounded separately by ResponseHeaderTimeout.
	client := makeClient(300*time.Second, proxyURL)
	url := BuildURL(baseURL, "/chat/completions")
	req, _ := http.NewRequest("POST", url, bytes.NewReader(reqBytes))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(body))
	}

	var chatResp M
	json.NewDecoder(resp.Body).Decode(&chatResp)

	// Extract usage
	var usage *TokenUsage
	if u, ok := chatResp["usage"].(map[string]interface{}); ok {
		usage = &TokenUsage{}
		if v, ok := u["prompt_tokens"].(float64); ok {
			usage.PromptTokens = int(v)
		}
		if v, ok := u["completion_tokens"].(float64); ok {
			usage.CompletionTokens = int(v)
		}
		if v, ok := u["total_tokens"].(float64); ok {
			usage.TotalTokens = int(v)
		}
	}

	return ExtractChatContent(chatResp), usage, nil
}

// DoChatStreamCallback makes a streaming chat completion call.
// onToken is called for each content delta; onThinking for <think> content.
// Supports both OpenAI-compatible and Anthropic APIs via the "_vendor" field in chatBody.
// Returns the full accumulated content (think-tags stripped), usage, and error.
func DoChatStreamCallback(baseURL, apiKey string, chatBody M, onToken, onThinking func(string)) (string, *TokenUsage, error) {
	vendor, _ := chatBody["_vendor"].(string)
	delete(chatBody, "_vendor")

	chatBody["stream"] = true
	if !IsAnthropic(vendor) {
		// Request usage in streaming mode (OpenAI stream_options)
		chatBody["stream_options"] = M{"include_usage": true}
	}

	// Per-baseURL rate limit — keeps streaming fanout from saturating the
	// provider's RPM cap. Same limiter as the non-streaming path.
	waitLLMRate(baseURL)

	req, err := PrepareStreamRequest(baseURL, apiKey, vendor, chatBody)
	if err != nil {
		return "", nil, fmt.Errorf("LLM stream request failed: %w", err)
	}

	client := makeStreamClient("")
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("LLM stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(body))
	}

	// Think-tag state machine (same pattern as agent/query.go)
	type thinkState int
	const (
		stateDetecting thinkState = iota
		stateThinking
		stateForwarding
	)
	state := stateDetecting
	var detectBuf strings.Builder
	var fullContent strings.Builder
	var usage *TokenUsage

	emitToken := func(text string) {
		if text == "" {
			return
		}
		switch state {
		case stateDetecting:
			detectBuf.WriteString(text)
			buf := detectBuf.String()
			if strings.HasPrefix(buf, "<think>") {
				state = stateThinking
				rest := buf[len("<think>"):]
				if rest != "" && onThinking != nil {
					onThinking(rest)
				}
			} else if len(buf) >= 7 || !strings.HasPrefix("<think>", buf) {
				state = stateForwarding
				fullContent.WriteString(buf)
				if onToken != nil {
					onToken(buf)
				}
			}
		case stateThinking:
			if idx := strings.Index(text, "</think>"); idx >= 0 {
				before := text[:idx]
				after := text[idx+len("</think>"):]
				if before != "" && onThinking != nil {
					onThinking(before)
				}
				state = stateForwarding
				if after != "" {
					after = strings.TrimLeft(after, "\n")
					fullContent.WriteString(after)
					if onToken != nil {
						onToken(after)
					}
				}
			} else {
				if onThinking != nil {
					onThinking(text)
				}
			}
		case stateForwarding:
			fullContent.WriteString(text)
			if onToken != nil {
				onToken(text)
			}
		}
	}

	isAnthropicVendor := IsAnthropic(vendor)

	// Parse SSE stream
	scanner := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	var lastEventType string
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			scanner = append(scanner, buf[:n]...)
			for {
				idx := bytes.IndexByte(scanner, '\n')
				if idx < 0 {
					break
				}
				line := string(scanner[:idx])
				scanner = scanner[idx+1:]
				line = strings.TrimSpace(line)

				// Track event type for Anthropic SSE format
				if strings.HasPrefix(line, "event: ") {
					lastEventType = strings.TrimSpace(line[7:])
					continue
				}

				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := line[6:]
				if data == "[DONE]" {
					continue
				}

				if isAnthropicVendor {
					// Anthropic SSE format
					content, done := ParseAnthropicStreamEvent(lastEventType, data)
					if content != "" {
						emitToken(content)
					}
					// Extract usage from message_delta event
					if lastEventType == "message_delta" {
						var evt M
						if json.Unmarshal([]byte(data), &evt) == nil {
							if u, ok := evt["usage"].(map[string]interface{}); ok {
								if usage == nil {
									usage = &TokenUsage{}
								}
								if v, ok := u["output_tokens"].(float64); ok {
									usage.CompletionTokens = int(v)
								}
							}
						}
					}
					// Extract input token count from message_start
					if lastEventType == "message_start" {
						var evt M
						if json.Unmarshal([]byte(data), &evt) == nil {
							if msg, ok := evt["message"].(map[string]interface{}); ok {
								if u, ok := msg["usage"].(map[string]interface{}); ok {
									if usage == nil {
										usage = &TokenUsage{}
									}
									if v, ok := u["input_tokens"].(float64); ok {
										usage.PromptTokens = int(v)
									}
								}
							}
						}
					}
					if done {
						if usage != nil {
							usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
						}
					}
					lastEventType = ""
					continue
				}

				// OpenAI-compatible SSE format
				var chunk M
				if json.Unmarshal([]byte(data), &chunk) != nil {
					continue
				}

				// Extract usage from final chunk
				if u, ok := chunk["usage"].(map[string]interface{}); ok {
					usage = &TokenUsage{}
					if v, ok := u["prompt_tokens"].(float64); ok {
						usage.PromptTokens = int(v)
					}
					if v, ok := u["completion_tokens"].(float64); ok {
						usage.CompletionTokens = int(v)
					}
					if v, ok := u["total_tokens"].(float64); ok {
						usage.TotalTokens = int(v)
					}
				}

				// Extract delta content
				if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
					if c, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := c["delta"].(map[string]interface{}); ok {
							if content, ok := delta["content"].(string); ok && content != "" {
								emitToken(content)
							}
							// Some models put thinking in reasoning_content
							if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
								if onThinking != nil {
									onThinking(rc)
								}
							}
						}
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	// Flush detecting buffer if still in detecting state
	if state == stateDetecting && detectBuf.Len() > 0 {
		buf := detectBuf.String()
		fullContent.WriteString(buf)
		if onToken != nil {
			onToken(buf)
		}
	}

	return strings.TrimSpace(fullContent.String()), usage, nil
}

// MaskApiKey masks an API key for display.
func MaskApiKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
