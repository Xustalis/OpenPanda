// SPDX-License-Identifier: AGPL-3.0-or-later

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

func TestGeographicOrigin(t *testing.T) {
	tests := []struct {
		id       string
		wantGeo  GeoRegion
		isCN     bool
		isGlobal bool
	}{
		{"deepseek", RegionChina, true, false},
		{"kimi", RegionChina, true, false},
		{"volcengine", RegionChina, true, false},
		{"zhipu", RegionChina, true, false},
		{"qwen", RegionChina, true, false},
		{"siliconflow", RegionChina, true, false},
		{"claude", RegionGlobal, false, true},
		{"openai", RegionGlobal, false, true},
		{"openrouter", RegionGlobal, false, true},
		{"ollama", RegionGlobal, false, true},
		{"custom", RegionGlobal, false, true},
	}

	for _, tt := range tests {
		p, ok := Lookup(tt.id)
		if !ok {
			t.Fatalf("provider %q not found", tt.id)
		}
		if p.GeographicOrigin != tt.wantGeo {
			t.Errorf("provider %q GeographicOrigin = %v, want %v", tt.id, p.GeographicOrigin, tt.wantGeo)
		}
		if p.IsChineseModel() != tt.isCN {
			t.Errorf("provider %q IsChineseModel() = %v, want %v", tt.id, p.IsChineseModel(), tt.isCN)
		}
		if p.IsGlobalModel() != tt.isGlobal {
			t.Errorf("provider %q IsGlobalModel() = %v, want %v", tt.id, p.IsGlobalModel(), tt.isGlobal)
		}
	}
}

func TestDetectRegion(t *testing.T) {
	tests := []struct {
		input   string
		wantGeo GeoRegion
	}{
		{"deepseek", RegionChina},
		{"deepseek-chat", RegionChina},
		{"deepseek-v4-flash", RegionChina},
		{"qwen", RegionChina},
		{"qwen-max", RegionChina},
		{"qwen2.5-coder:14b", RegionChina},
		{"kimi", RegionChina},
		{"moonshot-v1-8k", RegionChina},
		{"doubao", RegionChina},
		{"doubao-1-5-pro-32k", RegionChina},
		{"zhipu", RegionChina},
		{"glm-4-plus", RegionChina},
		{"siliconflow", RegionChina},
		{"claude", RegionGlobal},
		{"claude-sonnet-4-5", RegionGlobal},
		{"agent:claude_code", RegionGlobal},
		{"openai", RegionGlobal},
		{"gpt-4o", RegionGlobal},
		{"agent:codex", RegionGlobal},
		{"openrouter", RegionGlobal},
		{"ollama", RegionGlobal},
		{"custom", RegionGlobal},
		{"", RegionGlobal},
		{"unknown-model", RegionGlobal},
	}

	for _, tt := range tests {
		if got := DetectRegion(tt.input); got != tt.wantGeo {
			t.Errorf("DetectRegion(%q) = %v, want %v", tt.input, got, tt.wantGeo)
		}
	}
}

// TestPricingCost covers the unit-cost arithmetic: free pricing yields zero,
// priced tokens scale per million, and input/output sides add independently.
func TestPricingCost(t *testing.T) {
	free := Pricing{}
	if got := free.Cost(1_000_000, 1_000_000); got != 0 {
		t.Fatalf("free pricing cost = %v, want 0", got)
	}
	p := Pricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}
	if got := p.Cost(1_000_000, 0); got != 3.00 {
		t.Fatalf("input-only cost = %v, want 3.00", got)
	}
	if got := p.Cost(0, 500_000); got != 7.50 {
		t.Fatalf("output-only cost = %v, want 7.50", got)
	}
	if got := p.Cost(2_000_000, 1_000_000); got != 21.00 {
		t.Fatalf("combined cost = %v, want 21.00", got)
	}
}

// TestLookupPricing pins the catalogue lookup and the name-substring fallbacks
// used when the provider id is absent from the catalogue (custom endpoints).
func TestLookupPricing(t *testing.T) {
	// The name-substring fallback only fires for provider ids absent from
	// the catalogue ("custom" is itself a builtin and would take the
	// catalogue branch), so probe it with a genuinely unknown id.
	catalog := map[string]struct{ in, out float64 }{
		"deepseek":      {0.14, 0.28},
		"claude-haiku":  {0.80, 4.00},
		"claude-sonnet": {3.00, 15.00},
		"gpt-4o-mini":   {0.15, 0.60},
		"gpt-4o":        {2.50, 10.00},
		"qwen-max":      {0.11, 0.28},
	}
	for model, want := range catalog {
		got := LookupPricing("unlisted-provider", model)
		if got.InputPerMillion != want.in || got.OutputPerMillion != want.out {
			t.Errorf("LookupPricing(unlisted, %q) = %+v, want %+v", model, got, want)
		}
	}
	if got := LookupPricing("unlisted-provider", "something-unknown"); got != (Pricing{}) {
		t.Errorf("unknown model = %+v, want zero Pricing", got)
	}
	// A catalogued provider returns its own table, not the name fallback.
	if p, ok := Lookup("openai"); ok && p.Pricing != (Pricing{}) {
		got := LookupPricing("openai", "anything")
		if got != p.Pricing {
			t.Errorf("catalogued provider pricing = %+v, want %+v", got, p.Pricing)
		}
	}
}

// TestThinkingStyleFor pins the wire-shape selection: an anthropic endpoint
// always takes the anthropic object, a provider's declared style wins for
// other dialects, and a bare custom openai endpoint falls back to "object".
func TestThinkingStyleFor(t *testing.T) {
	if got := ThinkingStyleFor(config.ModelConfig{APIType: config.APITypeAnthropic}); got != "anthropic" {
		t.Fatalf("anthropic dialect style = %q", got)
	}
	for _, p := range All() {
		if p.ThinkingStyle == "" {
			continue
		}
		if got := ThinkingStyleFor(config.ModelConfig{Provider: p.ID, APIType: config.APITypeOpenAI}); got != p.ThinkingStyle {
			t.Errorf("provider %s declared style = %q, resolved %q", p.ID, p.ThinkingStyle, got)
		}
	}
	if got := ThinkingStyleFor(config.ModelConfig{Provider: "custom", APIType: config.APITypeOpenAI}); got != "object" {
		t.Fatalf("custom openai fallback = %q, want object", got)
	}
}

// TestDetect covers provider inference: exact and containing base-URL hits,
// model-prefix hits, and a clean miss.
func TestDetect(t *testing.T) {
	for _, p := range All() {
		if p.ID == "custom" || p.BaseURL == "" {
			continue
		}
		got, ok := Detect(p.BaseURL, "")
		if !ok || got.ID != p.ID {
			t.Errorf("Detect(%q) = %v, %v; want %s", p.BaseURL, got.ID, ok, p.ID)
		}
	}
	if _, ok := Detect("https://example.invalid", "no-such-model"); ok {
		t.Error("Detect matched a nonsense endpoint/model")
	}
}

// TestAllReturnsCopy ensures callers cannot mutate the shared catalogue.
func TestAllReturnsCopy(t *testing.T) {
	a := All()
	if len(a) == 0 {
		t.Fatal("catalogue is empty")
	}
	a[0].ID = "mutated"
	if All()[0].ID == "mutated" {
		t.Fatal("All() exposes the shared backing array")
	}
}
