package llmclient

import (
	"context"
	"testing"

	. "github.com/lakehouse2ontology/httputil"
)

func TestFixtureServesScriptedToolCalls(t *testing.T) {
	fix := NewFixtureClient([]ToolCallScript{
		{ToolName: "lookup_od", Args: map[string]interface{}{"q": "revenue"}},
		{ToolName: "commit_card", Args: map[string]interface{}{"name": "FIXTURE_LLM_DETERMINISTIC_abc"}},
		{FinalText: "all done"},
	})

	ctx := context.Background()
	body := M{"messages": []M{{"role": "user", "content": "hi"}}}

	// Turn 1: lookup_od tool call.
	content, calls, _, err := fix.DoChatWithTools(ctx, "", "", body, nil, "", "")
	if err != nil {
		t.Fatalf("turn 1 err: %v", err)
	}
	if content != "" || len(calls) != 1 || calls[0].Name != "lookup_od" {
		t.Fatalf("turn 1 unexpected: content=%q calls=%+v", content, calls)
	}
	if got, _ := calls[0].Arguments["q"].(string); got != "revenue" {
		t.Fatalf("turn 1 args lost: %+v", calls[0].Arguments)
	}

	// Turn 2: commit_card tool call carrying the AC-11 marker prefix.
	_, calls, _, err = fix.DoChatWithTools(ctx, "", "", body, nil, "", "")
	if err != nil {
		t.Fatalf("turn 2 err: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "commit_card" {
		t.Fatalf("turn 2 unexpected: %+v", calls)
	}
	name, _ := calls[0].Arguments["name"].(string)
	if name != "FIXTURE_LLM_DETERMINISTIC_abc" {
		t.Fatalf("turn 2 marker missing: %q", name)
	}

	// Turn 3: final text, no tool call.
	content, calls, _, err = fix.DoChatWithTools(ctx, "", "", body, nil, "", "")
	if err != nil {
		t.Fatalf("turn 3 err: %v", err)
	}
	if content != "all done" || len(calls) != 0 {
		t.Fatalf("turn 3 unexpected: content=%q calls=%+v", content, calls)
	}

	if fix.ToolCallsServed != 3 {
		t.Fatalf("ToolCallsServed = %d, want 3", fix.ToolCallsServed)
	}
	if fix.Remaining() != 0 {
		t.Fatalf("Remaining = %d, want 0", fix.Remaining())
	}
}

func TestFixtureExhaustionReturnsEmpty(t *testing.T) {
	fix := NewFixtureClient(nil)
	content, calls, _, err := fix.DoChatWithTools(context.Background(), "", "", M{}, nil, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if content != "" || len(calls) != 0 {
		t.Fatalf("exhausted fixture should return empty: content=%q calls=%+v", content, calls)
	}
	if fix.ToolCallsServed != 1 {
		t.Fatalf("ToolCallsServed = %d, want 1 (call still counted)", fix.ToolCallsServed)
	}
}

func TestLiveClientImplementsInterface(t *testing.T) {
	// Compile-time check: *LiveClient and *Fixture both satisfy Client.
	var _ Client = NewLiveClient()
	var _ Client = NewFixtureClient(nil)
}
