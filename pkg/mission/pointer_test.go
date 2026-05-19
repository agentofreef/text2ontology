package mission

import "testing"

func TestIsRef(t *testing.T) {
	refs := []string{
		"t1", "t12", "t1.city", "t2.Total_amount",
		"t1.city[0]", "t3.col[42]",
		"mABC.t1.city[0]", "m12ab-cd.t2.x",
		"t1.city[0] ", // trimmed before matching
	}
	for _, s := range refs {
		if !IsRef(s) {
			t.Errorf("%q should be a ref", s)
		}
	}
	notRefs := []string{
		"", "上海", "city", "t", "tN", "100",
		"city=t1.city[0]", "tt1", "t1.", "t1.city[]", "monday",
	}
	for _, s := range notRefs {
		if IsRef(s) {
			t.Errorf("%q should NOT be a ref", s)
		}
	}
}

func TestScanForLiteral(t *testing.T) {
	steps := map[string]StepResult{
		"t1": {Rows: []map[string]any{
			{"city": "上海", "Total_amount": "100"},
			{"city": "北京", "Total_amount": "50"},
		}},
	}

	// T11: a literal copied out of a step result is flagged.
	args := map[string]any{
		"intent": "store_revenue",
		"params": map[string]any{"city": "上海"},
	}
	viol, found := ScanForLiteral(args, steps, nil)
	if !found {
		t.Fatal("literal 上海 should be flagged")
	}
	if viol.Literal != "上海" || viol.ShouldBe != "t1.city[0]" {
		t.Errorf("unexpected violation: %+v", viol)
	}

	// A reference value is fine — it IS the pointer.
	okArgs := map[string]any{"params": map[string]any{"city": "t1.city[0]"}}
	if _, found := ScanForLiteral(okArgs, steps, nil); found {
		t.Error("a reference value must not be flagged")
	}

	// T12: a question-origin literal is exempt.
	if _, found := ScanForLiteral(args, steps, map[string]bool{"上海": true}); found {
		t.Error("an exempt literal must not be flagged")
	}

	// A literal absent from every step result is fine.
	free := map[string]any{"params": map[string]any{"city": "广州"}}
	if _, found := ScanForLiteral(free, steps, nil); found {
		t.Error("a literal absent from steps must not be flagged")
	}

	// Nested slices are walked.
	sliceArgs := map[string]any{"list": []any{"安全", "北京"}}
	v, found := ScanForLiteral(sliceArgs, steps, nil)
	if !found || v.Literal != "北京" || v.ShouldBe != "t1.city[1]" {
		t.Errorf("slice scan failed: found=%v viol=%+v", found, v)
	}

	// An Intent name is a plain literal not present in any step -> fine.
	if _, found := ScanForLiteral(map[string]any{"intent": "store_revenue"}, steps, nil); found {
		t.Error("an Intent name should not be flagged")
	}
}
