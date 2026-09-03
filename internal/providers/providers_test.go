package providers

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestBuiltinsCoverRequestedVendors(t *testing.T) {
	want := []string{"deepseek", "claude", "openai", "kimi", "volcengine"}
	for _, id := range want {
		p, ok := Lookup(id)
		if !ok {
			t.Fatalf("missing built-in provider %q", id)
		}
		if p.BaseURL == "" && id != "custom" {
			t.Fatalf("provider %q has empty base_url", id)
		}
		if p.APIType != config.APITypeAnthropic && p.APIType != config.APITypeOpenAI {
			t.Fatalf("provider %q has invalid api_type %q", id, p.APIType)
		}
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := Lookup("nope"); ok {
		t.Fatal("expected unknown provider to miss")
	}
}

func TestModelConfigBuilds(t *testing.T) {
	mc, ok := ModelConfig("deepseek", "", "sk-test")
	if !ok {
		t.Fatal("expected deepseek to resolve")
	}
	if mc.Provider != "deepseek" || mc.APIKey != "sk-test" {
		t.Fatalf("bad build: %+v", mc)
	}
	if mc.Model != "deepseek-v4-flash" {
		t.Fatalf("expected default model, got %q", mc.Model)
	}
	if mc.BaseURL != "https://api.deepseek.com/anthropic" {
		t.Fatalf("unexpected base_url %q", mc.BaseURL)
	}
	if mc.ContextWindow != 128000 {
		t.Fatalf("expected context_window 128000, got %d", mc.ContextWindow)
	}

	// Explicit model overrides the default.
	mc2, _ := ModelConfig("deepseek", "deepseek-v4-pro", "sk-x")
	if mc2.Model != "deepseek-v4-pro" {
		t.Fatalf("expected explicit model, got %q", mc2.Model)
	}
}

func TestModelConfigUnknown(t *testing.T) {
	if _, ok := ModelConfig("bogus", "", "k"); ok {
		t.Fatal("expected unknown provider to miss")
	}
}
