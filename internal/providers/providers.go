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
	// PromptCache toggles provider-native prompt-cache markers (Anthropic
	// cache_control / OpenAI prompt_cache_key). Most vendors honour them; a
	// strict legacy relay may not, so it is per-vendor tunable.
	PromptCache bool
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
		PromptCache:      true,
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
		PromptCache:      true,
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
		PromptCache:      true,
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
		PromptCache:      true,
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
		PromptCache:      true,
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
		PromptCache:      true,
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
	},
	{
		ID:          "custom",
		Label:       "自定义 (base model)",
		APIType:     config.APITypeOpenAI,
		BaseURL:     "",
		ModelsPath:  "",
		PromptCache: true,
	},
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
