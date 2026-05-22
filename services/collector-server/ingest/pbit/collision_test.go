package pbit

import "testing"

func TestTruncToBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"under limit", "Sales", 63, "Sales"},
		{"exact limit", "abc", 3, "abc"},
		{"ascii cut", "abcdef", 3, "abc"},
		{"zero", "abc", 0, ""},
		{"trailing space trimmed", "ab cd", 3, "ab"},
		// "中" is 3 UTF-8 bytes: with n=4 only one rune fits, never a split.
		{"cjk no split at 4", "中文", 4, "中"},
		{"cjk no split at 5", "中文", 5, "中"},
		{"cjk both fit at 6", "中文", 6, "中文"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncToBytes(tt.in, tt.n); got != tt.want {
				t.Fatalf("truncToBytes(%q,%d)=%q want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

func TestFitIdentNeverExceedsLimit(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "数据" // 6 bytes each → 600 bytes
	}
	got := fitIdent(long)
	if len(got) > maxIdentBytes {
		t.Fatalf("fitIdent produced %d bytes, exceeds %d", len(got), maxIdentBytes)
	}
}

func TestSanitizeLabel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Sales.pbix", "Sales"},
		{"FY24 report.PBIX", "FY24 report"},
		{"north\tregion\n", "north region"},
		{`weird"quotes"`, "weird quotes"},
		{"   ", "src"},
		{"", "src"},
	}
	for _, tt := range tests {
		if got := sanitizeLabel(tt.in); got != tt.want {
			t.Errorf("sanitizeLabel(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
	// Long label is capped to 24 runes.
	longLabel := "this label is definitely longer than twenty four runes for sure"
	if rs := []rune(sanitizeLabel(longLabel)); len(rs) > 24 {
		t.Errorf("sanitizeLabel did not cap length: %d runes", len(rs))
	}
}

func TestCandidateName(t *testing.T) {
	if got := candidateName("Sales", "北区", 0); got != "Sales" {
		t.Errorf("attempt 0 = %q want %q", got, "Sales")
	}
	if got := candidateName("Sales", "北区", 1); got != "Sales (北区)" {
		t.Errorf("attempt 1 = %q want %q", got, "Sales (北区)")
	}
	if got := candidateName("Sales", "北区", 2); got != "Sales (北区 2)" {
		t.Errorf("attempt 2 = %q want %q", got, "Sales (北区 2)")
	}
}

func TestCandidateNamePreservesSuffixUnderTruncation(t *testing.T) {
	// raw at/over the byte limit must still keep the disambiguating suffix.
	raw := ""
	for i := 0; i < 80; i++ {
		raw += "x" // 80 ASCII bytes, over the 63 limit
	}
	got := candidateName(raw, "北区", 1)
	if len(got) > maxIdentBytes {
		t.Fatalf("candidate %d bytes exceeds %d", len(got), maxIdentBytes)
	}
	// The suffix " (北区)" must survive intact (it's what makes the name unique).
	const suffix = " (北区)"
	if got[len(got)-len(suffix):] != suffix {
		t.Fatalf("suffix dropped under truncation: %q", got)
	}
}

func TestShortHashStableAndShort(t *testing.T) {
	a := shortHash("00000000-0000-0000-0000-000000000001")
	b := shortHash("00000000-0000-0000-0000-000000000001")
	c := shortHash("00000000-0000-0000-0000-000000000002")
	if a != b {
		t.Errorf("shortHash not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("shortHash collided across distinct ids")
	}
	if len(a) != 8 {
		t.Errorf("shortHash len = %d want 8", len(a))
	}
}
