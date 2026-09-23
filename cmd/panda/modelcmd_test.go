package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestModelCmdEffectiveHelpers(t *testing.T) {
	// 1. Fully populated
	mc1 := config.ModelConfig{
		Name:          "fast",
		Provider:      "deepseek",
		Model:         "deepseek-v4-flash",
		BaseURL:       "https://api.deepseek.com/anthropic",
		ContextWindow: 128000,
	}
	if effectiveModel(mc1) != "deepseek-v4-flash" {
		t.Fatalf("expected deepseek-v4-flash, got %s", effectiveModel(mc1))
	}
	if effectiveBaseURL(mc1) != "https://api.deepseek.com/anthropic" {
		t.Fatalf("unexpected baseURL: %s", effectiveBaseURL(mc1))
	}
	if effectiveProvider(mc1) != "deepseek" {
		t.Fatalf("expected deepseek, got %s", effectiveProvider(mc1))
	}
	if effectiveContextWindow(mc1) != 128000 {
		t.Fatalf("expected 128000, got %d", effectiveContextWindow(mc1))
	}

	// 2. Provider set, but Model and BaseURL omitted (inheriting from provider)
	mc2 := config.ModelConfig{
		Provider: "claude",
	}
	if effectiveModel(mc2) != "claude-sonnet-4-5" {
		t.Fatalf("expected claude default model, got %s", effectiveModel(mc2))
	}
	if effectiveBaseURL(mc2) != "https://api.anthropic.com" {
		t.Fatalf("expected claude default baseURL, got %s", effectiveBaseURL(mc2))
	}
	if effectiveContextWindow(mc2) != 200000 {
		t.Fatalf("expected 200000, got %d", effectiveContextWindow(mc2))
	}

	// 3. Provider omitted, but BaseURL and Model present (auto-detect)
	mc3 := config.ModelConfig{
		BaseURL: "https://api.deepseek.com/anthropic",
		Model:   "deepseek-v4-flash",
	}
	if effectiveProvider(mc3) != "deepseek" {
		t.Fatalf("expected detected deepseek, got %s", effectiveProvider(mc3))
	}
	if effectiveContextWindow(mc3) != 128000 {
		t.Fatalf("expected inferred 128000, got %d", effectiveContextWindow(mc3))
	}
}

func TestLooksLikeAPIKey(t *testing.T) {
	if !looksLikeAPIKey("sk-ant-api03-abcdef12345678901234567890") {
		t.Error("expected sk- to be recognized as API key")
	}
	if looksLikeAPIKey("deepseek-reasoner") {
		t.Error("model name should not be recognized as API key")
	}
	if looksLikeAPIKey("qwen2.5-coder:14b") {
		t.Error("ollama tag should not be recognized as API key")
	}
}

func TestModelCmdRunModelStatus(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	yaml := `node:
  name: "test-node"
model:
  provider: "deepseek"
  model: "deepseek-v4-flash"
  api_key: "sk-test-key"
models:
  - name: "claude-primary"
    provider: "claude"
    model: "claude-sonnet-4-5"
    api_key: "sk-ant-test"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}

	// Test status doesn't panic
	runModel([]string{"--config", cfgPath, "status"})
	// Test list doesn't panic
	runModel([]string{"--config", cfgPath, "list"})
}

func TestSanitizeProjectName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"normal", "normal"},
		{"proj-1_2", "proj-1_2"},
		{"中文项目", "中文项目"},
		{"日本語プロジェクト", "日本語プロジェクト"},
		{"my/project", "my_project"},
		{"project:name", "project_name"},
		{"foo*bar?baz", "foo_bar_baz"},
		{"", "project"},
		{".", "project"},
		{"..", "project"},
	}

	for _, tc := range cases {
		got := sanitizeProjectName(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeProjectName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestModelAddKeyAndAlias(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{
		Node: config.NodeConfig{Name: "test"},
	}
	r := &repl{
		cfg:        cfg,
		configPath: cfgPath,
	}

	// 3 args: provider, key, alias (omitting model); --force skips the
	// connectivity probe so the test needs no network.
	r.modelAdd([]string{"deepseek", "sk-test-00000000000000000000000000", "my-alias", "--force"})
	if len(r.cfg.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(r.cfg.Models))
	}
	m := r.cfg.Models[0]
	if m.Name != "my-alias" {
		t.Errorf("m.Name = %q, want my-alias", m.Name)
	}
	if m.APIKey != "sk-test-00000000000000000000000000" {
		t.Errorf("m.APIKey = %q, want sk-...", m.APIKey)
	}
	if m.Model != "deepseek-v4-flash" {
		t.Errorf("m.Model = %q, want provider default deepseek-v4-flash", m.Model)
	}
	// Because no active model was configured, it should have been auto-applied as active
	if r.cfg.Model.Alias() != "my-alias" {
		t.Errorf("active model alias = %q, want my-alias", r.cfg.Model.Alias())
	}
}

func TestModelSwitchPrefersAliasMatch(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{
		Node: config.NodeConfig{Name: "test"},
		Model: config.ModelConfig{
			Name:     "primary",
			Provider: "deepseek",
			Model:    "deepseek-v4-flash",
			BaseURL:  "https://api.deepseek.com/anthropic",
			APIKey:   "sk-test",
		},
		Models: []config.ModelConfig{
			{
				Name:     "primary",
				Provider: "deepseek",
				Model:    "deepseek-v4-flash",
				BaseURL:  "https://api.deepseek.com/anthropic",
				APIKey:   "sk-test",
			},
			{
				Name:     "backup-relay",
				Provider: "deepseek",
				Model:    "deepseek-v4-flash",
				BaseURL:  "https://my-relay.com/anthropic",
				APIKey:   "sk-relay",
			},
		},
	}
	r := &repl{
		cfg:        cfg,
		configPath: cfgPath,
	}

	r.modelSwitch("backup-relay")
	if r.cfg.Model.Name != "backup-relay" {
		t.Fatalf("expected switch to backup-relay, got %q", r.cfg.Model.Name)
	}
	if r.cfg.Model.BaseURL != "https://my-relay.com/anthropic" {
		t.Fatalf("expected relay baseURL, got %q", r.cfg.Model.BaseURL)
	}
}

// TestModelAddCustomVerifies exercises the connectivity probe: a custom relay
// entry is only persisted when the endpoint actually answers. The httptest
// server speaks the OpenAI dialect; the second half proves a dead endpoint is
// rejected (and --force bypasses the probe).
func TestModelAddCustomVerifies(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{Node: config.NodeConfig{Name: "test"}}
	r := &repl{cfg: cfg, configPath: cfgPath}

	// Live endpoint: one-shot OpenAI-style completion.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer live.Close()

	r.modelAdd([]string{"custom", live.URL, "gpt-relay", "sk-anything"})
	if len(r.cfg.Models) != 1 {
		t.Fatalf("verified custom model should persist, got %d entries", len(r.cfg.Models))
	}
	m := r.cfg.Models[0]
	if m.Provider != "custom" || m.BaseURL != live.URL || m.Model != "gpt-relay" {
		t.Fatalf("unexpected stored config: %+v", m)
	}
	if m.NormalizedAPIType() != config.APITypeOpenAI {
		t.Fatalf("custom default should be openai dialect, got %q", m.APIType)
	}

	// Dead endpoint: nothing is persisted without --force.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	dead.Close() // connection refused — the probe must fail fast

	before := len(r.cfg.Models)
	r.modelAdd([]string{"custom", dead.URL, "x", "sk-anything"})
	if len(r.cfg.Models) != before {
		t.Fatalf("failed probe must not add a model, got %d entries", len(r.cfg.Models))
	}

	// --force bypasses the probe and stores the entry.
	r.modelAdd([]string{"custom", dead.URL, "x", "sk-anything", "--force"})
	if len(r.cfg.Models) != before+1 {
		t.Fatalf("--force should persist despite the failed probe, got %d", len(r.cfg.Models))
	}
}

// TestModelTestCustomEmptyModel: `model test custom <url>` with no model name
// must probe the first advertised model rather than submit an empty model.
func TestModelTestCustomEmptyModel(t *testing.T) {
	dir := t.TempDir()
	r := &repl{cfg: &config.Config{Node: config.NodeConfig{Name: "test"}}, configPath: filepath.Join(dir, "config.yaml")}

	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.HasSuffix(req.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"picked-model"}]}`)
			return
		}
		body, _ := io.ReadAll(req.Body)
		var reqMap map[string]any
		_ = json.Unmarshal(body, &reqMap)
		gotModel, _ = reqMap["model"].(string)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer srv.Close()

	r.modelTest([]string{"custom", srv.URL})
	if gotModel != "picked-model" {
		t.Fatalf("expected the probe to use the advertised model, got %q", gotModel)
	}
}
