package llmclient

import (
	"testing"
)

// TestExtractFunctionCallXML_KimiK2Single covers the format Kimi K2 emits when
// firing a single tool call. Real capture from a builder thread that stalled
// because this format wasn't recognised.
func TestExtractFunctionCallXML_KimiK2Single(t *testing.T) {
	input := `<|tool_calls_section_begin|><|tool_call_begin|>functions.inspect:3<|tool_call_argument_begin|>{"mode": "schema", "table": "Products", "tableName": "Products"}<|tool_call_end|><|tool_calls_section_end|>`
	name, args, before, ok := ExtractFunctionCallXML(input)
	if !ok {
		t.Fatal("expected K2 single-call to parse")
	}
	if name != "inspect" {
		t.Errorf("expected name=inspect (functions. prefix + :N suffix stripped), got %q", name)
	}
	if args["mode"] != "schema" || args["table"] != "Products" {
		t.Errorf("expected parsed args, got %+v", args)
	}
	if before != "" {
		t.Errorf("expected empty textBefore, got %q", before)
	}
}

// TestExtractFunctionCallXML_KimiK2Batch ensures we extract the FIRST tool
// call from a batch — the agent loop iterates so subsequent calls happen on
// later turns.
func TestExtractFunctionCallXML_KimiK2Batch(t *testing.T) {
	input := `<|tool_calls_section_begin|>` +
		`<|tool_call_begin|>functions.inspect:3<|tool_call_argument_begin|>{"mode":"schema","table":"Products"}<|tool_call_end|>` +
		`<|tool_call_begin|>functions.inspect:4<|tool_call_argument_begin|>{"mode":"schema","table":"Customers"}<|tool_call_end|>` +
		`<|tool_call_begin|>functions.inspect:5<|tool_call_argument_begin|>{"mode":"schema","table":"Employees"}<|tool_call_end|>` +
		`<|tool_calls_section_end|>`
	name, args, _, ok := ExtractFunctionCallXML(input)
	if !ok {
		t.Fatal("expected batch to parse first call")
	}
	if name != "inspect" || args["table"] != "Products" {
		t.Errorf("expected first call (Products), got name=%q args=%+v", name, args)
	}
}

// TestExtractFunctionCallXML_KimiK2NoSection accepts the same per-call markers
// when the outer section wrapper is absent (some K2 variants).
func TestExtractFunctionCallXML_KimiK2NoSection(t *testing.T) {
	input := `<|tool_call_begin|>functions.smartquery:1<|tool_call_argument_begin|>{"intent":"Sales.Total","params":{}}<|tool_call_end|>`
	name, args, _, ok := ExtractFunctionCallXML(input)
	if !ok {
		t.Fatal("expected K2 no-section to parse")
	}
	if name != "smartquery" {
		t.Errorf("expected smartquery, got %q", name)
	}
	if args["intent"] != "Sales.Total" {
		t.Errorf("expected intent=Sales.Total, got %v", args)
	}
}

// TestExtractFunctionCallXML_KimiK2Truncated handles model output that gets
// cut off mid-stream (no closing <|tool_call_end|>). We salvage the JSON
// block via balanced-brace matching.
func TestExtractFunctionCallXML_KimiK2Truncated(t *testing.T) {
	input := `<|tool_calls_section_begin|><|tool_call_begin|>functions.inspect:1<|tool_call_argument_begin|>{"mode":"schema","table":"Orders"}`
	name, args, _, ok := ExtractFunctionCallXML(input)
	if !ok {
		t.Fatal("expected truncated K2 to salvage")
	}
	if name != "inspect" || args["table"] != "Orders" {
		t.Errorf("expected (inspect, Orders), got name=%q args=%+v", name, args)
	}
}

// TestExtractFunctionCallXML_KimiK2WithLeadingText preserves textBefore when
// the model prepended commentary before opening the tool section.
func TestExtractFunctionCallXML_KimiK2WithLeadingText(t *testing.T) {
	input := `Let me check the schema. <|tool_calls_section_begin|><|tool_call_begin|>functions.inspect:1<|tool_call_argument_begin|>{"mode":"schema","table":"X"}<|tool_call_end|><|tool_calls_section_end|>`
	name, _, before, ok := ExtractFunctionCallXML(input)
	if !ok {
		t.Fatal("expected to parse")
	}
	if name != "inspect" {
		t.Errorf("name mismatch: %q", name)
	}
	if before != "Let me check the schema." {
		t.Errorf("unexpected textBefore: %q", before)
	}
}

// TestExtractFunctionCallXML_StandardFormatStillWins ensures the K2 branch
// doesn't shadow the existing <function_call> wrapper format.
func TestExtractFunctionCallXML_StandardFormatStillWins(t *testing.T) {
	input := `<function_call>{"name":"smartquery","arguments":{"intent":"X"}}</function_call>`
	name, args, _, ok := ExtractFunctionCallXML(input)
	if !ok {
		t.Fatal("expected to parse")
	}
	if name != "smartquery" || args["intent"] != "X" {
		t.Errorf("standard wrapper regressed: name=%q args=%+v", name, args)
	}
}
