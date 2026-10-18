// SPDX-License-Identifier: AGPL-3.0-or-later

// Package providers is the built-in LLM provider catalogue. It turns the
// "paste a key and pick a model" workflow into data instead of prose: each
// entry carries the wire dialect, endpoint, auth style, model-list path and
// per-vendor tuning flags the entry client needs, so adding a vendor is a
// table edit, never a code change.
//
// The catalogue is deliberately dependency-free. The entry client merges a
// Provider's tuning into a config.ModelConfig at construction time; the CLI
// and web panel read the same table to render the "add model" picker, so the
// two surfaces can never drift apart.
package providers

import (
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// GeoRegion designates the model provider's geographical origin.
type GeoRegion string

const (
	RegionChina  GeoRegion = "cn"
	RegionGlobal GeoRegion = "global"
)

// Provider describes one built-in LLM vendor.
type Provider struct {
	// ID is the stable key the user types: "deepseek", "claude", "openai",
	// "kimi", "volcengine"… It is also the value stored in
	// config.ModelConfig.Provider.
	ID string
	// Label is the human-facing name shown in pickers.
	Label string
	// APIType is the wire dialect: config.APITypeAnthropic (Messages API) or
	// config.APITypeOpenAI (Chat Completions).
	APIType string
	// BaseURL is the endpoint base, e.g. "https://api.deepseek.com/anthropic".
	BaseURL string
	// NoAuth marks providers that need no API key (Ollama, a local vLLM).
	NoAuth bool
	// ModelsPath is the list-models endpoint, relative to BaseURL (leading
	// slash included). Empty derives from APIType: openai → "/models",
	// anthropic → "/v1/models".
	ModelsPath string
	// DefaultModel is the model chosen when the user adds the provider with
	// only a key and no explicit model id.
	DefaultModel string
	// DefaultMaxTokens is the completion cap applied when the config names
	// none. 0 falls back to the entry package default.
	DefaultMaxTokens int
	// MaxTokensCap is the provider's hard ceiling on the completion cap —
	// a configured max_tokens above it can never succeed (MiniMax answers
	// 400 past 524288), so the entry client clamps down to it instead of
	// failing the turn. 0 = no known ceiling.
	MaxTokensCap int
	// ContextWindow is the advertised context length in tokens; 0 = unknown.
	ContextWindow int
	// Models carries per-model limit rows for the catalogue entry — a
	// vendor's models may advertise different windows and ceilings
	// (MiniMax-M3 does 1M/512K, the M2.x line 200K/200K), so a matching
	// spec wins over the provider-level values. See SpecFor.
	Models []ModelSpec
	// ModelAliases are extra model-name prefixes Detect should resolve to
	// this provider — the provider id alone misses families like gpt-*
	// (openai), glm-* (zhipu) or doubao-* (volcengine).
	ModelAliases []string
	// ThinkingPassback marks providers whose multi-turn assistant history must
	// echo a thinking/reasoning placeholder. DeepSeek in thinking mode and
	// Anthropic's extended thinking reject the history otherwise. This is the
	// supplier-level default; the entry client still probes at runtime, so a
	// flag here only skips a rejected round-trip on the first call.
	ThinkingPassback bool
	// ThinkingStyle names the wire shape the client emits when the user turns
	// thinking on (config.ModelConfig.Thinking). Anthropic-dialect providers
	// always use the Anthropic thinking object regardless of this field; for
	// OpenAI-dialect providers the choice is per vendor:
	//   "object" → thinking:{type:"enabled"|"disabled"|"auto"} (Ark, GLM,
	//              DeepSeek OpenAI surface, most CN relays)
	//   "flag"   → enable_thinking:bool + thinking_budget:int (DashScope/Qwen)
	//   "effort" → reasoning_effort:"low"|"medium"|"high" (OpenAI, OpenRouter)
	//   ""       → send nothing; the model's native behaviour applies
	// Unknown values are treated as "". A 400 that names the thinking field
	// triggers one automatic retry without it, so a wrong guess degrades to
	// the provider default rather than failing the turn.
	ThinkingStyle string
	// PromptCache toggles provider-native prompt-cache markers (Anthropic
	// cache_control / OpenAI prompt_cache_key). Most vendors honour them; a
	// strict legacy relay may not, so it is per-vendor tunable.
	PromptCache bool
	// Pricing defines the default token prices in USD per 1M tokens.
	Pricing Pricing
	// GeographicOrigin designates the vendor's regional provenance for prompt routing.
	GeographicOrigin GeoRegion
}

// IsChineseModel returns true if the provider's geographic origin is China.
func (p Provider) IsChineseModel() bool {
	return p.GeographicOrigin == RegionChina
}

// IsGlobalModel returns true if the provider's geographic origin is Global.
func (p Provider) IsGlobalModel() bool {
	return p.GeographicOrigin == RegionGlobal
}

// ModelSpec is one model's limits inside a Provider row. Zero fields fall
// through to the provider-level value, so a spec only needs the numbers
// that differ.
type ModelSpec struct {
	// ID is the model id (or family prefix — e.g. "MiniMax-M2" covers the
	// M2.7/M2.5/M2.1/-highspeed line). SpecFor matches longest-prefix-first.
	ID               string
	ContextWindow    int
	MaxTokensCap     int
	DefaultMaxTokens int
}

// Pricing defines token prices in USD per 1M (1,000,000) tokens.
type Pricing struct {
	InputPerMillion  float64 `json:"input_per_million" yaml:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million" yaml:"output_per_million"`
}

// Cost calculates the estimated USD cost for the given input and output token counts.
func (p Pricing) Cost(inputTokens, outputTokens int64) float64 {
	if p.InputPerMillion <= 0 && p.OutputPerMillion <= 0 {
		return 0
	}
	return (float64(inputTokens)*p.InputPerMillion + float64(outputTokens)*p.OutputPerMillion) / 1_000_000.0
}

// builtins is the curated catalogue. Order matters: it is the display order of
// the "/model add" picker.
var builtins = []Provider{
	{
		ID:      "deepseek",
		Label:   "DeepSeek",
		APIType: config.APITypeAnthropic,
		BaseURL: "https://api.deepseek.com/anthropic",
		// DeepSeek's list-models endpoint lives on its OpenAI-compatible
		// surface (Bearer auth), not the Anthropic endpoint, so it is given as
		// an absolute override.
		ModelsPath: "https://api.deepseek.com/models",
		// V4.1-Flash/V4-Pro ship a 1M window and cap one completion at
		// 384K per the /models metadata; the retired deepseek-v4-flash id
		// still resolves, but the listed canonical id is deepseek-flash.
		DefaultModel:     "deepseek-flash",
		DefaultMaxTokens: 4096,
		MaxTokensCap:     393216,
		ContextWindow:    1048576,
		ThinkingPassback: true,
		ThinkingStyle:    "object",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 0.14, OutputPerMillion: 0.28},
		GeographicOrigin: RegionChina,
	},
	{
		ID:               "claude",
		Label:            "Claude (Anthropic)",
		APIType:          config.APITypeAnthropic,
		BaseURL:          "https://api.anthropic.com",
		ModelsPath:       "/v1/models",
		DefaultModel:     "claude-sonnet-5-5",
		DefaultMaxTokens: 8192,
		// The 5.x/Fable/Mythos generation (and Sonnet/Opus ≥4.6) advertises
		// a 1M window and a 128K synchronous output ceiling.
		MaxTokensCap:  131072,
		ContextWindow: 1000000,
		Models: []ModelSpec{
			// The late-4.x ids jumped to the 1M/128K tier ahead of the 5s.
			{ID: "claude-sonnet-4-6", ContextWindow: 1000000, MaxTokensCap: 131072},
			{ID: "claude-opus-4-6", ContextWindow: 1000000, MaxTokensCap: 131072},
			{ID: "claude-opus-4-7", ContextWindow: 1000000, MaxTokensCap: 131072},
			{ID: "claude-opus-4-8", ContextWindow: 1000000, MaxTokensCap: 131072},
			// The rest of the 4.x/older generation is the exception: 200K
			// window, 64K output ceiling — a configured cap past it is
			// clamped, not 400'd. (Sonnet-4-6+ longest-prefixes out first.)
			{ID: "claude-sonnet-4", ContextWindow: 200000, MaxTokensCap: 64000},
			{ID: "claude-opus-4", ContextWindow: 200000, MaxTokensCap: 64000},
			{ID: "claude-haiku-4", ContextWindow: 200000, MaxTokensCap: 64000},
		},
		ThinkingPassback: true,
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 2.00, OutputPerMillion: 10.00},
		GeographicOrigin: RegionGlobal,
	},
	{
		ID:               "openai",
		Label:            "ChatGPT (OpenAI)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://api.openai.com/v1",
		ModelsPath:       "/models",
		DefaultModel:     "gpt-6.1-sol",
		DefaultMaxTokens: 4096,
		// The GPT-6 family (astra / 6.1-sol / sol / luna) shares a 1.05M
		// window and a 128K completion ceiling, all on Chat Completions.
		ContextWindow: 1050000,
		MaxTokensCap:  128000,
		Models: []ModelSpec{
			{ID: "gpt-6", ContextWindow: 1050000, MaxTokensCap: 128000},
			// The gpt-5.4 flagship also runs 1.05M; its mini tier is 400K.
			{ID: "gpt-5.4-mini", ContextWindow: 400000, MaxTokensCap: 128000},
			{ID: "gpt-5.4", ContextWindow: 1050000, MaxTokensCap: 128000},
			// The gpt-4o family (incl. -mini and dated variants) caps a
			// completion at 16384 tokens.
			{ID: "gpt-4o", ContextWindow: 128000, MaxTokensCap: 16384},
		},
		ThinkingStyle:    "effort",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 2.00, OutputPerMillion: 10.00},
		GeographicOrigin: RegionGlobal,
		ModelAliases:     []string{"gpt-", "o1-", "o3-", "o4-"},
	},
	{
		ID:               "kimi",
		Label:            "Kimi (月之暗面)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://api.moonshot.cn/v1",
		ModelsPath:       "/models",
		DefaultModel:     "kimi-k3",
		DefaultMaxTokens: 4096,
		// kimi-k3 carries a 1M window; the k2.6/k2.7-code line is 256K.
		// kimi-latest, kimi-k2.5 and the moonshot-v1 ids were retired
		// upstream Aug 2026.
		ContextWindow: 1048576,
		Models: []ModelSpec{
			{ID: "kimi-k2", ContextWindow: 262144},
		},
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 1.40, OutputPerMillion: 1.40},
		GeographicOrigin: RegionChina,
		ModelAliases:     []string{"moonshot-"},
	},
	{
		ID:           "minimax",
		Label:        "MiniMax",
		APIType:      config.APITypeAnthropic,
		BaseURL:      "https://api.minimax.cn/anthropic",
		ModelsPath:   "/v1/models",
		DefaultModel: "MiniMax-M3",
		// M3/M3.1 advertise a 1M window but hard-cap one completion at
		// 512K (vendor-recommended 128K) — the cap is an API limit, not
		// a hint, which is why it lives on MaxTokensCap.
		DefaultMaxTokens: 131072,
		MaxTokensCap:     524288,
		ContextWindow:    1000000,
		Models: []ModelSpec{
			// The M3 line (incl. M3.1-Flash-Preview) carries the 1M window
			// and 512K completion ceiling; the M2.x family is 200K/192K.
			{ID: "MiniMax-M3", ContextWindow: 1000000, MaxTokensCap: 524288, DefaultMaxTokens: 131072},
			// M2.x family verified live: the API rejects max_tokens past
			// 196608 even though the advertised window is 204800.
			{ID: "MiniMax-M2", ContextWindow: 204800, MaxTokensCap: 196608, DefaultMaxTokens: 65536},
		},
		PromptCache:      true,
		GeographicOrigin: RegionChina,
	},
	{
		ID:               "volcengine",
		Label:            "火山引擎 (Ark/豆包)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://ark.cn-beijing.volces.com/api/v3",
		ModelsPath:       "/models",
		DefaultModel:     "doubao-seed-evolving",
		DefaultMaxTokens: 4096,
		// Seed-2.1 pro/lite and the evolving alias run a 1M window with a
		// 256K answer ceiling; turbo and the 2.0 line are 256K windows.
		ContextWindow: 1048576,
		MaxTokensCap:  262144,
		Models: []ModelSpec{
			{ID: "doubao-seed-2-1-turbo", ContextWindow: 262144, MaxTokensCap: 262144},
			{ID: "doubao-seed-2-0", ContextWindow: 262144, MaxTokensCap: 131072},
			{ID: "doubao-1-5", ContextWindow: 32768, MaxTokensCap: 4096},
		},
		ThinkingStyle:    "object",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 0.85, OutputPerMillion: 4.20},
		GeographicOrigin: RegionChina,
		ModelAliases:     []string{"doubao-"},
	},
	{
		ID:               "zhipu",
		Label:            "智谱 GLM",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://open.bigmodel.cn/api/paas/v4",
		ModelsPath:       "/models",
		DefaultModel:     "glm-5.3",
		DefaultMaxTokens: 4096,
		// GLM-5.2/5.3 (incl. -Flash/-FlashX) run a 1M window with a 128K
		// completion ceiling; the 5/5.1 and 4.6/4.7 lines are 200K/128K.
		ContextWindow: 1000000,
		MaxTokensCap:  131072,
		Models: []ModelSpec{
			{ID: "glm-5.3", ContextWindow: 1000000, MaxTokensCap: 131072},
			{ID: "glm-5.2", ContextWindow: 1000000, MaxTokensCap: 131072},
			{ID: "glm-5", ContextWindow: 200000, MaxTokensCap: 131072},
			{ID: "glm-4.6", ContextWindow: 200000, MaxTokensCap: 131072},
			{ID: "glm-4.7", ContextWindow: 200000, MaxTokensCap: 131072},
			{ID: "glm-4.5", ContextWindow: 128000, MaxTokensCap: 98304},
			{ID: "glm-4-long", ContextWindow: 1000000, MaxTokensCap: 4096},
			{ID: "glm-4", ContextWindow: 128000, MaxTokensCap: 16384},
		},
		ThinkingStyle:    "object",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 1.40, OutputPerMillion: 1.40},
		GeographicOrigin: RegionChina,
		ModelAliases:     []string{"glm-"},
	},
	{
		ID:               "qwen",
		Label:            "通义千问 (DashScope)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://dashscope.aliyuncs.com/compatible-mode/v1",
		ModelsPath:       "/models",
		DefaultModel:     "qwen3.7-plus",
		DefaultMaxTokens: 4096,
		// The qwen3.7/3.8 generation carries a 1M window; qwen3.8-max
		// publishes a 128K completion ceiling (131072).
		ContextWindow: 1000000,
		Models: []ModelSpec{
			{ID: "qwen3.8", ContextWindow: 1000000, MaxTokensCap: 131072},
			// Retired-generation ids keep their old envelopes.
			{ID: "qwen3-max", ContextWindow: 262144, MaxTokensCap: 65536},
			{ID: "qwen-plus", ContextWindow: 128000},
		},
		ThinkingStyle:    "flag",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 0.11, OutputPerMillion: 0.28},
		GeographicOrigin: RegionChina,
	},
	{
		ID:               "siliconflow",
		Label:            "硅基流动 (SiliconFlow)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://api.siliconflow.cn/v1",
		ModelsPath:       "/models",
		DefaultModel:     "deepseek-ai/DeepSeek-V4.1-Flash",
		DefaultMaxTokens: 4096,
		ContextWindow:    1048576,
		MaxTokensCap:     393216,
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 0.15, OutputPerMillion: 0.60},
		GeographicOrigin: RegionChina,
	},
	{
		ID:               "openrouter",
		Label:            "OpenRouter",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://openrouter.ai/api/v1",
		ModelsPath:       "/models",
		DefaultModel:     "anthropic/claude-sonnet-5.5",
		DefaultMaxTokens: 4096,
		ContextWindow:    1000000,
		MaxTokensCap:     131072,
		ThinkingStyle:    "effort",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 2.00, OutputPerMillion: 10.00},
		GeographicOrigin: RegionGlobal,
	},
	{
		ID:               "ollama",
		Label:            "Ollama (本地)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "http://localhost:11434/v1",
		NoAuth:           true,
		ModelsPath:       "/models",
		DefaultModel:     "qwen2.5-coder:14b",
		DefaultMaxTokens: 4096,
		PromptCache:      false,
		Pricing:          Pricing{InputPerMillion: 0, OutputPerMillion: 0},
		GeographicOrigin: RegionGlobal,
	},
	{
		// Relay stations / self-hosted gateways: the user supplies base_url,
		// picks the wire dialect (openai | anthropic) and key. ThinkingStyle is
		// resolved at request time from the chosen api_type (anthropic →
		// Anthropic object; openai → object), and a 400 naming the thinking
		// field retries once without it.
		ID:               "custom",
		Label:            "自定义 / 中转站 (custom base_url)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "",
		ModelsPath:       "",
		ThinkingStyle:    "object",
		PromptCache:      true,
		GeographicOrigin: RegionGlobal,
	},
}

// ThinkingStyleFor resolves the wire shape used to request thinking on a
// ModelConfig: the provider's declared style wins; a bare custom endpoint
// falls back to the dialect default (anthropic → "anthropic" object, openai →
// "object"). The entry client consults this at request-build time.
func ThinkingStyleFor(mc config.ModelConfig) string {
	if mc.NormalizedAPIType() == config.APITypeAnthropic {
		return "anthropic"
	}
	if p, ok := Lookup(mc.Provider); ok && p.ThinkingStyle != "" {
		return p.ThinkingStyle
	}
	// A custom endpoint behind a known vendor speaks that vendor's dialect —
	// the relay's URL or the served model name resolves it the same way the
	// client's tuning does.
	if p, ok := Detect(mc.BaseURL, mc.Model); ok && p.ThinkingStyle != "" {
		return p.ThinkingStyle
	}
	return "object"
}

// LookupPricing returns the Pricing for a given provider and model name.
func LookupPricing(providerID, model string) Pricing {
	p, ok := Lookup(providerID)
	if !ok {
		lower := strings.ToLower(model)
		switch {
		case strings.Contains(lower, "deepseek"):
			return Pricing{InputPerMillion: 0.14, OutputPerMillion: 0.28}
		case strings.Contains(lower, "minimax"):
			return Pricing{InputPerMillion: 0.30, OutputPerMillion: 1.20}
		case strings.Contains(lower, "claude") && strings.Contains(lower, "haiku"):
			return Pricing{InputPerMillion: 0.80, OutputPerMillion: 4.00}
		case strings.Contains(lower, "claude"):
			return Pricing{InputPerMillion: 2.00, OutputPerMillion: 10.00}
		case strings.Contains(lower, "gpt-6"):
			return Pricing{InputPerMillion: 2.00, OutputPerMillion: 10.00}
		case strings.Contains(lower, "gpt-5.4-mini"):
			return Pricing{InputPerMillion: 0.75, OutputPerMillion: 4.50}
		case strings.Contains(lower, "gpt-5.4"):
			return Pricing{InputPerMillion: 2.50, OutputPerMillion: 15.00}
		case strings.Contains(lower, "gpt-4o-mini"):
			return Pricing{InputPerMillion: 0.15, OutputPerMillion: 0.60}
		case strings.Contains(lower, "gpt-4o"):
			return Pricing{InputPerMillion: 2.50, OutputPerMillion: 10.00}
		case strings.Contains(lower, "qwen"):
			return Pricing{InputPerMillion: 0.11, OutputPerMillion: 0.28}
		}
		return Pricing{}
	}
	lower := strings.ToLower(model)
	if providerID == "openai" && (strings.Contains(lower, "gpt-4o") && !strings.Contains(lower, "mini")) {
		return Pricing{InputPerMillion: 2.50, OutputPerMillion: 10.00}
	}
	if providerID == "claude" && strings.Contains(lower, "haiku") {
		return Pricing{InputPerMillion: 0.80, OutputPerMillion: 4.00}
	}
	return p.Pricing
}

// SpecFor resolves the model spec matching a model id: an exact id wins,
// else the longest matching prefix (a versioned id like
// "MiniMax-M3.1-Flash-Preview" lands on its family row). Reports false when
// the catalogue knows no per-model row for it.
func (p Provider) SpecFor(model string) (ModelSpec, bool) {
	var best ModelSpec
	for _, s := range p.Models {
		if model == s.ID {
			return s, true
		}
		if strings.HasPrefix(model, s.ID) && len(s.ID) > len(best.ID) {
			best = s
		}
	}
	return best, best.ID != ""
}

// ContextFor resolves the advertised context window for a model id: the
// matching spec's window when one exists, else the provider-level value.
func (p Provider) ContextFor(model string) int {
	if s, ok := p.SpecFor(model); ok && s.ContextWindow > 0 {
		return s.ContextWindow
	}
	return p.ContextWindow
}

// TokenPolicy resolves the catalogue-side completion-cap policy for a
// model: the vendor's hard ceiling and the curated default — spec values
// win over the provider row. Callers lift the default toward the resolved
// context window and clamp it to the ceiling.
func (p Provider) TokenPolicy(model string) (autoDefault, cap int) {
	autoDefault, cap = p.DefaultMaxTokens, p.MaxTokensCap
	if s, ok := p.SpecFor(model); ok {
		if s.MaxTokensCap > 0 {
			cap = s.MaxTokensCap
		}
		if s.DefaultMaxTokens > 0 {
			autoDefault = s.DefaultMaxTokens
		}
	}
	return autoDefault, cap
}

// AutoMaxTokens resolves the completion cap this provider would auto-load
// for a model: the TokenPolicy default lifted to an eighth of the resolved
// context window (a 1M window lands on 128K, MiniMax's own recommended
// value), clamped by the vendor ceiling. 0 when the catalogue has nothing
// to say.
func (p Provider) AutoMaxTokens(model string) int {
	mt, cap := p.TokenPolicy(model)
	if ctx := p.ContextFor(model); ctx > 0 && ctx/8 > mt {
		mt = ctx / 8
	}
	if cap > 0 && mt > cap {
		mt = cap
	}
	return mt
}

// All returns the catalogue in display order.
func All() []Provider { return append([]Provider(nil), builtins...) }

// Lookup returns the provider with the given id, or false when unknown.
func Lookup(id string) (Provider, bool) {
	for _, p := range builtins {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Detect infers a built-in provider from the endpoint URL or model name.
func Detect(baseURL, model string) (Provider, bool) {
	b := strings.TrimRight(strings.ToLower(baseURL), "/")
	m := strings.ToLower(model)
	for _, p := range builtins {
		if p.ID == "custom" {
			continue
		}
		if p.BaseURL != "" && (b == strings.TrimRight(strings.ToLower(p.BaseURL), "/") || strings.Contains(b, p.ID)) {
			return p, true
		}
	}
	for _, p := range builtins {
		if p.ID == "custom" {
			continue
		}
		if m == "" {
			continue
		}
		if strings.HasPrefix(m, p.ID) {
			return p, true
		}
		for _, a := range p.ModelAliases {
			if strings.HasPrefix(m, a) {
				return p, true
			}
		}
	}
	return Provider{}, false
}

// ModelConfig builds the config.ModelConfig that "add <provider> <key>"
// produces: the provider's endpoint, dialect and tuning pre-filled, the key
// attached, and the model id resolved (explicit model wins over the default).
func ModelConfig(id, model, key string) (config.ModelConfig, bool) {
	p, ok := Lookup(id)
	if !ok {
		return config.ModelConfig{}, false
	}
	m := model
	if m == "" {
		m = p.DefaultModel
	}
	// Bake the resolved token policy into the written config — the same
	// value the entry client would auto-load for this model.
	mc := config.ModelConfig{
		Provider:      p.ID,
		APIType:       p.APIType,
		BaseURL:       p.BaseURL,
		APIKey:        key,
		Model:         m,
		MaxTokens:     p.AutoMaxTokens(m),
		ContextWindow: p.ContextFor(m),
	}
	return mc, true
}

// DetectRegion determines the geographic region (RegionChina or RegionGlobal)
// from a provider ID, model name, or agent descriptor string (e.g., "deepseek-chat",
// "claude-sonnet-5-5", "qwen-max", "doubao-pro", "agent:claude_code", etc.).
func DetectRegion(modelOrProvider string) GeoRegion {
	s := strings.ToLower(strings.TrimSpace(modelOrProvider))
	if s == "" {
		return RegionGlobal
	}

	// 1. Direct provider match if it's a known provider ID
	if p, ok := Lookup(s); ok && p.GeographicOrigin != "" {
		return p.GeographicOrigin
	}

	// 2. Known Chinese models / providers keywords
	chineseKeywords := []string{
		"deepseek",
		"qwen",
		"kimi",
		"moonshot",
		"zhipu",
		"glm",
		"doubao",
		"volcengine",
		"baichuan",
		"minimax",
		"internlm",
		"hunyuan",
		"ernie",
		"wenxin",
		"spark",
		"siliconflow",
		"stepfun",
		"step-",
		"yi-",
		"01-ai",
		"lingyi",
	}

	for _, kw := range chineseKeywords {
		if strings.Contains(s, kw) {
			return RegionChina
		}
	}

	// 3. Known Global models / providers keywords
	globalKeywords := []string{
		"claude",
		"anthropic",
		"openai",
		"gpt",
		"o1-",
		"o3-",
		"o4-",
		"codex",
		"gemini",
		"google",
		"llama",
		"meta",
		"mistral",
		"cohere",
		"openrouter",
		"groq",
	}

	for _, kw := range globalKeywords {
		if strings.Contains(s, kw) {
			return RegionGlobal
		}
	}

	return RegionGlobal
}
