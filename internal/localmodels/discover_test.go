// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCodexToml(t *testing.T) {
	text := `
model = "gpt-6.1-sol"
model_reasoning_effort = "ultra"

[mcp_servers.obsidian]
command = 'python.exe'
args = ['server.py']  # a comment with # inside is still a comment

[model_providers.custom]
name = "custom"
base_url = "http://127.0.0.1:7896/v1"
wire_api = "responses"
experimental_bearer_token = "PROXY_MANAGED"

[model_providers."relay-2"]
base_url = "https://relay.example/v1"
env_key = "RELAY_KEY"

[model_providers.custom.env]
IGNORED = "1"
`
	got := parseCodexToml(text)
	if got.Model != "gpt-6.1-sol" {
		t.Fatalf("model = %q", got.Model)
	}
	if got.ModelProvider != "" {
		t.Fatalf("model_provider should stay empty, got %q", got.ModelProvider)
	}
	if len(got.Providers) != 2 {
		t.Fatalf("providers = %+v", got.Providers)
	}
	custom := got.Providers[0]
	if custom.ID != "custom" || custom.BaseURL != "http://127.0.0.1:7896/v1" ||
		custom.WireAPI != "responses" || custom.Bearer != "PROXY_MANAGED" {
		t.Fatalf("custom provider = %+v", custom)
	}
	relay := got.Providers[1]
	if relay.ID != "relay-2" || relay.EnvKey != "RELAY_KEY" {
		t.Fatalf("quoted provider = %+v", relay)
	}
}

func TestCleanModelID(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		window int
	}{
		{"claude-opus-5[1M]", "claude-opus-5", 1_000_000},
		{"claude-sonnet-5", "claude-sonnet-5", 0},
		{" gpt-6.1-sol ", "gpt-6.1-sol", 0},
		{"qwen3-max[128k]", "qwen3-max", 0},
		{"has space", "", 0},
		{"", "", 0},
	}
	for _, c := range cases {
		id, window := cleanModelID(c.in)
		if id != c.want || window != c.window {
			t.Errorf("cleanModelID(%q) = (%q, %d), want (%q, %d)", c.in, id, window, c.want, c.window)
		}
	}
}

func TestDecodeModelRecordsShapes(t *testing.T) {
	for _, raw := range []string{
		`["a-1","b-2"]`,
		`{"data":[{"id":"a-1"},{"id":"b-2"}]}`,
		`{"models":[{"slug":"a-1"},{"name":"b-2"}]}`,
	} {
		ids := decodeModelRecords([]byte(raw))
		if len(ids) != 2 || ids[0].id != "a-1" || ids[1].id != "b-2" {
			t.Fatalf("decodeModelRecords(%s) = %+v", raw, ids)
		}
	}
}

func TestDedupeKeepsTheCredential(t *testing.T) {
	sources := []Source{
		{ID: SourceCodex, Candidates: []Candidate{
			{Source: SourceCodex, Provider: "custom", Model: "m", BaseURL: "http://127.0.0.1:7896/v1/", Importable: true},
		}},
		{ID: SourceClaude, Candidates: []Candidate{
			{Source: SourceClaude, Provider: "custom", Model: "m", BaseURL: "http://127.0.0.1:7896/v1", KeyRef: "claude:settings", Importable: true, ContextWindow: 1_000_000},
		}},
	}
	out := dedupe(sources)
	if n := len(out[0].Candidates); n != 1 {
		t.Fatalf("codex candidates = %d, want 1", n)
	}
	merged := out[0].Candidates[0]
	if merged.KeyRef != "claude:settings" {
		t.Errorf("key ref not carried over: %q", merged.KeyRef)
	}
	if merged.ContextWindow != 1_000_000 {
		t.Errorf("context window not carried over: %d", merged.ContextWindow)
	}
	if len(out[1].Candidates) != 0 {
		t.Errorf("duplicate left in the second source: %+v", out[1].Candidates)
	}
	if merged.ID == "" {
		t.Error("candidate id not assigned")
	}
}

func TestCollectClaudeFromSettingsFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"env":{
		"ANTHROPIC_BASE_URL":"http://127.0.0.1:7896",
		"ANTHROPIC_AUTH_TOKEN":"PROXY_MANAGED",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":"claude-opus-5[1M]",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":"claude-haiku-5-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":"racc"
	}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Home: home, NoProbe: true, Getenv: func(string) string { return "" }}
	src := collectClaude(context.Background(), opt)
	if src.Status != StatusOK {
		t.Fatalf("status = %s (%s)", src.Status, src.Detail)
	}
	if len(src.Candidates) != 2 {
		t.Fatalf("candidates = %+v", src.Candidates)
	}
	opus := src.Candidates[0]
	// The [1M] suffix stays in the id: it is what selects the window, and the
	// reference implementation keeps it too. The window is recorded separately.
	if opus.Model != "claude-opus-5[1M]" || opus.ContextWindow != 1_000_000 {
		t.Errorf("opus row = %+v", opus)
	}
	if opus.BaseURL != "http://127.0.0.1:7896" || opus.APIType != "anthropic" {
		t.Errorf("opus endpoint = %+v", opus)
	}
	if opus.KeyRef != "claude:settings" {
		t.Errorf("key ref = %q", opus.KeyRef)
	}
}

func TestCollectCodexWithoutConfig(t *testing.T) {
	opt := Options{Home: t.TempDir(), NoProbe: true, Getenv: func(string) string { return "" }}
	src := collectCodex(context.Background(), opt)
	if src.Status != StatusMissing {
		t.Fatalf("status = %s (%s)", src.Status, src.Detail)
	}
}

// A config that names a model and a provider the model does not belong to is
// the case that used to make the row disappear: Codex defaults model_provider
// to "openai", so `model = "gpt-6.1-sol"` is not attached to the one relay the
// file happens to declare — and the sheet then showed neither the relay's
// models nor the one the person was running.
func TestCollectCodexListsTheModelTheConfigRuns(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
model = "gpt-6.1-sol"

[model_providers.custom]
name = "custom"
base_url = "http://127.0.0.1:7896/v1"
wire_api = "responses"
experimental_bearer_token = "PROXY_MANAGED"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Home: home, NoProbe: true, Getenv: func(string) string { return "" }}
	src := collectCodex(context.Background(), opt)
	if len(src.Candidates) != 1 {
		t.Fatalf("candidates = %+v", src.Candidates)
	}
	row := src.Candidates[0]
	if row.Model != "gpt-6.1-sol" || row.Importable || row.Note != "cli-login" {
		t.Fatalf("row = %+v", row)
	}
	if !strings.Contains(src.Detail, "none importable") {
		t.Errorf("detail does not say nothing can be imported: %q", src.Detail)
	}
}

// A model pointed at a provider that was declared without an endpoint is a
// different answer from one that goes through the CLI's login, and the sheet
// has to say which.
func TestCollectCodexModelOfAnEndpointlessProvider(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
model = "relay-model"
model_provider = "relay"

[model_providers.relay]
name = "relay"
env_key = "RELAY_KEY"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Home: home, NoProbe: true, Getenv: func(string) string { return "" }}
	src := collectCodex(context.Background(), opt)
	if len(src.Candidates) != 1 || src.Candidates[0].Note != "no-endpoint" {
		t.Fatalf("candidates = %+v", src.Candidates)
	}
}

// The desktop app writes the build that owns the file into its own MCP block.
// It is the build whose roster matches the app the person is looking at, so it
// is tried before whatever `codex` happens to be on PATH.
// The whole point of the Codex source: a machine with a ChatGPT login can put
// its account models in the registry, because the CLI's token opens Codex's own
// Responses backend. The row names `auth: codex` instead of a key — the
// credential stays in ~/.codex/auth.json and is read per call.
func TestCollectCodexOffersTheAccountModelsWhenLoggedIn(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
model = "gpt-6.1-sol"

[model_providers.custom]
name = "custom"
base_url = "http://127.0.0.1:7896/v1"
wire_api = "responses"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"fake","account_id":"acct"}}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Home: home, NoProbe: true, Getenv: func(string) string { return "" }}
	src := collectCodex(context.Background(), opt)
	if len(src.Candidates) != 1 {
		t.Fatalf("candidates = %+v", src.Candidates)
	}
	row := src.Candidates[0]
	if !row.Importable || row.Auth != "codex" || row.BaseURL != codexResponsesURL {
		t.Fatalf("row = %+v", row)
	}
	if !row.Suggested {
		t.Error("the configured model should be ticked by default")
	}
	if strings.Contains(src.Detail, "none importable") {
		t.Errorf("detail = %q", src.Detail)
	}
}

// The same file without a login: the rows stay visible but cannot be imported,
// and the sheet says which of the two reasons applies.
func TestCollectCodexWithoutLoginKeepsTheRowsGreyed(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "model = \"gpt-6.1-sol\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Home: home, NoProbe: true, Getenv: func(string) string { return "" }}
	src := collectCodex(context.Background(), opt)
	if len(src.Candidates) != 1 {
		t.Fatalf("candidates = %+v", src.Candidates)
	}
	if src.Candidates[0].Importable || src.Candidates[0].Note != "cli-login" {
		t.Fatalf("row = %+v", src.Candidates[0])
	}
}

// A login row must not be written as no-auth: the endpoint needs the Bearer,
// and no_auth would tell the UI (and the health check) that it does not.
func TestCodexLoginRowKeepsItsCredential(t *testing.T) {
	opt := Options{Getenv: func(string) string { return "" }}
	mc, err := Candidate{
		Provider: "codex", Model: "gpt-6.1-sol", APIType: "responses",
		BaseURL: codexResponsesURL, Auth: "codex", Importable: true,
	}.Row(opt)
	if err != nil {
		t.Fatal(err)
	}
	if mc.NoAuth {
		t.Error("row marked no-auth although it authenticates with a token")
	}
	if mc.Auth != "codex" || mc.APIKey != "" || mc.KeyEnv != "" {
		t.Errorf("row = %+v", mc)
	}
}

func TestParseCodexTomlCapturesCLIPath(t *testing.T) {
	want := `C:\Users\x\AppData\Local\OpenAI\Codex\bin\abc\codex.exe`
	got := parseCodexToml(`
model = "gpt-6.1-sol"

[mcp_servers.node_repl.env]
CODEX_CLI_PATH = '` + want + `'
`)
	if got.CLIPath != want {
		t.Fatalf("cli path = %q, want %q", got.CLIPath, want)
	}
}

func TestCodexCLIsPrefersTheBuildTheConfigNames(t *testing.T) {
	home := t.TempDir()
	cli := filepath.Join(home, "codex.exe")
	if err := os.WriteFile(cli, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Getenv: func(string) string { return "" }}
	got := codexCLIs(opt, cli)
	if len(got) == 0 || got[0] != cli {
		t.Fatalf("cli order = %v, want %q first", got, cli)
	}
	// A path the config mentions but that no longer exists must not be tried.
	missing := filepath.Join(home, "gone.exe")
	for _, p := range codexCLIs(opt, missing) {
		if p == missing {
			t.Fatalf("missing build kept in the list: %v", codexCLIs(opt, missing))
		}
	}
}

func TestCandidateRowUsesCatalogueForKnownProvider(t *testing.T) {
	opt := Options{Getenv: func(string) string { return "" }}
	mc, err := Candidate{Provider: "qwen", Model: "qwen3-max", APIType: "openai", Importable: true}.Row(opt)
	if err != nil {
		t.Fatal(err)
	}
	if mc.BaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
		t.Errorf("base = %q", mc.BaseURL)
	}
	if mc.Provider != "qwen" || mc.Model != "qwen3-max" {
		t.Errorf("row = %+v", mc)
	}
}

func TestCandidateRowKeepsEnvKeyAsReference(t *testing.T) {
	opt := Options{Getenv: func(k string) string {
		if k == "DASHSCOPE_API_KEY" {
			return "sk-test"
		}
		return ""
	}}
	mc, err := Candidate{
		Provider: "qwen", Model: "qwen3-max", APIType: "openai",
		KeyRef: "env:DASHSCOPE_API_KEY", Importable: true,
	}.Row(opt)
	if err != nil {
		t.Fatal(err)
	}
	// The value is not copied into the row; the reference is. entry.NewClient
	// resolves it at call time.
	if mc.APIKey != "" || mc.KeyEnv != "DASHSCOPE_API_KEY" || mc.NoAuth {
		t.Errorf("key = %q keyEnv = %q noAuth = %v", mc.APIKey, mc.KeyEnv, mc.NoAuth)
	}
}

func TestLocalStateParsesOllamaPS(t *testing.T) {
	// The payload shape, not the transport: /api/ps is the only endpoint that
	// reports a context length, and the base-id fallback is what makes a row
	// for "qwen3" get the window of "qwen3:14b".
	body := []byte(`{"models":[{"model":"qwen3:14b","context_length":32768}]}`)
	var payload struct {
		Models []localRow `json:"models"`
	}
	if err := jsonUnmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 1 || payload.Models[0].ContextLength != 32768 {
		t.Fatalf("payload = %+v", payload.Models)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:7896":     true,
		"http://127.0.0.1:7896/v1":  true,
		"http://localhost:11434/v1": true,
		"http://[::1]:8080/v1":      true,
		"http://192.168.1.9:11434":  false,
		"https://api.anthropic.com": false,
		"":                          false,
	}
	for base, want := range cases {
		if got := IsLoopback(base); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", base, got, want)
		}
	}
}

func TestDefiniteRejection(t *testing.T) {
	yes := []string{
		"entry: api status 404: {\"error\":{\"code\":\"model_not_found\"}}",
		"entry: api status 401: unauthorized",
		"model does not exist",
	}
	no := []string{
		"entry: api status 429: rate limited",
		"entry: api status 500: boom",
		"context deadline exceeded",
		"dial tcp 127.0.0.1:7896: connectex: connection refused",
	}
	for _, msg := range yes {
		if !definiteRejection(errString(msg)) {
			t.Errorf("expected a rejection for %q", msg)
		}
	}
	for _, msg := range no {
		if definiteRejection(errString(msg)) {
			t.Errorf("did not expect a rejection for %q", msg)
		}
	}
}

func TestDedupePrefersTheLocalProbe(t *testing.T) {
	// A config row and a live loopback server for the same id: the probe wins,
	// because only it knows the model is resident and what window the server
	// actually reports.
	sources := []Source{
		{ID: SourceClaude, Candidates: []Candidate{{
			Source: SourceClaude, Provider: "custom", Model: "m",
			BaseURL: "http://127.0.0.1:11434/v1", Importable: true, KeyRef: "claude:settings",
		}}},
		{ID: SourceLocal, Candidates: []Candidate{{
			Source: SourceLocal, Provider: "ollama", Model: "m",
			BaseURL: "http://127.0.0.1:11434/v1", Importable: true, NoAuth: true,
			Loaded: true, ContextWindow: 32768,
		}}},
	}
	out := dedupe(sources)
	if len(out[0].Candidates) != 0 {
		t.Fatalf("config row should have lost, got %+v", out[0].Candidates)
	}
	if len(out[1].Candidates) != 1 {
		t.Fatalf("local row missing: %+v", out[1].Candidates)
	}
	got := out[1].Candidates[0]
	if !got.Loaded || got.ContextWindow != 32768 || got.Provider != "ollama" {
		t.Errorf("local row not preserved: %+v", got)
	}
}

func TestRowStoresEnvReferenceNotTheSecret(t *testing.T) {
	// The registry's own header says not to write keys into config.yaml. A row
	// imported from another tool's config must keep the reference, or the
	// import silently copies a secret into a second file.
	opt := Options{Getenv: func(k string) string {
		if k == "DASHSCOPE_API_KEY" {
			return "sk-should-not-be-written"
		}
		return ""
	}}
	mc, err := Candidate{
		Provider: "qwen", Model: "qwen3-max", APIType: "openai",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		KeyRef:  "env:DASHSCOPE_API_KEY", Importable: true,
	}.Row(opt)
	if err != nil {
		t.Fatal(err)
	}
	if mc.APIKey != "" {
		t.Errorf("secret was resolved into the row: %q", mc.APIKey)
	}
	if mc.KeyEnv != "DASHSCOPE_API_KEY" {
		t.Errorf("key_env = %q", mc.KeyEnv)
	}
	if mc.NoAuth {
		t.Error("a row with an env reference must not be marked no-auth")
	}
}

func TestRowKeepsLiteralPlaceholder(t *testing.T) {
	// A local proxy's placeholder is not in any environment; it has to be
	// written or the row cannot authenticate at all.
	//
	// The reference names a Codex provider, so the fixture has to be this
	// test's own: reading the host's ~/.codex/config.toml made the assertion
	// depend on whether the machine running it happens to have one.
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[model_providers.custom]\nbase_url = \"http://127.0.0.1:7896/v1\"\n" +
		"experimental_bearer_token = \"PROXY_MANAGED\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Home: home, Getenv: func(string) string { return "" }}
	mc, err := Candidate{
		Provider: "custom", Model: "m", APIType: "anthropic",
		BaseURL: "http://127.0.0.1:7896", KeyRef: "codex:custom", Importable: true,
	}.Row(opt)
	if err != nil {
		t.Fatal(err)
	}
	if mc.APIKey != "PROXY_MANAGED" || mc.KeyEnv != "" {
		t.Errorf("literal not carried: key=%q env=%q", mc.APIKey, mc.KeyEnv)
	}
}
