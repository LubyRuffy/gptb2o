package backend

import "testing"

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		in   string
		out  string
	}{
		{name: "empty", in: "", out: ""},
		{name: "undefined", in: "[undefined]", out: ""},
		{name: "null", in: "null", out: ""},
		{name: "low", in: "low", out: "low"},
		{name: "medium_keep_case", in: "MeDiuM", out: "MeDiuM"},
		{name: "high", in: "HIGH", out: "HIGH"},
		{name: "xhigh", in: "xhigh", out: "xhigh"},
		{name: "unknown", in: "ultra", out: "ultra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeReasoningEffort(tt.in)
			if got != tt.out {
				t.Fatalf("NormalizeReasoningEffort(%q)=%q, want %q", tt.in, got, tt.out)
			}
		})
	}
}

func TestIsUnsupportedReasoningEffortError(t *testing.T) {
	msg := `{"error":{"message":"Unsupported value: 'xhigh' is not supported","param":"reasoning.effort"}}`
	if !IsUnsupportedReasoningEffortError(msg, "xhigh") {
		t.Fatalf("expected unsupported reasoning error to be detected")
	}
	if IsUnsupportedReasoningEffortError(msg, "high") {
		t.Fatalf("unexpected detection for unrelated effort")
	}
}

func TestFallbackReasoningEffort(t *testing.T) {
	out, ok := FallbackReasoningEffort("xhigh")
	if !ok || out != "high" {
		t.Fatalf("FallbackReasoningEffort(xhigh)=(%q,%v), want (high,true)", out, ok)
	}
	out, ok = FallbackReasoningEffort("x-high")
	if !ok || out != "high" {
		t.Fatalf("FallbackReasoningEffort(x-high)=(%q,%v), want (high,true)", out, ok)
	}
	out, ok = FallbackReasoningEffort("medium")
	if ok || out != "" {
		t.Fatalf("FallbackReasoningEffort(medium)=(%q,%v), want (\"\",false)", out, ok)
	}
}
