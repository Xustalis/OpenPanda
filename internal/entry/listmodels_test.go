package entry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestListModelsOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth = %q, want Bearer sk-test", got)
		}
		w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"o3-mini"}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{APIType: config.APITypeOpenAI, BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 2 || models[0].ID != "gpt-4o" || models[1].ID != "o3-mini" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListModelsAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "sk-test" {
			t.Errorf("x-api-key = %q, want sk-test", got)
		}
		w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-5"}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet-4-5" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListModelsRequiresKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.ListModels(context.Background()); err != ErrNoKey {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

func TestListModelsNoAuthProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:14b"}]}`))
	}))
	defer srv.Close()

	// Ollama is no-auth: an empty key must not be refused.
	c, err := NewClient(config.ModelConfig{Provider: "ollama", APIType: config.APITypeOpenAI, BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen2.5-coder:14b" {
		t.Fatalf("models = %+v", models)
	}
}

func TestProviderTuningApplied(t *testing.T) {
	// DeepSeek: thinking-passback should be pre-armed, so the first multi-turn
	// call does not spend a rejected round-trip.
	c, err := NewClient(config.ModelConfig{Provider: "deepseek", BaseURL: "https://api.deepseek.com/anthropic", APIKey: "sk", Model: "deepseek-v4-flash"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if !c.passback.Load() {
		t.Fatal("expected deepseek passback pre-armed")
	}
	if c.modelsURL != "https://api.deepseek.com/models" {
		t.Fatalf("modelsURL = %q", c.modelsURL)
	}
}

func TestListModelsEndpointAuthStyle(t *testing.T) {
	// Claude provider: Anthropic surface with /v1/models must use x-api-key (bearer false)
	cClaude, err := NewClient(config.ModelConfig{Provider: "claude", BaseURL: "https://api.anthropic.com", APIKey: "sk-ant"})
	if err != nil {
		t.Fatalf("new claude client: %v", err)
	}
	url, bearer := cClaude.listModelsEndpoint()
	if bearer {
		t.Errorf("claude provider should not use bearer auth")
	}
	if url != "https://api.anthropic.com/v1/models" {
		t.Errorf("claude modelsURL = %q, want https://api.anthropic.com/v1/models", url)
	}

	// DeepSeek provider: list-models lives on OpenAI surface, must use bearer auth
	cDeepSeek, err := NewClient(config.ModelConfig{Provider: "deepseek", BaseURL: "https://api.deepseek.com/anthropic", APIKey: "sk-ds"})
	if err != nil {
		t.Fatalf("new deepseek client: %v", err)
	}
	url, bearer = cDeepSeek.listModelsEndpoint()
	if !bearer {
		t.Errorf("deepseek provider should use bearer auth on models endpoint")
	}
	if url != "https://api.deepseek.com/models" {
		t.Errorf("deepseek modelsURL = %q, want https://api.deepseek.com/models", url)
	}

	// OpenAI provider: must use bearer auth
	cOpenAI, err := NewClient(config.ModelConfig{Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-oai"})
	if err != nil {
		t.Fatalf("new openai client: %v", err)
	}
	url, bearer = cOpenAI.listModelsEndpoint()
	if !bearer {
		t.Errorf("openai provider should use bearer auth")
	}
	if url != "https://api.openai.com/v1/models" {
		t.Errorf("openai modelsURL = %q, want https://api.openai.com/v1/models", url)
	}
}

func TestNewClientAPITypeNormalization(t *testing.T) {
	// Mixed-case or spaced "OpenAI" should normalize to openai, not fallback to anthropic
	c, err := NewClient(config.ModelConfig{APIType: " OpenAI ", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.apiType != config.APITypeOpenAI {
		t.Errorf("c.apiType = %q, want %q", c.apiType, config.APITypeOpenAI)
	}
}
