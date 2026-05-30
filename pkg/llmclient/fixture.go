package llmclient

// Step 0.5 finding (planner seam verification):
//
// pkg/llmclient exposes a procedural API (DoChatFullCtx, DoChatWithToolsCtx,
// DoChatStreamCallbackCtx) rather than an interface. The seam the
// agent-server explore handler needs is the tool-call call: given a chat
// body + tool definitions, return either tool_calls or final content.
//
// This file introduces an additive `Client` interface that wraps the
// existing DoChatWithToolsCtx surface. Production code can keep calling
// the package functions directly, while tests (TestLLMConvergence in
// services/agent-server/handler/handler_agent_explore_test.go) can inject
// a Fixture implementation that replays a canned tool-call script and
// exposes ToolCallsServed as a stub-bypass detector (AC-11 hardening).
//
// Reference: .omc/plans/plan-explore-chat-redesign-final.md Step 0.5 + 4.5.

import (
	"context"
	"sync"

	. "github.com/lakehouse2ontology/httputil"
)

// Client is the minimum tool-calling seam the agent-server explore handler
// consumes. It is additive — existing callers of DoChatWithToolsCtx are
// unaffected. A small adapter (LiveClient) wraps the procedural function
// so production code can pass a *LiveClient where a Client is needed.
type Client interface {
	// DoChatWithTools sends a tool-enabled chat completion.
	// Returns (content, toolCalls, usage, error) — same shape as the
	// procedural DoChatWithToolsCtx.
	DoChatWithTools(
		ctx context.Context,
		baseURL, apiKey string,
		chatBody M,
		tools []ToolDef,
		proxyURL, vendor string,
	) (content string, toolCalls []ToolCallResult, usage *TokenUsage, err error)
}

// LiveClient is the production Client backed by the procedural
// DoChatWithToolsCtx function. Zero-config: NewLiveClient() returns a
// ready-to-use Client.
type LiveClient struct{}

// NewLiveClient returns the production Client.
func NewLiveClient() Client { return &LiveClient{} }

// DoChatWithTools delegates to the package-level DoChatWithToolsCtx.
func (l *LiveClient) DoChatWithTools(
	ctx context.Context,
	baseURL, apiKey string,
	chatBody M,
	tools []ToolDef,
	proxyURL, vendor string,
) (string, []ToolCallResult, *TokenUsage, error) {
	return DoChatWithToolsCtx(ctx, baseURL, apiKey, chatBody, tools, proxyURL, vendor)
}

// ToolCallScript is one canned LLM turn output for the Fixture client.
// If ToolName is non-empty, the fixture serves a tool_call with the
// given name + args. If ToolName is empty, FinalText is returned as the
// assistant's final content (no tool_call).
type ToolCallScript struct {
	ToolName  string
	Args      map[string]interface{}
	FinalText string
}

// Fixture is a deterministic LLM client for tests. It replays a canned
// script of tool-call responses and counts invocations to defeat
// stub-bypass gaming (AC-11 in plan-explore-chat-redesign-final.md).
//
// ToolCallsServed is exported and updated atomically under a mutex so
// tests can assert (a) the dispatcher was actually invoked
// (ToolCallsServed > 0) and (b) the payload carried the fixture marker
// prefix "FIXTURE_LLM_DETERMINISTIC_" — together these prove the LLM
// loop was driven by the fixture rather than bypassed.
type Fixture struct {
	mu              sync.Mutex
	script          []ToolCallScript
	cursor          int
	ToolCallsServed int
}

// NewFixtureClient returns a Client backed by the supplied script. Each
// call to DoChatWithTools pops the next script entry; when the script is
// exhausted the fixture returns a final empty assistant text.
func NewFixtureClient(script []ToolCallScript) *Fixture {
	cp := make([]ToolCallScript, len(script))
	copy(cp, script)
	return &Fixture{script: cp}
}

// DoChatWithTools serves the next canned response from the script.
// Increments ToolCallsServed on every invocation (whether tool_call or
// final text) so AC-11 (b) can assert the dispatcher reached the seam.
func (f *Fixture) DoChatWithTools(
	_ context.Context,
	_, _ string,
	_ M,
	_ []ToolDef,
	_, _ string,
) (string, []ToolCallResult, *TokenUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ToolCallsServed++

	if f.cursor >= len(f.script) {
		// Script exhausted — return empty final content so the loop
		// terminates gracefully rather than spinning.
		return "", nil, nil, nil
	}
	step := f.script[f.cursor]
	f.cursor++

	if step.ToolName == "" {
		return step.FinalText, nil, nil, nil
	}
	args := step.Args
	if args == nil {
		args = map[string]interface{}{}
	}
	tc := ToolCallResult{
		ID:        "fixture-" + step.ToolName,
		Name:      step.ToolName,
		Arguments: args,
	}
	return "", []ToolCallResult{tc}, nil, nil
}

// Remaining returns how many script entries have not yet been served.
// Useful for tests that want to assert the entire script was consumed.
func (f *Fixture) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cursor >= len(f.script) {
		return 0
	}
	return len(f.script) - f.cursor
}
