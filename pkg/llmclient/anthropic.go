package llmclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

const anthropicVersion = "2023-06-01"

// IsAnthropic returns true if the vendor string indicates the Anthropic (Claude) API.
func IsAnthropic(vendor string) bool {
	return strings.ToLower(vendor) == "anthropic"
}

// doAnthropicChatRaw makes a non-streaming call to the Anthropic Messages API
// and returns the raw response map for tool_use extraction.
func doAnthropicChatRaw(baseURL, apiKey string, chatBody M, proxyURL string) (M, *TokenUsage, error) {
	anthropicBody := convertToAnthropicBody(chatBody)
	reqBytes, _ := json.Marshal(anthropicBody)
	logLen := len(reqBytes)
	if logLen > 2000 {
		logLen = 2000
	}
	log.Printf("ANTHROPIC request: %s", string(reqBytes[:logLen]))

	client := makeClient(120*time.Second, proxyURL)
	url := BuildURL(baseURL, "/messages")

	req, _ := NewAnthropicHTTPRequest(url, apiKey, reqBytes)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("Anthropic request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("ANTHROPIC error %d: %s", resp.StatusCode, string(body))
		return nil, nil, fmt.Errorf("Anthropic returned %d: %s", resp.StatusCode, string(body))
	}

	var anthropicResp M
	json.NewDecoder(resp.Body).Decode(&anthropicResp)

	// Log stop_reason and content types for debugging
	if sr, ok := anthropicResp["stop_reason"].(string); ok {
		log.Printf("ANTHROPIC response: stop_reason=%s", sr)
	}

	usage := extractAnthropicUsage(anthropicResp)
	return anthropicResp, usage, nil
}

// doAnthropicChat is the simple text-only wrapper (no tool support).
func doAnthropicChat(baseURL, apiKey string, chatBody M, proxyURL string) (string, *TokenUsage, error) {
	raw, usage, err := doAnthropicChatRaw(baseURL, apiKey, chatBody, proxyURL)
	if err != nil {
		return "", nil, err
	}
	return extractAnthropicContent(raw), usage, nil
}

// convertToAnthropicBody transforms an OpenAI-style chat body to Anthropic Messages format.
// Key differences:
//   - system prompt is a top-level field, not in the messages array
//   - tools use "input_schema" instead of "parameters"
//   - tool results use content blocks with type "tool_result"
func convertToAnthropicBody(chatBody M) M {
	model, _ := chatBody["model"].(string)
	maxTokens := 4096
	if mt, ok := chatBody["max_tokens"].(int); ok {
		maxTokens = mt
	} else if mt, ok := chatBody["max_tokens"].(float64); ok {
		maxTokens = int(mt)
	}

	body := M{
		"model":      model,
		"max_tokens": maxTokens,
	}

	// Extract system prompt from messages and separate it
	var system string
	var messages []M

	extractMessages := func(rawMsgs []interface{}) {
		for _, rm := range rawMsgs {
			msg, ok := rm.(map[string]interface{})
			if !ok {
				continue
			}
			role := fmt.Sprintf("%v", msg["role"])
			if role == "system" {
				system += fmt.Sprintf("%v", msg["content"]) + "\n"
				continue
			}
			// Pass through messages with complex content (tool_result blocks) as-is
			if role == "tool" {
				// Convert OpenAI tool result → Anthropic tool_result content block
				toolCallID, _ := msg["tool_call_id"].(string)
				content := fmt.Sprintf("%v", msg["content"])
				messages = append(messages, M{"role": "user", "content": []M{
					{"type": "tool_result", "tool_use_id": toolCallID, "content": content},
				}})
				continue
			}
			// Check if this is an assistant message with tool_calls (OpenAI format)
			if role == "assistant" {
				if toolCalls, ok := msg["tool_calls"].([]M); ok && len(toolCalls) > 0 {
					// Convert to Anthropic format: content blocks with type "tool_use"
					var contentBlocks []M
					// Add text content if present
					if text, ok := msg["content"].(string); ok && text != "" {
						contentBlocks = append(contentBlocks, M{"type": "text", "text": text})
					}
					for _, tc := range toolCalls {
						fn, _ := tc["function"].(M)
						if fn == nil {
							if fnRaw, ok := tc["function"].(map[string]interface{}); ok {
								fn = M(fnRaw)
							}
						}
						argsStr, _ := fn["arguments"].(string)
						var argsObj interface{}
						if json.Unmarshal([]byte(argsStr), &argsObj) != nil {
							argsObj = M{}
						}
						contentBlocks = append(contentBlocks, M{
							"type": "tool_use", "id": tc["id"], "name": fn["name"], "input": argsObj,
						})
					}
					messages = append(messages, M{"role": "assistant", "content": contentBlocks})
					continue
				}
				// Also handle []interface{} for tool_calls
				if toolCalls, ok := msg["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
					var contentBlocks []M
					if text, ok := msg["content"].(string); ok && text != "" {
						contentBlocks = append(contentBlocks, M{"type": "text", "text": text})
					}
					for _, tcRaw := range toolCalls {
						tc, _ := tcRaw.(map[string]interface{})
						fn, _ := tc["function"].(map[string]interface{})
						argsStr, _ := fn["arguments"].(string)
						var argsObj interface{}
						if json.Unmarshal([]byte(argsStr), &argsObj) != nil {
							argsObj = M{}
						}
						contentBlocks = append(contentBlocks, M{
							"type": "tool_use", "id": tc["id"], "name": fn["name"], "input": argsObj,
						})
					}
					messages = append(messages, M{"role": "assistant", "content": contentBlocks})
					continue
				}
			}
			// Regular message — preserve content as-is (may be string or array)
			messages = append(messages, M{"role": role, "content": msg["content"]})
		}
	}

	if rawMsgs, ok := chatBody["messages"].([]M); ok {
		asIface := make([]interface{}, len(rawMsgs))
		for i, m := range rawMsgs {
			asIface[i] = map[string]interface{}(m)
		}
		extractMessages(asIface)
	} else if rawMsgs, ok := chatBody["messages"].([]interface{}); ok {
		extractMessages(rawMsgs)
	}

	if system != "" {
		body["system"] = strings.TrimSpace(system)
	}
	if len(messages) > 0 {
		body["messages"] = messages
	}

	// Convert OpenAI tools format → Anthropic tools format
	if tools, ok := chatBody["tools"]; ok {
		body["tools"] = convertToolsToAnthropic(tools)
	}

	if temp, ok := chatBody["temperature"]; ok {
		body["temperature"] = temp
	}
	if stream, ok := chatBody["stream"].(bool); ok && stream {
		body["stream"] = true
	}

	return body
}

// convertToolsToAnthropic converts OpenAI tools format to Anthropic tools format.
// OpenAI: [{"type":"function","function":{"name","description","parameters"}}]
// Anthropic: [{"name","description","input_schema"}]
func convertToolsToAnthropic(tools interface{}) []M {
	var result []M
	var toolArr []interface{}
	switch t := tools.(type) {
	case []M:
		for _, m := range t {
			toolArr = append(toolArr, map[string]interface{}(m))
		}
	case []interface{}:
		toolArr = t
	default:
		return nil
	}

	for _, rawTool := range toolArr {
		tool, ok := rawTool.(map[string]interface{})
		if !ok {
			continue
		}
		fn, _ := tool["function"].(map[string]interface{})
		if fn == nil {
			continue
		}
		result = append(result, M{
			"name":         fn["name"],
			"description":  fn["description"],
			"input_schema": fn["parameters"],
		})
	}
	return result
}

// NewAnthropicHTTPRequest creates an HTTP POST request with Anthropic-specific headers.
func NewAnthropicHTTPRequest(url, apiKey string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	return req, nil
}

// extractAnthropicContent extracts only the text content from an Anthropic Messages response.
func extractAnthropicContent(resp M) string {
	contentArr, ok := resp["content"].([]interface{})
	if !ok || len(contentArr) == 0 {
		return ""
	}
	var parts []string
	for _, item := range contentArr {
		if block, ok := item.(map[string]interface{}); ok {
			if blockType, _ := block["type"].(string); blockType == "text" {
				if text, ok := block["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "")
}

// extractAnthropicToolUse extracts tool_use blocks from an Anthropic response.
// Returns them as ToolCallResult compatible with the OpenAI format.
func extractAnthropicToolUse(resp M) []ToolCallResult {
	contentArr, ok := resp["content"].([]interface{})
	if !ok {
		return nil
	}
	var results []ToolCallResult
	for _, item := range contentArr {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if blockType, _ := block["type"].(string); blockType == "tool_use" {
			tc := ToolCallResult{
				ID:   fmt.Sprintf("%v", block["id"]),
				Name: fmt.Sprintf("%v", block["name"]),
			}
			if input, ok := block["input"].(map[string]interface{}); ok {
				tc.Arguments = input
			} else {
				tc.Arguments = map[string]interface{}{}
			}
			results = append(results, tc)
		}
	}
	return results
}

// extractAnthropicUsage maps Anthropic usage fields to TokenUsage.
// Anthropic uses input_tokens/output_tokens instead of prompt_tokens/completion_tokens.
func extractAnthropicUsage(resp M) *TokenUsage {
	u, ok := resp["usage"].(map[string]interface{})
	if !ok {
		return nil
	}
	usage := &TokenUsage{}
	if v, ok := u["input_tokens"].(float64); ok {
		usage.PromptTokens = int(v)
	}
	if v, ok := u["output_tokens"].(float64); ok {
		usage.CompletionTokens = int(v)
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return usage
}

// ParseAnthropicStreamEvent parses a single SSE event from the Anthropic streaming API.
// Returns the text delta content and whether the stream is done.
//
// Anthropic SSE format uses typed events:
//
//	event: content_block_delta
//	data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}
//
//	event: message_stop
//	data: {"type":"message_stop"}
func ParseAnthropicStreamEvent(eventType, data string) (content string, done bool) {
	if data == "" {
		return "", false
	}

	var evt M
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return "", false
	}

	msgType, _ := evt["type"].(string)
	switch msgType {
	case "content_block_delta":
		if delta, ok := evt["delta"].(map[string]interface{}); ok {
			if text, ok := delta["text"].(string); ok {
				return text, false
			}
		}
	case "message_stop":
		return "", true
	case "message_delta":
		// May contain stop_reason and usage — signal done if stop_reason present
		if delta, ok := evt["delta"].(map[string]interface{}); ok {
			if _, ok := delta["stop_reason"].(string); ok {
				return "", false // not done yet, message_stop follows
			}
		}
	}
	return "", false
}

// PrepareStreamRequest constructs an HTTP request for streaming, handling both
// OpenAI and Anthropic protocols based on vendor.
func PrepareStreamRequest(baseURL, apiKey, vendor string, chatBody M) (*http.Request, error) {
	if IsAnthropic(vendor) {
		anthropicBody := convertToAnthropicBody(chatBody)
		anthropicBody["stream"] = true
		reqBytes, _ := json.Marshal(anthropicBody)
		return NewAnthropicHTTPRequest(BuildURL(baseURL, "/messages"), apiKey, reqBytes)
	}

	// OpenAI-compatible
	NormalizeMaxTokens(chatBody)
	reqBytes, _ := json.Marshal(chatBody)
	url := BuildURL(baseURL, "/chat/completions")
	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return req, nil
}
