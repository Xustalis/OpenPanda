package security

import (
	"strings"
	"testing"
)

func TestRedactKeyValue(t *testing.T) {
	cases := map[string]string{
		"ANTHROPIC_API_KEY=sk-test-123":              "ANTHROPIC_API_KEY=[redacted]",
		"ANTHROPIC_API_KEY=sk-test-123 more text":    "ANTHROPIC_API_KEY=[redacted] more text",
		"token=xyz,next=1":                           "token=[redacted],next=1",
		"authorization: Bearer abc.def.ghi trailing": "authorization: Bearer [redacted] trailing",
		`{"api_key": "sk-json-123"}`:                 `{"api_key": "[redacted]"}`,
		`{"secret": "s3cr3t", "next": 1}`:            `{"secret": "[redacted]", "next": 1}`,
		`token: "quoted value here"`:                 `token: "[redacted]"`,
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Fatalf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactBearerToken(t *testing.T) {
	got := Redact(`{"authorization":"Bearer eyJhbGciOiJIUzI1NiJ9.abc.def"}`)
	if strings.Contains(got, "eyJhbGci") {
		t.Fatalf("bearer token not redacted: %q", got)
	}
	if !strings.Contains(got, "Bearer [redacted]") {
		t.Fatalf("expected Bearer [redacted], got %q", got)
	}
}

func TestRedactLeavesNormalText(t *testing.T) {
	in := "exit status 1: command not found"
	if got := Redact(in); got != in {
		t.Fatalf("Redact(%q) = %q, want unchanged", in, got)
	}
}
