package commander

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
)

// deadEndpoint returns a URL guaranteed to fail the probe: a listener is
// opened and immediately closed, so connecting hits a refused port without
// depending on any real network route.
func deadEndpoint(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return "http://" + addr
}

func probeRouter(model config.ModelConfig) *Router {
	r := NewRouter(testCard(), NewExecutor(), model,
		config.InjectionConfig{}, config.RoutingConfig{})
	return r
}

// TestProbePostsMinimalModelRequest: the probe must exercise the model API —
// a POST to the chat-completions leaf — not a bare host GET, so a relay that
// serves the API (and only the API) still probes reachable.
func TestProbePostsMinimalModelRequest(t *testing.T) {
	resetEndpointProbeCache()
	var gotMethod, gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	v := modelProbe(ProbeSpec{
		Endpoint: srv.URL, APIType: config.APITypeOpenAI,
		APIKey: "sk-secret", Model: "gpt-test", KeyDefinitive: true,
	})
	if !v.OK {
		t.Fatalf("live endpoint should probe reachable, detail=%q", v.Detail)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("probe method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("probe path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Fatalf("probe auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"max_tokens":1`) || !strings.Contains(gotBody, "ping") {
		t.Fatalf("probe body is not a minimal completion: %s", gotBody)
	}
}

// TestProbeAnthropicShape: the anthropic wire protocol POSTs /v1/messages
// with x-api-key and the version header.
func TestProbeAnthropicShape(t *testing.T) {
	resetEndpointProbeCache()
	var gotPath, gotKey, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	v := modelProbe(ProbeSpec{
		Endpoint: srv.URL, APIType: config.APITypeAnthropic, APIKey: "k", Model: "m",
	})
	if !v.OK || gotPath != "/v1/messages" || gotKey != "k" || gotVer == "" {
		t.Fatalf("anthropic probe = ok:%v path:%q key:%q ver:%q", v.OK, gotPath, gotKey, gotVer)
	}
}

// TestProbeServerErrorUnreachable: a 5xx means the model cannot serve right
// now — the exact "configured but cannot connect" case routing must catch.
func TestProbeServerErrorUnreachable(t *testing.T) {
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	v := modelProbe(ProbeSpec{Endpoint: srv.URL, APIType: config.APITypeOpenAI})
	if v.OK {
		t.Fatal("503 must probe unreachable — the service answers but cannot serve")
	}
	if !strings.Contains(v.Detail, "503") {
		t.Fatalf("detail = %q, want the http status", v.Detail)
	}
}

// TestProbeCredentialRejected: when the probe carries the literal credential
// the run sends and the endpoint refuses it — 401/402/403/429 — the verdict
// is rejected, not reachable: a relay with no quota would hang the run
// exactly the same way.
func TestProbeCredentialRejected(t *testing.T) {
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	// Definitive key refused → unusable.
	v := modelProbe(ProbeSpec{
		Endpoint: srv.URL, APIType: config.APITypeOpenAI,
		APIKey: "sk-dead", KeyDefinitive: true,
	})
	if v.OK || !v.Rejected {
		t.Fatalf("definitive-key 403 = %+v, want rejected", v)
	}

	// A maybe-stale extracted token (non-definitive) proves only the host.
	v = modelProbe(ProbeSpec{
		Endpoint: srv.URL + "/oauth", APIType: config.APITypeOpenAI,
		APIKey: "tok-oauth", KeyDefinitive: false,
	})
	if !v.OK || v.Rejected {
		t.Fatalf("non-definitive-key 403 = %+v, want reachable", v)
	}

	// No key at all → 4xx is just the service answering.
	v = modelProbe(ProbeSpec{Endpoint: srv.URL + "/anon", APIType: config.APITypeOpenAI})
	if !v.OK {
		t.Fatalf("anonymous 403 = %+v, want reachable", v)
	}
}

// TestProbeClientErrorReachable: a 4xx proves the service answers API-shaped
// requests; auth rejection is the adapter's failure class, not connectivity.
func TestProbeClientErrorReachable(t *testing.T) {
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	v := modelProbe(ProbeSpec{Endpoint: srv.URL, APIType: config.APITypeOpenAI})
	if !v.OK || !strings.Contains(v.Detail, "401") {
		t.Fatalf("401 should probe reachable with status detail, got %+v", v)
	}
}

func TestEndpointReachable(t *testing.T) {
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // auth failure still proves reachability
	}))
	t.Cleanup(srv.Close)
	if v := modelProbe(ProbeSpec{Endpoint: srv.URL}); !v.OK {
		t.Fatal("live endpoint should probe reachable")
	}
	if v := modelProbe(ProbeSpec{Endpoint: deadEndpoint(t)}); v.OK || v.Detail == "" {
		t.Fatalf("closed port should probe unreachable with a detail, got %+v", v)
	}
}

func TestEndpointProbeCachesVerdict(t *testing.T) {
	resetEndpointProbeCache()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)

	// Success is cached for endpointProbeOKTTL: a second check must not hit
	// the network again.
	spec := ProbeSpec{Endpoint: srv.URL}
	if v := modelProbe(spec); !v.OK {
		t.Fatal("expected reachable verdict")
	}
	if v := modelProbe(spec); !v.OK {
		t.Fatal("expected reachable verdict")
	}
	if calls != 1 {
		t.Fatalf("cached verdict re-probed: %d calls", calls)
	}

	// A failure is cached too, but with the shorter bad-verdict TTL.
	dead := ProbeSpec{Endpoint: deadEndpoint(t)}
	if v := modelProbe(dead); v.OK {
		t.Fatal("expected unreachable verdict")
	}
	if v := modelProbe(dead); v.OK {
		t.Fatal("expected unreachable verdict")
	}
}

// usableAgent returns a card entry whose CLI resolves on this host (the go
// toolchain binary is present wherever `go test` runs) so the static checks
// pass and the endpoint probe is what decides.
func usableAgent(adapter string) ledger.Agent {
	return ledger.Agent{Adapter: adapter, InstallCheck: "which go"}
}

func TestAgentUsableNoModelConfigured(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	// No own credentials, no panda model key: the installed CLI is dead weight.
	r := probeRouter(config.ModelConfig{})
	ok, reason := r.agentUsable("codex", usableAgent("codex.py"))
	if ok || !strings.Contains(reason, "no model configured") {
		t.Fatalf("agentUsable = %v, %q — want unusable 'no model configured'", ok, reason)
	}
}

func TestAgentUsableEndpointUnreachable(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	resetEndpointProbeCache()
	// Injection would give the agent a model, but the endpoint answers nothing.
	model := config.ModelConfig{
		APIType: config.APITypeOpenAI,
		BaseURL: deadEndpoint(t),
		APIKey:  "sk-test",
		Model:   "m",
	}
	r := probeRouter(model)
	ok, reason := r.agentUsable("codex", usableAgent("codex.py"))
	if ok {
		t.Fatal("agentUsable should fail on an unreachable injected endpoint")
	}
	if !strings.Contains(reason, "unreachable") {
		t.Fatalf("reason = %q, want 'unreachable'", reason)
	}
}

func TestAgentUsableEndpointReachable(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // a 404 path still proves the service is alive
	}))
	t.Cleanup(srv.Close)
	model := config.ModelConfig{
		APIType: config.APITypeOpenAI,
		BaseURL: srv.URL,
		APIKey:  "sk-test",
		Model:   "m",
	}
	r := probeRouter(model)
	ok, reason := r.agentUsable("codex", usableAgent("codex.py"))
	if !ok {
		t.Fatalf("agentUsable = %v, %q — reachable endpoint should pass", ok, reason)
	}
}

// TestAgentUsableCredentialsRejected: an injected model whose endpoint
// refuses the key (expired token, empty quota) is unusable — the run would
// send the same credential and fail identically.
func TestAgentUsableCredentialsRejected(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	r := probeRouter(config.ModelConfig{
		APIType: config.APITypeOpenAI,
		BaseURL: srv.URL,
		APIKey:  "sk-dead",
		Model:   "m",
	})
	ok, reason := r.agentUsable("codex", usableAgent("codex.py"))
	if ok {
		t.Fatal("rejected injected key must not be dispatchable")
	}
	if !strings.Contains(reason, "rejected") {
		t.Fatalf("reason = %q, want 'rejected'", reason)
	}
}

func TestAgentUsableStaticFailSkipsEndpointProbe(t *testing.T) {
	cleanCredentialEnv(t)
	r := probeRouter(config.ModelConfig{})
	probed := 0
	r.SetEndpointProber(func(ProbeSpec) ProbeVerdict {
		probed++
		return ProbeVerdict{}
	})
	// A missing CLI must fail before any network traffic is paid for it.
	ag := ledger.Agent{Adapter: "codex.py", InstallCheck: "which definitely-not-real-xyz"}
	if r.AgentDispatchable("codex", ag) {
		t.Fatal("missing cli should not be dispatchable")
	}
	if probed != 0 {
		t.Fatalf("endpoint probe ran %d times on a statically-dead agent", probed)
	}
}

func TestAgentEndpointResolution(t *testing.T) {
	cleanCredentialEnv(t)
	resetEndpointProbeCache()

	// Own credentials: the agent's own env override wins, then the registry
	// default endpoint.
	t.Setenv("OPENAI_API_KEY", "own-key")
	t.Setenv("OPENAI_BASE_URL", "https://override.test/v1")
	r := probeRouter(config.ModelConfig{})
	if ep := r.agentEndpoint(usableAgent("codex.py")); ep != "https://override.test/v1" {
		t.Fatalf("env override endpoint = %q", ep)
	}
	t.Setenv("OPENAI_BASE_URL", "")
	if ep := r.agentEndpoint(usableAgent("codex.py")); ep != "https://api.openai.com" {
		t.Fatalf("registry endpoint = %q", ep)
	}

	// Injection: the injected model's base URL wins over the registry default.
	cleanCredentialEnv(t)
	r2 := probeRouter(config.ModelConfig{
		APIType: config.APITypeOpenAI,
		BaseURL: "https://injected.test/v1",
		APIKey:  "sk-test",
		Model:   "m",
	})
	if ep := r2.agentEndpoint(usableAgent("codex.py")); ep != "https://injected.test/v1" {
		t.Fatalf("injected endpoint = %q", ep)
	}
}

// TestAgentEndpointFromOwnConfig: the relay-station case — the harness's own
// config file points at a different provider, and the probe must hit THAT
// endpoint, not the registry's default host (which may be dead while the
// relay is fine, or vice versa).
func TestAgentEndpointFromOwnConfig(t *testing.T) {
	home := cleanCredentialEnv(t)

	// codex on a DeepSeek relay via ~/.codex/config.toml.
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), `
model = "deepseek-chat"
model_provider = "deepseek"

[model_providers.deepseek]
name = "DeepSeek"
base_url = "https://api.deepseek.com/v1"
env_key = "DEEPSEEK_API_KEY"
`)
	t.Setenv("OPENAI_API_KEY", "own-key") // own creds: injection stays off
	r := probeRouter(config.ModelConfig{})
	if ep := r.agentEndpoint(usableAgent("codex.py")); ep != "https://api.deepseek.com/v1" {
		t.Fatalf("codex config.toml endpoint = %q, want the configured relay", ep)
	}
	spec := r.agentTarget(usableAgent("codex.py"))
	if spec.APIKey != "own-key" || spec.APIType != config.APITypeOpenAI {
		t.Fatalf("probe spec should carry own key + openai type, got %+v", spec)
	}

	// claude on a relay via ~/.claude/settings.json env.
	home2 := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home2, ".claude", "settings.json"),
		`{"env": {"ANTHROPIC_BASE_URL": "https://relay.example.com"}}`)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	if ep := r.agentEndpoint(usableAgent("claude_code.py")); ep != "https://relay.example.com" {
		t.Fatalf("claude settings.json endpoint = %q, want the configured relay", ep)
	}

	// opencode provider override via opencode.json provider options.
	home3 := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home3, ".config", "opencode", "opencode.json"), `{
  "model": "myrelay/some-model",
  "provider": {"myrelay": {"options": {"baseURL": "https://relay2.example.com/v1"}}}
}`)
	if ep := r.agentEndpoint(usableAgent("opencode.py")); ep != "https://relay2.example.com/v1" {
		t.Fatalf("opencode provider endpoint = %q", ep)
	}
}

// TestCodexConfigTargetResponses: a codex provider that speaks the Responses
// API must yield the responses probe shape, and its inline bearer token must
// authenticate the probe.
func TestCodexConfigTargetResponses(t *testing.T) {
	home := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), `
model_provider = "relay"

[model_providers.relay]
base_url = "https://relay.test/v1"
wire_api = "responses"
experimental_bearer_token = "tok-relay"
`)
	spec := probeRouter(config.ModelConfig{}).agentTarget(usableAgent("codex.py"))
	if spec.Endpoint != "https://relay.test/v1" || spec.APIType != apiTypeResponses || spec.APIKey != "tok-relay" {
		t.Fatalf("codex responses target = %+v", spec)
	}
	if u := modelAPIURL(spec.Endpoint, spec.APIType); u != "https://relay.test/v1/responses" {
		t.Fatalf("responses url = %q", u)
	}
	body, _ := minimalModelRequest(spec)
	if !strings.Contains(string(body), `"input":"ping"`) {
		t.Fatalf("responses body = %s", body)
	}
}

// TestHermesRecordedFailure: when the harness's own credential_pool marks
// the endpoint's credential dead and the probe cannot authenticate (secret
// in a keyring), the recorded failure + a live auth refusal = rejection.
func TestHermesRecordedFailure(t *testing.T) {
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	home := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"),
		"model:\n  default: m\n  base_url: "+srv.URL+"\n")
	mustWrite(t, filepath.Join(home, ".hermes", "auth.json"), `{
  "credential_pool": {
    "custom:relay": [{
      "base_url": "`+srv.URL+`",
      "last_status": "exhausted",
      "failure_reason": "auth",
      "last_error_code": 403,
      "last_error_reason": "INSUFFICIENT_BALANCE",
      "last_error_message": "Insufficient account balance",
      "last_error_reset_at": null
    }]
  }
}`)

	spec := probeRouter(config.ModelConfig{}).agentTarget(usableAgent("hermes.py"))
	if spec.Endpoint != srv.URL || spec.RejectionHint == "" {
		t.Fatalf("hermes target = %+v, want endpoint + rejection hint", spec)
	}
	v := modelProbe(spec)
	if v.OK || !v.Rejected || !strings.Contains(v.Detail, "INSUFFICIENT_BALANCE") {
		t.Fatalf("recorded-failure probe = %+v, want rejected with reason", v)
	}
}

// TestDshCredentialsYaml: dsh stores secrets in ~/.dsh/.credentials.yaml's
// refs map — the probe must find both the credential evidence (registry
// manifest) and the literal key (refs are injected as env, so definitive),
// plus the model name from settings.yaml's agent-default-model.
func TestDshCredentialsYaml(t *testing.T) {
	resetEndpointProbeCache()
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	home := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home, ".dsh", ".credentials.yaml"),
		"version: 1\nrecords: {}\nrefs:\n  DEEPSEEK_API_KEY: sk-dsh-secret\n")
	mustWrite(t, filepath.Join(home, ".dsh", "settings.yaml"),
		"agent-default-model:\n  provider: deepseek-official\n  model: deepseek-flash\n  reasoningEffort: high\n"+
			"llm-pi-ai:\n  providers:\n    custom:\n      base_url: "+srv.URL+"\n")

	own, src := probeAgentCredentials("deepseek_harness.py")
	if !own || !strings.Contains(src, ".credentials.yaml") {
		t.Fatalf("dsh credentials not detected: own=%v src=%q", own, src)
	}
	spec := probeRouter(config.ModelConfig{}).agentTarget(usableAgent("deepseek_harness.py"))
	if spec.Endpoint != srv.URL {
		t.Fatalf("dsh endpoint = %q, want custom provider %q", spec.Endpoint, srv.URL)
	}
	if spec.APIKey != "sk-dsh-secret" || !spec.KeyDefinitive {
		t.Fatalf("dsh key resolution failed: definitive=%v", spec.KeyDefinitive)
	}
	if spec.Model != "deepseek-flash" {
		t.Fatalf("dsh model = %q, want deepseek-flash", spec.Model)
	}
	if v := modelProbe(spec); !v.OK {
		t.Fatalf("dsh probe = %+v, want OK", v)
	}
	if gotAuth != "Bearer sk-dsh-secret" || !strings.Contains(gotBody, "deepseek-flash") {
		t.Fatalf("probe should authenticate with the refs key and model: auth=%q body=%s", gotAuth, gotBody)
	}
}

// TestDshEmptyRefsNotCredentials: a .credentials.yaml whose refs block is
// empty (or absent) is not credentials — dsh writes the file at first run.
func TestDshEmptyRefsNotCredentials(t *testing.T) {
	home := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home, ".dsh", ".credentials.yaml"),
		"version: 1\nrecords:\n  session: {}\nrefs: {}\n")
	if own, _ := probeAgentCredentials("deepseek_harness.py"); own {
		t.Fatal("empty refs block must not count as credentials")
	}
}

// TestDotenvCommentsNotCredentials: a .env of comments and empty
// placeholders is not credentials — hermes ships exactly that template.
func TestDotenvCommentsNotCredentials(t *testing.T) {
	home := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home, ".hermes", ".env"),
		"# Hermes env\n# OPENAI_API_KEY=sk-...\nOPENAI_API_KEY=\n")
	if own, _ := probeAgentCredentials("hermes.py"); own {
		t.Fatal("comments-only .env must not count as credentials")
	}
	mustWrite(t, filepath.Join(home, ".hermes", ".env"), "OPENAI_API_KEY=sk-live\n")
	if own, _ := probeAgentCredentials("hermes.py"); !own {
		t.Fatal("a real assignment in .env should count as credentials")
	}
}

// TestHermesStaleRecordIgnored: a recovered provider must not be condemned by
// history — an expired failure record does not feed the verdict.
func TestHermesStaleRecordIgnored(t *testing.T) {
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	home := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), "model:\n  base_url: "+srv.URL+"\n")
	mustWrite(t, filepath.Join(home, ".hermes", "auth.json"), `{
  "credential_pool": {
    "custom:relay": [{
      "base_url": "`+srv.URL+`",
      "last_status": "ok",
      "last_error_code": 403,
      "last_error_reset_at": "2000-01-01T00:00:00Z"
    }]
  }
}`)
	spec := probeRouter(config.ModelConfig{}).agentTarget(usableAgent("hermes.py"))
	if spec.RejectionHint != "" {
		t.Fatalf("stale record fed a hint: %+v", spec)
	}
	if v := modelProbe(spec); !v.OK {
		t.Fatalf("recovered provider should probe reachable, got %+v", v)
	}
}

// TestModelAPIURL: the probe URL derivation must match what the provider SDKs
// do — /v1 for bare hosts, versioned paths kept, service prefixes extended.
func TestModelAPIURL(t *testing.T) {
	cases := []struct{ base, apiType, want string }{
		{"https://api.openai.com", "openai", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1", "openai", "https://api.openai.com/v1/chat/completions"},
		{"https://api.deepseek.com/v1", "openai", "https://api.deepseek.com/v1/chat/completions"},
		{"https://open.bigmodel.cn/api/paas/v4", "openai", "https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		{"https://api.anthropic.com", "anthropic", "https://api.anthropic.com/v1/messages"},
		{"https://api.deepseek.com/anthropic", "anthropic", "https://api.deepseek.com/anthropic/v1/messages"},
		{"https://api.deepseek.com/anthropic/v1", "anthropic", "https://api.deepseek.com/anthropic/v1/messages"},
		{"http://localhost:11434", "openai", "http://localhost:11434/v1/chat/completions"},
		{"not a url", "openai", ""},
	}
	for _, c := range cases {
		if got := modelAPIURL(c.base, c.apiType); got != c.want {
			t.Errorf("modelAPIURL(%q, %q) = %q, want %q", c.base, c.apiType, got, c.want)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPlanUsable(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	r := probeRouter(config.ModelConfig{})

	// Non-agent plans carry no agent dependency.
	if !r.PlanUsable(Plan{Kind: "native"}) || !r.PlanUsable(Plan{Kind: "manual"}) {
		t.Fatal("native/manual plans must always be usable")
	}

	// An agent plan with no dispatchable candidate fails closed.
	if r.PlanUsable(Plan{Kind: "agent", Agent: "claude_code"}) {
		t.Fatal("plan with only-dead agents should not be usable")
	}

	// One dispatchable candidate anywhere in the chain makes it usable.
	r.SetAgentProber(func(name string, _ ledger.Agent) bool { return name == "opencode" })
	if !r.PlanUsable(Plan{Kind: "agent", Agent: "claude_code", Alternates: []string{"opencode"}}) {
		t.Fatal("plan with one live alternate should be usable")
	}
}

func TestCheckAgentReport(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	r := probeRouter(config.ModelConfig{
		APIType: config.APITypeOpenAI,
		BaseURL: srv.URL,
		APIKey:  "sk-test",
		Model:   "m-injected",
	})
	chk := r.CheckAgent("codex", usableAgent("codex.py"))
	if !chk.Usable || chk.Reason != "" {
		t.Fatalf("check = %+v, want usable", chk)
	}
	if chk.ModelSrc != "injected" || chk.Model != "m-injected" {
		t.Fatalf("model source = %q/%q, want injected m-injected", chk.ModelSrc, chk.Model)
	}
	if chk.Endpoint != srv.URL || !chk.Reachable || chk.Detail != "http 200" {
		t.Fatalf("endpoint = %q reachable=%v detail=%q", chk.Endpoint, chk.Reachable, chk.Detail)
	}
}

func TestProbeModelFreshLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)
	v, lat := ProbeModel(ProbeSpec{Endpoint: srv.URL, APIType: config.APITypeOpenAI})
	if !v.OK || lat <= 0 || lat > 10*time.Second || v.Detail != "http 200" {
		t.Fatalf("ProbeModel = %+v, %v", v, lat)
	}
}

// TestExecAgentReportsUnusableReason: the fallback chain's skip list must
// carry the actionable reason, not a bare "unavailable".
func TestExecAgentReportsUnusableReason(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	r := probeRouter(config.ModelConfig{})
	r.SetAdapterRunner(func(context.Context, string, string, string) AgentResult {
		t.Fatal("adapter must not run when every candidate is unusable")
		return AgentResult{}
	})
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	res := r.Execute(t.Context(), plan, "prompt", t.TempDir(), false)
	if res.OK {
		t.Fatal("expected failure")
	}
	if !strings.Contains(res.Stderr, "no usable agent") {
		t.Fatalf("stderr = %q", res.Stderr)
	}
	// The reason must name the failing check, so the user knows what to fix.
	if !strings.Contains(res.Stderr, "cli ") && !strings.Contains(res.Stderr, "no model configured") &&
		!strings.Contains(res.Stderr, "unreachable") && !strings.Contains(res.Stderr, "not found") {
		t.Fatalf("stderr lacks a dispatch reason: %q", res.Stderr)
	}
}
