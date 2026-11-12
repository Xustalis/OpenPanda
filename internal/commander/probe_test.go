// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"github.com/Xustalis/OpenPanda/internal/providers"
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
// the run sends and the endpoint refuses it — 401/402/403 — the verdict
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

// TestProbeRateLimitedIsReachable pins the 429 carve-out. A rate limit is a
// service that is alive and answering, and the rest of the tree already retries
// 429 as a transient on the model call (entry/model.go, entry/fallback.go)
// rather than reading it as a verdict on the key. This matters for account-pool
// relays: asked to serve a name it cannot route, a relay answers 429 "no
// account can take this" — which says nothing about the credential, and used to
// disqualify an otherwise healthy agent from every task until the pool freed up.
func TestProbeRateLimitedIsReachable(t *testing.T) {
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	v := modelProbe(ProbeSpec{
		Endpoint: srv.URL, APIType: config.APITypeOpenAI,
		APIKey: "sk-rate-limited", KeyDefinitive: true,
	})
	if !v.OK || v.Rejected {
		t.Fatalf("definitive-key 429 = %+v, want reachable: a rate limit is not a credential refusal", v)
	}
	if !strings.Contains(v.Detail, "429") {
		t.Fatalf("429 = %+v, want the status kept in the detail", v)
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

// TestAgentUsableHarnessOwnCredentialRejectedByProbeStillUsable: when the
// harness carries its own credential (no panda injection), a 401 to panda's
// probe request is not evidence the harness fails — relays that admit only the
// genuine client refuse the probe's User-Agent while serving the real CLI. The
// gate must not disqualify an agent that works when run directly.
func TestAgentUsableHarnessOwnCredentialRejectedByProbeStillUsable(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "harness-own-token")
	r := probeRouter(config.ModelConfig{})
	ok, reason := r.agentUsable("claude_code", usableAgent("claude_code.py"))
	if !ok {
		t.Fatalf("agentUsable = %v, %q — harness-owned credential must not be disqualified by the probe", ok, reason)
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
	if ep := r.agentEndpoint("codex", usableAgent("codex.py")); ep != "https://override.test/v1" {
		t.Fatalf("env override endpoint = %q", ep)
	}
	t.Setenv("OPENAI_BASE_URL", "")
	if ep := r.agentEndpoint("codex", usableAgent("codex.py")); ep != "https://api.openai.com" {
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
	if ep := r2.agentEndpoint("codex", usableAgent("codex.py")); ep != "https://injected.test/v1" {
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
	if ep := r.agentEndpoint("codex", usableAgent("codex.py")); ep != "https://api.deepseek.com/v1" {
		t.Fatalf("codex config.toml endpoint = %q, want the configured relay", ep)
	}
	spec := r.agentTarget("codex", usableAgent("codex.py"))
	if spec.APIKey != "own-key" || spec.APIType != config.APITypeOpenAI {
		t.Fatalf("probe spec should carry own key + openai type, got %+v", spec)
	}

	// claude on a relay via ~/.claude/settings.json env.
	home2 := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home2, ".claude", "settings.json"),
		`{"env": {"ANTHROPIC_BASE_URL": "https://relay.example.com"}}`)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	if ep := r.agentEndpoint("claude_code", usableAgent("claude_code.py")); ep != "https://relay.example.com" {
		t.Fatalf("claude settings.json endpoint = %q, want the configured relay", ep)
	}

	// opencode provider override via opencode.json provider options.
	home3 := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home3, ".config", "opencode", "opencode.json"), `{
  "model": "myrelay/some-model",
  "provider": {"myrelay": {"options": {"baseURL": "https://relay2.example.com/v1"}}}
}`)
	if ep := r.agentEndpoint("opencode", usableAgent("opencode.py")); ep != "https://relay2.example.com/v1" {
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
	spec := probeRouter(config.ModelConfig{}).agentTarget("codex", usableAgent("codex.py"))
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

	spec := probeRouter(config.ModelConfig{}).agentTarget("hermes", usableAgent("hermes.py"))
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

	own, src := probeAgentCredentials("deepseek_harness", "deepseek_harness.py")
	if !own || !strings.Contains(src, ".credentials.yaml") {
		t.Fatalf("dsh credentials not detected: own=%v src=%q", own, src)
	}
	spec := probeRouter(config.ModelConfig{}).agentTarget("deepseek_harness", usableAgent("deepseek_harness.py"))
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
	if own, _ := probeAgentCredentials("deepseek_harness", "deepseek_harness.py"); own {
		t.Fatal("empty refs block must not count as credentials")
	}
}

// TestDotenvCommentsNotCredentials: a .env of comments and empty
// placeholders is not credentials — hermes ships exactly that template.
func TestDotenvCommentsNotCredentials(t *testing.T) {
	home := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home, ".hermes", ".env"),
		"# Hermes env\n# OPENAI_API_KEY=sk-...\nOPENAI_API_KEY=\n")
	if own, _ := probeAgentCredentials("hermes", "hermes.py"); own {
		t.Fatal("comments-only .env must not count as credentials")
	}
	mustWrite(t, filepath.Join(home, ".hermes", ".env"), "OPENAI_API_KEY=sk-live\n")
	if own, _ := probeAgentCredentials("hermes", "hermes.py"); !own {
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
	spec := probeRouter(config.ModelConfig{}).agentTarget("hermes", usableAgent("hermes.py"))
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

// TestPlanUnusableReason: the gate's failure text must name the candidate and
// the failing check instead of a generic "check failed" — an operator reading
// "no usable agent" needs to know whether to install a CLI, fix a key, or wait
// out a provider.
func TestPlanUnusableReason(t *testing.T) {
	r := probeRouter(config.ModelConfig{})

	// Non-agent plans have no candidate to explain.
	if got := r.PlanUnusableReason(Plan{Kind: "native"}); got != "" {
		t.Fatalf("native plan reason = %q, want empty", got)
	}

	// A dispatchable candidate clears the whole plan.
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	if got := r.PlanUnusableReason(Plan{Kind: "agent", Agent: "claude_code"}); got != "" {
		t.Fatalf("usable plan reason = %q, want empty", got)
	}

	// A candidate the card does not know is named as such, not lumped into a
	// generic failure.
	r.SetAgentProber(func(string, ledger.Agent) bool { return false })
	got := r.PlanUnusableReason(Plan{Kind: "agent", Agent: "ghost"})
	if !strings.Contains(got, "ghost: not in the capability card") {
		t.Fatalf("reason = %q, want it to name the unknown candidate", got)
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
	// lat == 0 is legal, not a missing measurement: on coarse-grained timers
	// (Windows) a localhost probe can complete inside one clock tick.
	if !v.OK || lat < 0 || lat > 10*time.Second || v.Detail != "http 200" {
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

// withProbeDeadline shortens the probe's own client deadline so a test can wait
// on a timeout without paying the production budget.
func withProbeDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	saved := endpointClient.Timeout
	endpointClient.Timeout = d
	t.Cleanup(func() { endpointClient.Timeout = saved })
}

// slowEndpoint accepts the request and then answers nothing, holding it open
// past the deadline — what a relay queued behind its own upstream looks like.
// srv.Close is registered first so the release runs before it: Close waits for
// outstanding handlers, and the handler is waiting on that channel.
func slowEndpoint(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv.URL
}

// TestProbeDeadlineIsUnknown pins the distinction the routing gate now rests on:
// a probe that drew no answer inside its deadline reports Unknown, and a refused
// connect does not. Without that split, either a slow relay blacklists a working
// agent, or a dead address stops disqualifying one.
func TestProbeDeadlineIsUnknown(t *testing.T) {
	resetEndpointProbeCache()
	withProbeDeadline(t, 150*time.Millisecond)

	v := modelProbe(ProbeSpec{
		Endpoint: slowEndpoint(t), APIType: config.APITypeOpenAI,
		APIKey: "sk-slow", KeyDefinitive: true,
	})
	if v.OK || v.Rejected || !v.Unknown {
		t.Fatalf("deadline = %+v, want !OK, !Rejected, Unknown", v)
	}

	resetEndpointProbeCache()
	v = modelProbe(ProbeSpec{Endpoint: deadEndpoint(t), APIType: config.APITypeOpenAI})
	if v.OK || v.Unknown {
		t.Fatalf("refused connect = %+v, want !OK and not Unknown — nothing serves that address, and that is a verdict", v)
	}
}

// TestAgentUsableDeadlineDoesNotBlock pins the gate both ways: a relay that has
// not answered inside the deadline must not disqualify the agent behind it,
// while an address that refuses connections still must.
func TestAgentUsableDeadlineDoesNotBlock(t *testing.T) {
	cleanCredentialEnv(t)
	if !pyexec.Available() {
		t.Skip("no python interpreter on this host")
	}
	withProbeDeadline(t, 150*time.Millisecond)

	resetEndpointProbeCache()
	r := probeRouter(config.ModelConfig{
		APIType: config.APITypeOpenAI, BaseURL: slowEndpoint(t),
		APIKey: "sk-test", Model: "m",
	})
	if ok, reason := r.agentUsable("codex", usableAgent("codex.py")); !ok {
		t.Fatalf("agentUsable = false (%q) — a probe deadline carries no information about the agent", reason)
	}

	resetEndpointProbeCache()
	r = probeRouter(config.ModelConfig{
		APIType: config.APITypeOpenAI, BaseURL: deadEndpoint(t),
		APIKey: "sk-test", Model: "m",
	})
	if ok, reason := r.agentUsable("codex", usableAgent("codex.py")); ok {
		t.Fatalf("a refused connect must still disqualify, got usable with reason %q", reason)
	}
}

// TestProbeSendsProviderSessionHeader: gateways that route and cache by
// conversation answer 400 MissingSessionID to a request without their session
// header — OpenCode's Go surface does exactly that. A probe that omits the
// header reported "http 400" for a perfectly working endpoint, which doctor
// then painted green. Pin both halves: the header goes out, and the same
// server turns reachable once it arrives.
func TestProbeSendsProviderSessionHeader(t *testing.T) {
	resetEndpointProbeCache()
	orig := providerDetect
	t.Cleanup(func() { providerDetect = orig })
	providerDetect = func(baseURL, model string) (providers.Provider, bool) {
		return providers.Provider{ID: "opencode-go", SessionHeader: "x-opencode-session"}, true
	}

	var gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = r.Header.Get("x-opencode-session")
		if gotSession == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"MissingSessionID"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	v := modelProbe(ProbeSpec{
		Endpoint: srv.URL, APIType: config.APITypeOpenAI,
		APIKey: "sk-test", Model: "deepseek-v4.1-flash", KeyDefinitive: true,
	})
	if !strings.HasPrefix(gotSession, "ses_") {
		t.Fatalf("probe session header = %q, want a ses_ value", gotSession)
	}
	if !v.OK || v.Status != http.StatusOK {
		t.Fatalf("verdict = ok:%v status:%d detail:%q, want a 200 once the header is sent",
			v.OK, v.Status, v.Detail)
	}
}

// TestProbeSessionHeaderDetection pins the mapping the header rides on, using
// the endpoint that motivated it: a "custom"-shaped base URL pointed at a known
// gateway must still be recognised. No network — this is the catalogue lookup.
func TestProbeSessionHeaderDetection(t *testing.T) {
	if got := probeSessionHeader(ProbeSpec{
		Endpoint: "https://opencode.ai/zen/go/v1", Model: "deepseek-v4.1-flash",
	}); got != "x-opencode-session" {
		t.Fatalf("opencode-go session header = %q, want x-opencode-session", got)
	}
	if got := probeSessionHeader(ProbeSpec{
		Endpoint: "https://api.example.invalid/v1", Model: "m",
	}); got != "" {
		t.Fatalf("unknown endpoint session header = %q, want empty", got)
	}
}

// TestProbeClientErrorKeepsStatus: the verdict stays reachable on a 4xx (a
// harness may run a model the probe never names), but the status must survive
// so a diagnostic can say "answered, refused the request" instead of printing
// a bare "http 400" next to a tick.
func TestProbeClientErrorKeepsStatus(t *testing.T) {
	resetEndpointProbeCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	v := modelProbe(ProbeSpec{
		Endpoint: srv.URL, APIType: config.APITypeOpenAI, APIKey: "sk-test",
		Model: "m", KeyDefinitive: true,
	})
	if !v.OK {
		t.Fatalf("a 400 must stay reachable for routing, got %+v", v)
	}
	if v.Status != http.StatusBadRequest {
		t.Fatalf("verdict status = %d, want 400", v.Status)
	}
}

func TestClaudeEnvironmentEndpointUsesSettingsCredentials(t *testing.T) {
	home := t.TempDir()
	oldHome := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = oldHome })
	t.Setenv("ANTHROPIC_BASE_URL", "https://relay.example")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"env":{"ANTHROPIC_API_KEY":"relay-key","ANTHROPIC_MODEL":"relay-model"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := &Router{}
	spec := r.agentTarget("claude_code", ledger.Agent{Adapter: "claude_code.py"})
	if spec.Endpoint != "https://relay.example" || spec.APIKey != "relay-key" || spec.Model != "relay-model" || !spec.KeyDefinitive {
		t.Fatalf("unexpected probe target: %+v", spec)
	}
}

func TestCodexEnvironmentEndpointDoesNotBorrowConfigCredential(t *testing.T) {
	home := cleanCredentialEnv(t)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), `model_provider = "relay"
[model_providers.relay]
base_url = "https://configured.example/v1"
wire_api = "responses"
experimental_bearer_token = "private-config-token"
`)
	t.Setenv("OPENAI_BASE_URL", "https://override.example/v1")
	spec := probeRouter(config.ModelConfig{}).agentTarget("codex", usableAgent("codex.py"))
	if spec.Endpoint != "https://override.example/v1" || spec.APIKey != "" || spec.APIType != config.APITypeOpenAI {
		t.Fatalf("unexpected probe target: %+v", spec)
	}
}
