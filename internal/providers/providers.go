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
	// ContextWindow is the advertised context length in tokens; 0 = unknown.
	ContextWindow int
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
		ModelsPath:       "https://api.deepseek.com/models",
		DefaultModel:     "deepseek-v4-flash",
		DefaultMaxTokens: 4096,
		ContextWindow:    128000,
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
		DefaultModel:     "claude-sonnet-4-5",
		DefaultMaxTokens: 8192,
		ContextWindow:    200000,
		ThinkingPassback: true,
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 3.00, OutputPerMillion: 15.00},
		GeographicOrigin: RegionGlobal,
	},
	{
		ID:               "openai",
		Label:            "ChatGPT (OpenAI)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://api.openai.com/v1",
		ModelsPath:       "/models",
		DefaultModel:     "gpt-4o-mini",
		DefaultMaxTokens: 4096,
		ContextWindow:    128000,
		ThinkingStyle:    "effort",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 0.15, OutputPerMillion: 0.60},
		GeographicOrigin: RegionGlobal,
	},
	{
		ID:               "kimi",
		Label:            "Kimi (月之暗面)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://api.moonshot.cn/v1",
		ModelsPath:       "/models",
		DefaultModel:     "kimi-latest",
		DefaultMaxTokens: 4096,
		ContextWindow:    128000,
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 1.40, OutputPerMillion: 1.40},
		GeographicOrigin: RegionChina,
	},
	{
		ID:               "volcengine",
		Label:            "火山引擎 (Ark/豆包)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://ark.cn-beijing.volces.com/api/v3",
		ModelsPath:       "/models",
		DefaultModel:     "doubao-1-5-pro-32k-250115",
		DefaultMaxTokens: 4096,
		ContextWindow:    32000,
		ThinkingStyle:    "object",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 0.11, OutputPerMillion: 0.28},
		GeographicOrigin: RegionChina,
	},
	{
		ID:               "zhipu",
		Label:            "智谱 GLM",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://open.bigmodel.cn/api/paas/v4",
		ModelsPath:       "/models",
		DefaultModel:     "glm-4-plus",
		DefaultMaxTokens: 4096,
		ContextWindow:    128000,
		ThinkingStyle:    "object",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 1.40, OutputPerMillion: 1.40},
		GeographicOrigin: RegionChina,
	},
	{
		ID:               "qwen",
		Label:            "通义千问 (DashScope)",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://dashscope.aliyuncs.com/compatible-mode/v1",
		ModelsPath:       "/models",
		DefaultModel:     "qwen-plus",
		DefaultMaxTokens: 4096,
		ContextWindow:    128000,
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
		DefaultModel:     "deepseek-ai/DeepSeek-V3",
		DefaultMaxTokens: 4096,
		ContextWindow:    64000,
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 0.14, OutputPerMillion: 0.28},
		GeographicOrigin: RegionChina,
	},
	{
		ID:               "openrouter",
		Label:            "OpenRouter",
		APIType:          config.APITypeOpenAI,
		BaseURL:          "https://openrouter.ai/api/v1",
		ModelsPath:       "/models",
		DefaultModel:     "anthropic/claude-3.5-sonnet",
		DefaultMaxTokens: 4096,
		ContextWindow:    200000,
		ThinkingStyle:    "effort",
		PromptCache:      true,
		Pricing:          Pricing{InputPerMillion: 3.00, OutputPerMillion: 15.00},
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
		case strings.Contains(lower, "claude") && strings.Contains(lower, "haiku"):
			return Pricing{InputPerMillion: 0.80, OutputPerMillion: 4.00}
		case strings.Contains(lower, "claude"):
			return Pricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}
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
		if m != "" && strings.HasPrefix(m, p.ID) {
			return p, true
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
	mc := config.ModelConfig{
		Provider:      p.ID,
		APIType:       p.APIType,
		BaseURL:       p.BaseURL,
		APIKey:        key,
		Model:         m,
		MaxTokens:     p.DefaultMaxTokens,
		ContextWindow: p.ContextWindow,
	}
	return mc, true
}

// DetectRegion determines the geographic region (RegionChina or RegionGlobal)
// from a provider ID, model name, or agent descriptor string (e.g., "deepseek-chat",
// "claude-sonnet-4-5", "qwen-max", "doubao-pro", "agent:claude_code", etc.).
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
