package llmclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

// ToolDef defines a tool in OpenAI function-calling format.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  M      `json:"parameters"` // JSON Schema
}

// ToolCallResult holds a parsed tool call from the LLM response.
type ToolCallResult struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// DoChatWithTools makes a non-streaming chat call with native tool definitions.
// Returns: content (if no tool call), tool calls (if tool call), usage, error.
// When the LLM decides to call a tool, content is empty and toolCalls is populated.
func DoChatWithTools(baseURL, apiKey string, chatBody M, tools []ToolDef, proxyURL, vendor string) (content string, toolCalls []ToolCallResult, usage *TokenUsage, err error) {
	// Build tools array in OpenAI format (will be converted for Anthropic in convertToAnthropicBody)
	var toolsPayload []M
	for _, t := range tools {
		toolsPayload = append(toolsPayload, M{
			"type": "function",
			"function": M{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	chatBody["tools"] = toolsPayload
	chatBody["_vendor"] = vendor

	raw, u, callErr := DoChatFullRaw(baseURL, apiKey, chatBody, proxyURL)
	if callErr != nil {
		return "", nil, nil, callErr
	}

	// Parse tool_calls from response — different format per vendor
	if IsAnthropic(vendor) {
		toolCalls = extractAnthropicToolUse(raw)
		if len(toolCalls) > 0 {
			return "", toolCalls, u, nil
		}
		return extractAnthropicContent(raw), nil, u, nil
	}

	// OpenAI format
	toolCalls = extractToolCalls(raw)
	if len(toolCalls) > 0 {
		return "", toolCalls, u, nil
	}

	// Fallback: check content for vendor-specific XML tool_call formats
	// (e.g. MiniMax: <minimax:tool_call><invoke name="...">...</invoke></minimax:tool_call>)
	content = ExtractChatContent(raw)
	if tc, ok := extractVendorXMLToolCall(content); ok {
		return "", []ToolCallResult{tc}, u, nil
	}
	// Final fallback: bare JSON tool call (no XML wrapper). Catches MiniMax
	// malformed hybrids like `minimax:tool_call {"name":...,"arguments":...} </function_call>`.
	if name, args, _, ok := extractBareToolCallJSON(content); ok {
		return "", []ToolCallResult{{ID: "bare-json-" + name, Name: name, Arguments: args}}, u, nil
	}
	return content, toolCalls, u, nil
}

// extractVendorXMLToolCall parses vendor-specific XML tool_call formats from content.
// Supports: <minimax:tool_call>, <tool_call>, and similar XML wrapper patterns.
// Uses regex for robustness against formatting variations (extra whitespace, single/double quotes).
//
// Two sub-formats are accepted:
//  1. Proper XML params  — <invoke name="X"><parameter name="Y">V</parameter>...</invoke>
//  2. Hybrid JSON body   — <invoke name="X"> { "arg1": ..., "arg2": ... } </function_call>
//
// Form (2) is emitted by MiniMax in practice: it opens with <invoke>, embeds the
// argument map as a bare JSON object (no <parameter> tags), and sometimes closes
// with </function_call> instead of </invoke>. Without handling this, the tool
// call dispatches with an empty argument map and downstream tools (smartquery,
// etc.) fail validation with confusing messages.
func extractVendorXMLToolCall(content string) (ToolCallResult, bool) {
	// Regex: <invoke name="funcName"> or <invoke name='funcName'> with flexible whitespace.
	invokeRe := regexp.MustCompile(`<invoke\s+name\s*=\s*["']([^"']+)["']\s*>`)
	invokeMatch := invokeRe.FindStringSubmatchIndex(content)
	if invokeMatch == nil {
		return ToolCallResult{}, false
	}
	funcName := content[invokeMatch[2]:invokeMatch[3]]
	if funcName == "" {
		return ToolCallResult{}, false
	}
	paramContent := content[invokeMatch[1]:]

	// Form 1: <parameter name="key">value</parameter> with flexible whitespace and quotes.
	paramRe := regexp.MustCompile(`(?s)<parameter\s+name\s*=\s*["']([^"']+)["']\s*>(.*?)</parameter>`)
	paramMatches := paramRe.FindAllStringSubmatch(paramContent, -1)

	args := map[string]interface{}{}
	for _, m := range paramMatches {
		paramName := m[1]
		paramValue := strings.TrimSpace(m[2])
		// Try parsing as JSON (arrays, objects, numbers).
		var parsed interface{}
		if json.Unmarshal([]byte(paramValue), &parsed) == nil {
			args[paramName] = parsed
		} else {
			args[paramName] = paramValue
		}
	}

	// Form 2 fallback: no <parameter> tags matched — scan for the first balanced
	// JSON object after <invoke> and treat it as the argument map directly.
	if len(args) == 0 {
		for i := 0; i < len(paramContent); i++ {
			if paramContent[i] != '{' {
				continue
			}
			endIdx := findMatchingBrace(paramContent, i)
			if endIdx < 0 {
				break // No balanced brace from here on.
			}
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(paramContent[i:endIdx+1]), &parsed) == nil {
				args = parsed
				break
			}
			// Skip past this open brace to try the next one if parsing failed.
			i = endIdx
		}
	}

	// Guard against silent success with empty args — that masks the actual
	// parse failure and surfaces as a misleading downstream tool-validation error.
	// Returning false here lets the outer fallbacks (extractBareToolCallJSON) try.
	if len(args) == 0 {
		return ToolCallResult{}, false
	}

	return ToolCallResult{
		ID:        "vendor-xml-" + funcName,
		Name:      funcName,
		Arguments: args,
	}, true
}

// DoChatFullRaw returns the raw response map (not just content).
func DoChatFullRaw(baseURL, apiKey string, chatBody M, proxyURL string) (M, *TokenUsage, error) {
	vendor, _ := chatBody["_vendor"].(string)
	delete(chatBody, "_vendor")

	// Per-baseURL rate limit — DoChatWithTools funnels through here, so this
	// is the choke-point for tool-call-heavy lakehouse agent traffic.
	waitLLMRate(baseURL)

	if IsAnthropic(vendor) {
		return doAnthropicChatRaw(baseURL, apiKey, chatBody, proxyURL)
	}

	// OpenAI-compatible path — same as DoChatFull but returns raw M
	NormalizeMaxTokens(chatBody)
	reqBytes, _ := json.Marshal(chatBody)
	// 300s overall cap: a non-streamed tool-calling round on a reasoning model
	// can take minutes. TTFT is bounded by the transport's ResponseHeaderTimeout.
	client := makeClient(300*time.Second, proxyURL)
	apiURL := BuildURL(baseURL, "/chat/completions")
	req, _ := http.NewRequest("POST", apiURL, bytes.NewReader(reqBytes))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(body))
	}

	var chatResp M
	json.NewDecoder(resp.Body).Decode(&chatResp)

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

	return chatResp, usage, nil
}

// extractToolCalls parses tool_calls from an OpenAI chat response.
func extractToolCalls(chatResp M) []ToolCallResult {
	choices, ok := chatResp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return nil
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return nil
	}
	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		return nil
	}

	rawCalls, ok := msg["tool_calls"].([]interface{})
	if !ok || len(rawCalls) == 0 {
		return nil
	}

	var results []ToolCallResult
	for _, rc := range rawCalls {
		call, ok := rc.(map[string]interface{})
		if !ok {
			continue
		}
		fn, ok := call["function"].(map[string]interface{})
		if !ok {
			continue
		}

		tc := ToolCallResult{
			ID:   fmt.Sprintf("%v", call["id"]),
			Name: fmt.Sprintf("%v", fn["name"]),
		}

		// Parse arguments — may be string or object
		switch args := fn["arguments"].(type) {
		case string:
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(args), &parsed) == nil {
				tc.Arguments = parsed
			}
		case map[string]interface{}:
			tc.Arguments = args
		}
		if tc.Arguments == nil {
			tc.Arguments = map[string]interface{}{}
		}

		results = append(results, tc)
	}
	return results
}

// BuildToolResultMessage creates a tool result message for the conversation.
// OpenAI format: role=tool, tool_call_id=id, content=result
func BuildToolResultMessage(toolCallID, result string) M {
	return M{
		"role":         "tool",
		"tool_call_id": toolCallID,
		"content":      result,
	}
}

// BuildAssistantToolCallMessage creates the assistant message that contains tool_calls.
// This is needed to maintain conversation history with the LLM.
func BuildAssistantToolCallMessage(toolCalls []ToolCallResult) M {
	var calls []M
	for _, tc := range toolCalls {
		argsJSON, _ := json.Marshal(tc.Arguments)
		calls = append(calls, M{
			"id":   tc.ID,
			"type": "function",
			"function": M{
				"name":      tc.Name,
				"arguments": string(argsJSON),
			},
		})
	}
	return M{
		"role":       "assistant",
		"content":    nil,
		"tool_calls": calls,
	}
}

// ExtractFunctionCallXML attempts to parse <function_call>...</function_call> from text.
// Returns the parsed name+arguments, the text before the tag, and whether it succeeded.
// This is the fallback for models that don't support native tool calling.
func ExtractFunctionCallXML(content string) (name string, arguments map[string]interface{}, textBefore string, ok bool) {
	// Normalize: strip markdown code fences that may wrap the tag
	normalized := content
	normalized = strings.ReplaceAll(normalized, "```xml\n", "")
	normalized = strings.ReplaceAll(normalized, "```json\n", "")
	normalized = strings.ReplaceAll(normalized, "```\n", "")
	normalized = strings.ReplaceAll(normalized, "\n```", "")
	normalized = strings.ReplaceAll(normalized, "```", "")

	// Try vendor-specific XML formats first (e.g. MiniMax <minimax:tool_call>)
	if tc, found := extractVendorXMLToolCall(normalized); found {
		// Find the text before the vendor XML tag
		for _, tag := range []string{"<minimax:tool_call>", "<tool_call>", "<invoke "} {
			if i := strings.Index(normalized, tag); i >= 0 {
				return tc.Name, tc.Arguments, strings.TrimSpace(normalized[:i]), true
			}
		}
		return tc.Name, tc.Arguments, "", true
	}

	idx := strings.Index(normalized, "<function_call>")
	if idx < 0 {
		// Try case-insensitive
		lower := strings.ToLower(normalized)
		idx = strings.Index(lower, "<function_call>")
		if idx < 0 {
			// Kimi K2 / Qwen multi-tool batch format. When the model decides
			// to fire several tools at once it wraps each call in
			//   <|tool_call_begin|>functions.NAME:N<|tool_call_argument_begin|>{...}<|tool_call_end|>
			// inside an outer <|tool_calls_section_begin|>...<|tool_calls_section_end|>.
			// We extract the FIRST call only — the agent loop iterates so the
			// remaining batched calls happen on the next turn. Single-tool
			// emission (which K2 also uses sometimes) hits the same path.
			if name, args, before, found := extractKimiK2ToolCall(normalized); found {
				return name, args, before, true
			}
			// Final fallback: bare JSON tool call without any opening wrapper.
			// Catches MiniMax-style malformed hybrids such as
			//   `minimax:tool_call {"name":"smartquery","arguments":{...}} </function_call>`
			// and plain JSON `{"name":"...","arguments":{...}}` outputs.
			if name, args, before, found := extractBareToolCallJSON(normalized); found {
				return name, args, before, true
			}
			return "", nil, content, false
		}
	}

	endTag := "</function_call>"
	endIdx := strings.Index(normalized[idx:], endTag)
	if endIdx < 0 {
		// Try without closing tag — maybe LLM stopped after JSON
		// Extract from <function_call> to end
		fcJSON := strings.TrimSpace(normalized[idx+len("<function_call>"):])
		fcJSON = strings.TrimSuffix(fcJSON, "</")
		textBefore = strings.TrimSpace(normalized[:idx])
		return parseFCJSON(fcJSON, textBefore)
	}

	endIdx += idx // absolute position
	fcJSON := strings.TrimSpace(normalized[idx+len("<function_call>") : endIdx])
	textBefore = strings.TrimSpace(normalized[:idx])
	return parseFCJSON(fcJSON, textBefore)
}

// extractKimiK2ToolCall parses the Kimi K2 / Qwen native tool call format:
//
//	<|tool_calls_section_begin|>
//	  <|tool_call_begin|>functions.NAME:INDEX<|tool_call_argument_begin|>
//	  {"args":...}
//	  <|tool_call_end|>
//	  ... (zero or more additional calls) ...
//	<|tool_calls_section_end|>
//
// The model uses this when it decides to batch multiple tool invocations into
// a single assistant message. Our agent loop is single-tool-per-turn, so we
// extract only the FIRST call and let the loop iterate for any remaining
// calls (the model will re-emit them in the next turn anyway).
//
// Returns name with the leading "functions." prefix and trailing ":N" index
// suffix stripped. textBefore is everything preceding the outer section
// marker (or the first <|tool_call_begin|> when no section wrapper exists).
func extractKimiK2ToolCall(content string) (name string, arguments map[string]interface{}, textBefore string, ok bool) {
	const (
		callBegin = "<|tool_call_begin|>"
		argBegin  = "<|tool_call_argument_begin|>"
		callEnd   = "<|tool_call_end|>"
		secBegin  = "<|tool_calls_section_begin|>"
	)
	bi := strings.Index(content, callBegin)
	if bi < 0 {
		return "", nil, "", false
	}
	nameStart := bi + len(callBegin)
	ai := strings.Index(content[nameStart:], argBegin)
	if ai < 0 {
		return "", nil, "", false
	}
	rawName := strings.TrimSpace(content[nameStart : nameStart+ai])
	// Strip "functions." prefix (Kimi convention).
	rawName = strings.TrimPrefix(rawName, "functions.")
	// Strip trailing ":N" call index when N is purely digits.
	if colon := strings.LastIndex(rawName, ":"); colon >= 0 {
		if _, err := strconv.Atoi(rawName[colon+1:]); err == nil {
			rawName = rawName[:colon]
		}
	}
	if rawName == "" {
		return "", nil, "", false
	}

	argsStart := nameStart + ai + len(argBegin)
	var argsJSON string
	if ei := strings.Index(content[argsStart:], callEnd); ei >= 0 {
		argsJSON = strings.TrimSpace(content[argsStart : argsStart+ei])
	} else {
		// No close tag — model output truncated. Try to take a balanced JSON
		// block so we still salvage one tool call.
		rest := strings.TrimSpace(content[argsStart:])
		if len(rest) > 0 && rest[0] == '{' {
			if endIdx := findMatchingBrace(rest, 0); endIdx > 0 {
				argsJSON = rest[:endIdx+1]
			} else {
				argsJSON = rest
			}
		} else {
			argsJSON = rest
		}
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", nil, "", false
	}
	if args == nil {
		args = map[string]interface{}{}
	}

	if si := strings.Index(content, secBegin); si >= 0 && si < bi {
		textBefore = strings.TrimSpace(content[:si])
	} else {
		textBefore = strings.TrimSpace(content[:bi])
	}
	return rawName, args, textBefore, true
}

// extractBareToolCallJSON scans content for a JSON object whose top-level keys
// include both `name` (non-empty string) and `arguments` (object). Catches
// vendor-specific malformed outputs that mix wrappers, e.g. MiniMax emitting
//
//	minimax:tool_call {"name":"smartquery","arguments":{...}} </function_call>
//
// where neither `<function_call>` nor `<minimax:tool_call>` opening tags exist.
// Returns the parsed call plus any text preceding the matched JSON object.
func extractBareToolCallJSON(content string) (name string, arguments map[string]interface{}, textBefore string, ok bool) {
	for i := 0; i < len(content); i++ {
		if content[i] != '{' {
			continue
		}
		endIdx := findMatchingBrace(content, i)
		if endIdx < 0 {
			break // Unbalanced from here on — no point scanning further opens.
		}
		candidate := content[i : endIdx+1]
		// Cheap pre-filter: must have both literal "name" and "arguments" keys.
		if !strings.Contains(candidate, `"name"`) || !strings.Contains(candidate, `"arguments"`) {
			continue
		}
		var fc struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(candidate), &fc); err != nil || fc.Name == "" {
			continue
		}
		if fc.Arguments == nil {
			fc.Arguments = map[string]interface{}{}
		}
		return fc.Name, fc.Arguments, strings.TrimSpace(content[:i]), true
	}
	return "", nil, "", false
}

// findMatchingBrace returns the index of the '}' that closes the '{' at openIdx,
// respecting JSON string literals (with backslash escapes). Returns -1 on imbalance.
func findMatchingBrace(s string, openIdx int) int {
	depth := 0
	inStr := false
	escape := false
	for i := openIdx; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if inStr {
			switch c {
			case '\\':
				escape = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func parseFCJSON(fcJSON, textBefore string) (string, map[string]interface{}, string, bool) {
	var fc struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}

	tryParse := func(s string) bool {
		return json.Unmarshal([]byte(s), &fc) == nil && fc.Name != ""
	}

	// 1. Direct unmarshal
	if tryParse(fcJSON) {
		return fc.Name, fc.Arguments, textBefore, true
	}

	// 2. Fix common LLM JSON issues: trailing commas, single quotes
	fixed := strings.ReplaceAll(fcJSON, "'", "\"")
	for _, pair := range []struct{ a, b string }{{",}", "}"}, {",]", "]"}} {
		fixed = strings.ReplaceAll(fixed, pair.a, pair.b)
	}
	if tryParse(fixed) {
		return fc.Name, fc.Arguments, textBefore, true
	}

	// 3. Strip excess trailing } — LLM sometimes emits extra closing braces (e.g. "}}}").
	// Try stripping all trailing } then adding back 1..5 to find the correct count.
	base := strings.TrimRight(strings.TrimSpace(fixed), "}")
	for nb := 1; nb <= 5; nb++ {
		candidate := base + strings.Repeat("}", nb)
		if tryParse(candidate) {
			return fc.Name, fc.Arguments, textBefore, true
		}
	}

	return "", nil, textBefore, false
}
