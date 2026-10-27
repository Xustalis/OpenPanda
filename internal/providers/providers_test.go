// SPDX-License-Identifier: AGPL-3.0-or-later

package providers

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestBuiltinsCoverRequestedVendors(t *testing.T) {
	want := []string{"deepseek", "claude", "openai", "kimi", "minimax", "volcengine"}
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
	if mc.Model != "deepseek-flash" {
		t.Fatalf("expected default model, got %q", mc.Model)
	}
	if mc.BaseURL != "https://api.deepseek.com/anthropic" {
		t.Fatalf("unexpected base_url %q", mc.BaseURL)
	}
	if mc.ContextWindow != 1048576 {
		t.Fatalf("expected context_window 1048576, got %d", mc.ContextWindow)
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
		{"minimax", RegionChina, true, false},
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
		{"claude-sonnet-5-5", RegionGlobal},
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

// TestModelSpecResolution pins the per-model catalogue layer: prefix match
// onto a family row, longest-match-first, spec fields overriding the
// provider row, and the ctx/8 auto-load clamped by the vendor ceiling.
func TestModelSpecResolution(t *testing.T) {
	mini, _ := Lookup("minimax")

	// Exact family prefix covers the whole M2.x line — a 204800-token
	// window but a 196608 completion ceiling (probed live).
	s, ok := mini.SpecFor("MiniMax-M2.7")
	if !ok || s.ContextWindow != 204800 || s.MaxTokensCap != 196608 {
		t.Fatalf("M2.7 spec = %+v (ok=%v), want 204800/196608 row", s, ok)
	}
	// A versioned id lands on the longer matching prefix.
	s, ok = mini.SpecFor("MiniMax-M3.1-Flash-Preview")
	if !ok || s.ContextWindow != 1000000 || s.MaxTokensCap != 524288 {
		t.Fatalf("M3.1 spec = %+v (ok=%v), want the 1M/512K row", s, ok)
	}
	if _, ok = mini.SpecFor("MiniMax-M99"); ok {
		t.Fatal("unknown minimax model should not resolve a spec")
	}

	// ContextFor falls back to the provider row for unlisted models.
	ds, _ := Lookup("deepseek")
	if got := ds.ContextFor("deepseek-v9-future"); got != 1048576 {
		t.Fatalf("provider-row fallback ctx = %d, want 1048576", got)
	}
	if got := mini.ContextFor("MiniMax-M2.5"); got != 204800 {
		t.Fatalf("spec ctx = %d, want 204800", got)
	}

	// AutoMaxTokens: catalogue floor lifted to ctx/8, clamped to the cap.
	if got := ds.AutoMaxTokens("deepseek-flash"); got != 131072 {
		t.Fatalf("deepseek auto = %d, want 131072 (1M/8)", got)
	}
	if got := mini.AutoMaxTokens("MiniMax-M3"); got != 131072 {
		t.Fatalf("minimax auto = %d, want vendor default 131072", got)
	}
	if got := mini.AutoMaxTokens("MiniMax-M2.7"); got != 65536 {
		t.Fatalf("M2.7 auto = %d, want spec default 65536 (ctx/8 = 25600 loses)", got)
	}
	// Unlisted provider with no numbers → nothing to say.
	custom, _ := Lookup("custom")
	if got := custom.AutoMaxTokens("x"); got != 0 {
		t.Fatalf("custom auto = %d, want 0", got)
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
		"claude-sonnet": {2.00, 10.00},
		"gpt-5.4-mini":  {0.75, 4.50},
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
	// Model-name aliases resolve the vendor even when the endpoint is an
	// unknown relay — the id-prefix pass alone misses gpt-*/glm-*/doubao-*.
	aliasCases := []struct {
		model string
		want  string
	}{
		{"gpt-6.1-sol", "openai"},
		{"gpt-4o-mini", "openai"},
		{"o3-mini", "openai"},
		{"glm-5.3", "zhipu"},
		{"doubao-seed-2-1-pro-260915", "volcengine"},
		{"moonshot-v1-8k", "kimi"},
		{"kimi-k3", "kimi"},
		{"qwen3.7-plus", "qwen"},
		{"MiniMax-M2.7", "minimax"},
		{"claude-sonnet-5-5", "claude"},
	}
	for _, tc := range aliasCases {
		got, ok := Detect("https://relay.example.com/v1", tc.model)
		if !ok || got.ID != tc.want {
			t.Errorf("Detect(relay, %q) = %v, %v; want %s", tc.model, got.ID, ok, tc.want)
		}
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

// TestOpenCodeGatewayRows pins the two OpenCode catalogue rows. Zen and Go
// share one oc_sk_ key but not an endpoint or a roster: Zen is the
// pay-as-you-go catalogue plus the free tier on /zen/v1, Go is the
// subscription roster on /zen/go/v1. Every model on both is served on the
// OpenAI-compatible SDK surface OpenCode's own models.json declares, so the
// old four-surface routing table was both wrong and unnecessary.
func TestOpenCodeGatewayRows(t *testing.T) {
	zen, ok := Lookup("opencode")
	if !ok {
		t.Fatal("missing opencode provider")
	}
	if zen.BaseURL != "https://opencode.ai/zen/v1" || zen.ModelsPath != "/models" {
		t.Fatalf("zen endpoint = %q models %q", zen.BaseURL, zen.ModelsPath)
	}
	if zen.DefaultModel != "deepseek-v4.1-flash-free" {
		t.Fatalf("zen default = %q, want the free tier", zen.DefaultModel)
	}
	if zen.SessionHeader != "x-opencode-session" {
		t.Fatalf("zen session header = %q", zen.SessionHeader)
	}
	zp, _ := ModelConfig("opencode", "deepseek-v4.1-flash", "oc_sk_test")
	if zp.BaseURL != zen.BaseURL || zp.APIType != config.APITypeOpenAI {
		t.Fatalf("zen model = %+v, want the /zen/v1 chat surface", zp)
	}

	gop, ok := Lookup("opencode-go")
	if !ok {
		t.Fatal("missing opencode-go provider")
	}
	if gop.BaseURL != "https://opencode.ai/zen/go/v1" || gop.ModelsPath != "/models" {
		t.Fatalf("go endpoint = %q models %q", gop.BaseURL, gop.ModelsPath)
	}
	if gop.DefaultModel != "deepseek-v4.1-flash" {
		t.Fatalf("go default = %q", gop.DefaultModel)
	}
	if gop.SessionHeader != "x-opencode-session" {
		t.Fatalf("go session header = %q", gop.SessionHeader)
	}
	gm, _ := ModelConfig("opencode-go", "claude-haiku-5-5", "oc_sk_test")
	if gm.BaseURL != gop.BaseURL || gm.APIType != config.APITypeOpenAI {
		t.Fatalf("go claude model = %+v, want the same OpenAI-chat surface", gm)
	}

	// The free Zen model advertises a 200K window and a 32K completion
	// ceiling; AutoMaxTokens lifts the 4096 default to an eighth of the
	// window but never past that ceiling.
	if ctx := zen.ContextFor("deepseek-v4.1-flash-free"); ctx != 200000 {
		t.Errorf("free context = %d, want 200000", ctx)
	}
	if _, cap := zen.TokenPolicy("deepseek-v4.1-flash-free"); cap != 32000 {
		t.Errorf("free completion ceiling = %d, want 32000", cap)
	}
	if mt := zen.AutoMaxTokens("deepseek-v4.1-flash-free"); mt != 25000 {
		t.Errorf("free auto max tokens = %d, want 25000", mt)
	}

	// New SDK endpoints and retired /inference aliases both identify the row
	// they belong to.
	for _, tc := range []struct{ url, want string }{
		{"https://opencode.ai/zen/v1", "opencode"},
		{"https://opencode.ai/zen", "opencode"},
		{"https://opencode.ai/zen/go/v1", "opencode-go"},
		{"https://opencode.ai/zen/go", "opencode-go"},
		{"https://opencode.ai/inference/openai/v1", "opencode-go"},
		{"https://opencode.ai/inference/anthropic", "opencode-go"},
		{"https://opencode.ai/inference/google/v1beta", "opencode-go"},
	} {
		got, ok := Detect(tc.url, "")
		if !ok || got.ID != tc.want {
			t.Errorf("Detect(%q) = %v, %v; want %s", tc.url, got.ID, ok, tc.want)
		}
	}

	// Pricing is per model and per roster; an unlisted id falls back to the
	// row rate rather than being reported as free.
	if got := LookupPricing("opencode", "deepseek-v4.1-flash"); got != (Pricing{InputPerMillion: 0.30, OutputPerMillion: 1.20}) {
		t.Errorf("zen deepseek pricing = %+v", got)
	}
	if got := LookupPricing("opencode-go", "deepseek-v4.1-flash"); got != (Pricing{InputPerMillion: 0.15, OutputPerMillion: 0.60}) {
		t.Errorf("go deepseek pricing = %+v", got)
	}
	if got := LookupPricing("opencode-go", "claude-haiku-5-5"); got != (Pricing{InputPerMillion: 0.10, OutputPerMillion: 0.50}) {
		t.Errorf("go haiku pricing = %+v", got)
	}
	if got := LookupPricing("opencode", "deepseek-v4.1-flash-free"); got != (Pricing{}) {
		t.Errorf("free pricing = %+v, want zero", got)
	}
	if got := LookupPricing("opencode-go", "some-unlisted-model"); got != (Pricing{InputPerMillion: 0.15, OutputPerMillion: 0.60}) {
		t.Errorf("unlisted pricing = %+v, want the row rate", got)
	}

	// Every served model speaks the row's dialect.
	for _, m := range []string{"kimi-k3", "claude-haiku-5-5", "gpt-6-luna", "gemini-4-argon"} {
		if got := ThinkingStyleFor(config.ModelConfig{Provider: "opencode-go", Model: m}); got != "object" {
			t.Errorf("%s thinking style = %q, want object", m, got)
		}
		if got := gop.UnsupportedReason(m); got != "" {
			t.Errorf("%s reported unsupported: %q", m, got)
		}
	}
}

// TestSiblingForOpenCodeRosters pins the misconfiguration guard: a free Zen
// id added under the Go provider (or a Go-only id under Zen) is reported as
// belonging to the other row instead of failing at call time.
func TestSiblingForOpenCodeRosters(t *testing.T) {
	if p, ok := SiblingFor("opencode-go", "deepseek-v4.1-flash-free"); !ok || p.ID != "opencode" {
		t.Fatalf("free Zen id under Go: %v %v, want opencode", p.ID, ok)
	}
	if p, ok := SiblingFor("opencode", "kimi-k3.1-pro"); !ok || p.ID != "opencode-go" {
		t.Fatalf("Go id under Zen: %v %v, want opencode-go", p.ID, ok)
	}
	// A model its own row lists is not a sibling miss.
	if p, ok := SiblingFor("opencode", "deepseek-v4.1-flash-free"); ok {
		t.Fatalf("own free id reported as sibling %q", p.ID)
	}
	// An unknown id has no sibling either.
	if p, ok := SiblingFor("opencode", "no-such-model"); ok {
		t.Fatalf("unknown id reported as sibling %q", p.ID)
	}
	// A model both rosters list has no unique sibling; a custom endpoint
	// pointed at either surface must not be warned about.
	if p, ok := SiblingFor("custom", "deepseek-v4.1-flash"); ok {
		t.Fatalf("shared model reported as unique sibling %q", p.ID)
	}
}

// TestProviderRouteResolution keeps the alternate-surface mechanism honest
// even though no shipped row needs it now: a route's longest prefix wins,
// and its endpoint counts as the provider's own for rebasing.
func TestProviderRouteResolution(t *testing.T) {
	p := Provider{
		BaseURL: "https://api.example.com/v1",
		APIType: config.APITypeOpenAI,
		Routes: []Route{
			{Prefixes: []string{"claude-"}, BaseURL: "https://api.example.com/anthropic", APIType: config.APITypeAnthropic},
			{Prefixes: []string{"claude-opus-"}, BaseURL: "https://api.example.com/opus", APIType: config.APITypeAnthropic},
		},
		LegacyBaseURLs: []string{"https://old.example.com/v1"},
	}
	if base, typ := p.EndpointFor("claude-opus-5-5"); base != "https://api.example.com/opus" || typ != config.APITypeAnthropic {
		t.Fatalf("opus route = %q %q, want the longest prefix", base, typ)
	}
	if base, typ := p.EndpointFor("claude-haiku-5-5"); base != "https://api.example.com/anthropic" || typ != config.APITypeAnthropic {
		t.Fatalf("haiku route = %q %q", base, typ)
	}
	if base, typ := p.EndpointFor("gpt-6-luna"); base != p.BaseURL || typ != config.APITypeOpenAI {
		t.Fatalf("unrouted = %q %q, want the row surface", base, typ)
	}
	for _, own := range []string{p.BaseURL, "https://api.example.com/anthropic", "https://old.example.com/v1/"} {
		if !p.OwnsEndpoint(own) {
			t.Errorf("OwnsEndpoint(%q) = false", own)
		}
	}
	if p.OwnsEndpoint("https://relay.example.com/v1") {
		t.Error("a foreign relay must not be owned")
	}
}
