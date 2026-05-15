package llmclient

import (
	"reflect"
	"testing"
)

// TestExtractFunctionCallXML_MiniMaxHybrid covers the exact malformed format
// observed on /lakehouse/ontology/lakehouse-agent: MiniMax outputs
//
//	`minimax:tool_call {"name":"...","arguments":{...}} </function_call>`
//
// with no opening <function_call> or <minimax:tool_call><invoke> wrapper.
func TestExtractFunctionCallXML_MiniMaxHybrid(t *testing.T) {
	input := `minimax:tool_call {"name":"smartquery","arguments":{"objects":["order"],"metric":"Order_Quantity","filters":[{"prop":"Product_Name","op":"=","value":"YGBOOK9 14IAH10","fuzzyMatch":true},{"prop":"Order Type","op":"=","value":"Real Order"}],"displayMode":"table"}} </function_call>`

	name, args, _, ok := ExtractFunctionCallXML(input)
	if !ok {
		t.Fatalf("expected ok=true for MiniMax hybrid format, got false (name=%q args=%v)", name, args)
	}
	if name != "smartquery" {
		t.Errorf("name: got %q want %q", name, "smartquery")
	}
	if got, want := args["metric"], "Order_Quantity"; got != want {
		t.Errorf("args.metric: got %v want %v", got, want)
	}
	objs, _ := args["objects"].([]interface{})
	if len(objs) != 1 || objs[0] != "order" {
		t.Errorf("args.objects: got %v want [order]", objs)
	}
	filters, _ := args["filters"].([]interface{})
	if len(filters) != 2 {
		t.Fatalf("args.filters length: got %d want 2", len(filters))
	}
	f0 := filters[0].(map[string]interface{})
	if f0["prop"] != "Product_Name" || f0["value"] != "YGBOOK9 14IAH10" || f0["fuzzyMatch"] != true {
		t.Errorf("filters[0]: got %v", f0)
	}
}

func TestExtractFunctionCallXML_BarePlainJSON(t *testing.T) {
	// Pure JSON, no wrappers at all.
	input := `{"name":"lookup","arguments":{"keyword":["接单"]}}`
	name, args, _, ok := ExtractFunctionCallXML(input)
	if !ok || name != "lookup" {
		t.Fatalf("expected lookup, got name=%q ok=%v", name, ok)
	}
	kw, _ := args["keyword"].([]interface{})
	if len(kw) != 1 || kw[0] != "接单" {
		t.Errorf("keyword: got %v", kw)
	}
}

func TestExtractFunctionCallXML_StandardWrapperStillWorks(t *testing.T) {
	// Make sure the new fallback didn't break the original <function_call> path.
	input := `some prelude <function_call>{"name":"lookup","arguments":{"ontology_name":["Order"]}}</function_call> trailing`
	name, args, before, ok := ExtractFunctionCallXML(input)
	if !ok || name != "lookup" {
		t.Fatalf("expected lookup, got name=%q ok=%v", name, ok)
	}
	if before != "some prelude" {
		t.Errorf("textBefore: got %q want %q", before, "some prelude")
	}
	on, _ := args["ontology_name"].([]interface{})
	if len(on) != 1 || on[0] != "Order" {
		t.Errorf("ontology_name: got %v", on)
	}
}

func TestExtractFunctionCallXML_VendorMinimaxXMLStillWorks(t *testing.T) {
	// Make sure existing <minimax:tool_call><invoke> path still works.
	input := `<minimax:tool_call><invoke name="smartquery"><parameter name="objects">["Order"]</parameter><parameter name="metric">sum(Order_Quantity)</parameter></invoke></minimax:tool_call>`
	name, args, _, ok := ExtractFunctionCallXML(input)
	if !ok || name != "smartquery" {
		t.Fatalf("expected smartquery, got name=%q ok=%v", name, ok)
	}
	objs, _ := args["objects"].([]interface{})
	if len(objs) != 1 || objs[0] != "Order" {
		t.Errorf("objects: got %v", objs)
	}
	if args["metric"] != "sum(Order_Quantity)" {
		t.Errorf("metric: got %v", args["metric"])
	}
}

func TestExtractFunctionCallXML_NoToolCallReturnsFalse(t *testing.T) {
	// Plain text answer must not be mistaken for a tool call.
	input := `用户的订单总数是 1234，分布在三个地区：北京、上海、深圳。`
	_, _, _, ok := ExtractFunctionCallXML(input)
	if ok {
		t.Errorf("expected ok=false for plain text, got true")
	}
}

func TestExtractFunctionCallXML_FalsePositiveGuard(t *testing.T) {
	// Object with `name` but no `arguments` must NOT be treated as a tool call.
	input := `Sample data: {"name":"Lenovo","price":100}`
	_, _, _, ok := ExtractFunctionCallXML(input)
	if ok {
		t.Errorf("expected ok=false for non-toolcall JSON, got true")
	}
}

func TestExtractBareToolCallJSON_SkipsNonMatching(t *testing.T) {
	// First brace opens a non-tool-call object; second brace opens the real one.
	input := `Result: {"foo":"bar"} then {"name":"smartquery","arguments":{"limit":10}}`
	name, args, _, ok := extractBareToolCallJSON(input)
	if !ok || name != "smartquery" {
		t.Fatalf("expected smartquery, got name=%q ok=%v", name, ok)
	}
	if v, _ := args["limit"].(float64); v != 10 {
		t.Errorf("limit: got %v", args["limit"])
	}
}

func TestFindMatchingBrace_HandlesStringEscapes(t *testing.T) {
	// A '}' inside a JSON string must not close the outer brace.
	input := `{"a":"x\"}y","b":1}`
	got := findMatchingBrace(input, 0)
	want := len(input) - 1
	if got != want {
		t.Errorf("findMatchingBrace: got %d want %d", got, want)
	}
}

func TestExtractBareToolCallJSON_ArgumentsShape(t *testing.T) {
	// Make sure parsed argument types match expectations downstream.
	input := `{"name":"smartquery","arguments":{"objects":["a","b"],"limit":5,"fuzzyMatch":true}}`
	_, args, _, ok := extractBareToolCallJSON(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	wantObjs := []interface{}{"a", "b"}
	gotObjs, _ := args["objects"].([]interface{})
	if !reflect.DeepEqual(gotObjs, wantObjs) {
		t.Errorf("objects: got %v want %v", gotObjs, wantObjs)
	}
	if v, _ := args["limit"].(float64); v != 5 {
		t.Errorf("limit: got %v", args["limit"])
	}
	if args["fuzzyMatch"] != true {
		t.Errorf("fuzzyMatch: got %v", args["fuzzyMatch"])
	}
}
