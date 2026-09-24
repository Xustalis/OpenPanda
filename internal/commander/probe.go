package commander

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// Pre-dispatch model probing. AgentViable proves an agent is locally
// runnable — CLI on PATH, credentials or injection available — but says
// nothing about whether the provider it will talk to actually serves models.
// A configured-but-unreachable model (DNS broken, provider down, egress
// firewalled) used to surface only as a long adapter hang: the task was
// dispatched, the agent spawned, and the failure arrived minutes later.
// Worse, probing the provider's default base URL lies for agents that point
// at a relay or a different provider through their own config files: the
// default host can be dead while the configured endpoint is fine. The probe
// below therefore POSTs one minimal model request — a 1-token completion —
// to the endpoint the run will actually use, bounded hard at a few seconds,
// and caches the verdict so a burst of tasks pays it once.
const (
	// endpointProbeTimeout bounds a single probe. On a dead route the TCP
	// connect fails fast; a live API answers a tiny request well under it.
	endpointProbeTimeout = 5 * time.Second
	// endpointProbeOKTTL caches a healthy verdict: a provider that answered a
	// minute ago is almost certainly still up, and re-probing per task would
	// make routing latency depend on the provider's RTT.
	endpointProbeOKTTL = 60 * time.Second
	// endpointProbeBadTTL caches a failure shorter than a success so a
	// recovering provider is retried quickly, but a dead one does not make
	// every routing decision wait on a timeout.
	endpointProbeBadTTL = 30 * time.Second
)

// endpointClient is the probe's own transport: no proxy, no keep-alive (the
// connection is single-use anyway), and timeouts on every phase so a
// half-open TLS handshake cannot outlive the caller's budget.
var endpointClient = &http.Client{
	Timeout: endpointProbeTimeout,
	Transport: &http.Transport{
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   endpointProbeTimeout,
		ResponseHeaderTimeout: endpointProbeTimeout,
	},
}

// apiTypeResponses is the OpenAI Responses API wire shape — codex's
// wire_api="responses" providers speak it instead of chat/completions.
const apiTypeResponses = "responses"

// ProbeSpec describes the model call a run would actually make: which
// endpoint, which wire protocol, and — when panda can see it — the
// credential and model name the run would use. APIKey is sent to the
// endpoint only; it is never logged, cached, or included in reports.
type ProbeSpec struct {
	Endpoint string // resolved provider base URL
	APIType  string // "anthropic" | "openai" | "responses" — request shape
	APIKey   string // optional; auth proves more than connectivity when present
	Model    string // model name when known; a wrong name still earns a 4xx
	// KeyDefinitive marks APIKey as literally the credential the run sends —
	// an env var, an inline token, the injected key — not a possibly-stale
	// OAuth access token extracted from an auth store the CLI would refresh.
	// Only a definitive key lets a 401/402/403/429 verdict mean "the run
	// would fail the same way"; without one that response just proves the
	// service is up.
	KeyDefinitive bool
	// RejectionHint carries a secret-free reason the harness's own state
	// recorded against this endpoint's credential — e.g. hermes's
	// credential_pool marking a provider "exhausted / INSUFFICIENT_BALANCE".
	// When the probe itself cannot authenticate (the secret lives in a
	// keyring), the hint corroborates an auth-class 4xx into a rejection:
	// the endpoint refusing AND the harness recording a credential failure
	// is conclusive where either alone is not.
	RejectionHint string
}

// ProbeVerdict is the outcome of one model-endpoint probe.
type ProbeVerdict struct {
	// OK means the model service answered and — when a definitive credential
	// was probed — did not reject it.
	OK bool
	// Detail is the short secret-free status: "http 200", "http 503", or the
	// transport error text.
	Detail string
	// Rejected is true when the endpoint refused the definitive credential
	// (401/402/403/429): the service is up but the run would fail on
	// auth/quota — a different fix than a dead route.
	Rejected bool
}

// probeKey is the cache identity: everything that can change the verdict
// except the credential value itself.
func (s ProbeSpec) probeKey() string {
	return s.Endpoint + "|" + s.APIType + "|" + s.Model + "|" + s.RejectionHint
}

type endpointVerdict struct {
	ok       bool
	detail   string // e.g. "http 200", "http 503", "dial tcp …: connect refused"
	rejected bool
	checked  time.Time
}

var (
	endpointMu    sync.Mutex
	endpointCache = map[string]endpointVerdict{}
)

// probeNow is a test seam over the clock so TTL behaviour is deterministic.
var probeNow = time.Now

// modelProbe reports whether the model endpoint answers a minimal request
// within the probe budget. Verdict rules:
//   - transport error or HTTP ≥500 → unreachable: the model cannot serve
//     right now (the "configured but cannot connect" case).
//   - 401/402/403/429 with a definitive key → rejected: the run would send
//     the same credential and fail the same way (auth or quota).
//   - any other HTTP response → reachable: the service answers API-shaped
//     requests; without a definitive key a 4xx only proves the host, and a
//     wrong model name earns a 4xx too — both are the adapter's business,
//     not routing's.
//
// Results are cached per probe key with asymmetric TTLs.
func modelProbe(spec ProbeSpec) ProbeVerdict {
	key := spec.probeKey()
	endpointMu.Lock()
	if v, ok := endpointCache[key]; ok {
		ttl := endpointProbeBadTTL
		if v.ok {
			ttl = endpointProbeOKTTL
		}
		if probeNow().Sub(v.checked) < ttl {
			endpointMu.Unlock()
			return ProbeVerdict{OK: v.ok, Detail: v.detail, Rejected: v.rejected}
		}
	}
	endpointMu.Unlock()

	v := probeModelOnce(spec)

	endpointMu.Lock()
	endpointCache[key] = endpointVerdict{ok: v.OK, detail: v.Detail, rejected: v.Rejected, checked: probeNow()}
	endpointMu.Unlock()
	return v
}

// probeModelOnce performs the actual request: a minimal model completion
// (1 token, "ping") against the URL the provider SDK the harness wraps would
// derive. POST — not a bare GET on the host — so the answer proves the API
// service is alive, not merely that something listens on 443.
func probeModelOnce(spec ProbeSpec) ProbeVerdict {
	u := modelAPIURL(spec.Endpoint, spec.APIType)
	if u == "" {
		return ProbeVerdict{Detail: "bad endpoint url"}
	}
	body, headers := minimalModelRequest(spec)
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return ProbeVerdict{Detail: "bad endpoint url"}
	}
	req.Header.Set("Content-Type", "application/json")
	// A fixed UA keeps provider WAFs from categorising the probe as a broken
	// client; the credential rides the request only when one was resolved.
	req.Header.Set("User-Agent", "openpanda-probe/1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := endpointClient.Do(req)
	if err != nil {
		return ProbeVerdict{Detail: trimProbeErr(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	detail := fmt.Sprintf("http %d", resp.StatusCode)
	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		return ProbeVerdict{Detail: detail}
	case credentialRejected(resp.StatusCode) && spec.KeyDefinitive && spec.APIKey != "":
		return ProbeVerdict{Detail: detail, Rejected: true}
	case credentialRejected(resp.StatusCode) && spec.RejectionHint != "":
		return ProbeVerdict{Detail: detail + " · " + spec.RejectionHint, Rejected: true}
	default:
		return ProbeVerdict{OK: true, Detail: detail}
	}
}

// credentialRejected reports whether the status means "the credential or the
// account behind it was refused": bad auth, no quota, or rate-limited — all
// failures the run would hit identically.
func credentialRejected(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusPaymentRequired,
		http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return false
}

// trimProbeErr shortens a transport error for display: url.Error wraps the
// endpoint URL into the message — fine for the operator, but the cause
// (timeout, refused, DNS) is the useful part.
func trimProbeErr(err error) string {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err.Error()
	}
	return err.Error()
}

// minimalModelRequest builds the cheapest model-API request each wire
// protocol accepts — one token for the word "ping" — plus the auth header
// the resolved credential would use. A real answer (even an error status)
// exercises the model route, not just the host.
func minimalModelRequest(spec ProbeSpec) ([]byte, map[string]string) {
	model := spec.Model
	if model == "" {
		// No model name is knowable for an agent's own config-file creds; a
		// bogus name still earns a real API answer, which is what we probe for.
		model = "panda-probe"
	}
	messages := []map[string]string{{"role": "user", "content": "ping"}}
	if spec.APIType == apiTypeResponses {
		// Responses API: input is a plain string; max_output_tokens floors at 16.
		body, _ := json.Marshal(map[string]any{
			"model": model, "input": "ping", "max_output_tokens": 16,
		})
		h := map[string]string{}
		if spec.APIKey != "" {
			h["Authorization"] = "Bearer " + spec.APIKey
		}
		return body, h
	}
	if spec.APIType == config.APITypeAnthropic {
		body, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": 1, "messages": messages,
		})
		h := map[string]string{"anthropic-version": "2023-06-01"}
		if spec.APIKey != "" {
			// API keys authenticate via x-api-key, OAuth tokens via Bearer —
			// the config does not say which this is, so send both and let the
			// endpoint pick the scheme it honours.
			h["x-api-key"] = spec.APIKey
			h["Authorization"] = "Bearer " + spec.APIKey
		}
		return body, h
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 1, "stream": false, "messages": messages,
	})
	h := map[string]string{}
	if spec.APIKey != "" {
		h["Authorization"] = "Bearer " + spec.APIKey
	}
	return body, h
}

// modelAPIURL derives the request URL from a provider base URL the same way
// the SDKs the harness wraps do: an empty path gets /v1, an already-versioned
// path (/v1, /api/paas/v4) is kept, and a service prefix without a version
// (deepseek's /anthropic) gets /v1 appended. Then the protocol leaf —
// /chat/completions or /messages — is attached. "" on an unparseable base.
func modelAPIURL(base, apiType string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return ""
	}
	leaf := "/chat/completions"
	switch apiType {
	case config.APITypeAnthropic:
		leaf = "/messages"
	case apiTypeResponses:
		leaf = "/responses"
	}
	p := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(p, leaf):
		// already a full endpoint path
	case p == "":
		p = "/v1" + leaf
	case lastSegVersioned(p):
		p += leaf
	default:
		p += "/v1" + leaf
	}
	u.Path = p
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// lastSegVersioned reports whether the path's last segment is a version tag
// like /v1 or /v4 (so the leaf attaches directly).
func lastSegVersioned(p string) bool {
	seg := p[strings.LastIndex(p, "/")+1:]
	if len(seg) < 2 || seg[0] != 'v' {
		return false
	}
	for _, c := range seg[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// resetEndpointProbeCache clears cached verdicts. Test seam.
func resetEndpointProbeCache() {
	endpointMu.Lock()
	endpointCache = map[string]endpointVerdict{}
	endpointMu.Unlock()
}

// ProbeModel performs a fresh, uncached minimal-request check and reports
// the verdict plus the observed latency. Interactive diagnostics
// ("agents test", "doctor") need what the network looks like now, not a
// verdict from half a minute ago; the fresh result is written back into the
// cache so the printed verdict matches the one routing would use next.
func ProbeModel(spec ProbeSpec) (ProbeVerdict, time.Duration) {
	start := probeNow()
	v := probeModelOnce(spec)
	endpointMu.Lock()
	endpointCache[spec.probeKey()] = endpointVerdict{ok: v.OK, detail: v.Detail, rejected: v.Rejected, checked: probeNow()}
	endpointMu.Unlock()
	return v, time.Since(start)
}

// ProbeAgentFresh resolves the endpoint this agent's run would use and probes
// it uncached — the live counterpart of CheckAgent's cached verdict, for
// diagnostics that report the network as it is now.
func (r *Router) ProbeAgentFresh(ag ledger.Agent) (ProbeVerdict, time.Duration) {
	spec := r.agentTarget(ag)
	if spec.Endpoint == "" {
		return ProbeVerdict{}, 0
	}
	return ProbeModel(spec)
}

// AgentCheck is the user-facing dispatch-readiness report for one agent:
// whether it would accept a task right now, why not when it would not, which
// model source the run would use, and the provider endpoint it would hit.
// It backs `panda agents test`, where the reason must name the fix.
type AgentCheck struct {
	// Usable is the dispatch verdict — the same check routing applies.
	Usable bool
	// Reason explains the first failing check in fixable terms ("" when
	// Usable): missing cli, no credentials, unreachable endpoint.
	Reason string
	// ModelSrc is where the run's model comes from:
	// "own" (agent credentials), "injected" (panda's model), "self"
	// (self-contained), or "none" (no usable source).
	ModelSrc string
	// Model is the injected model name; only set when ModelSrc == "injected".
	Model string
	// Endpoint is the provider base URL the run would hit ("" = undeterminable).
	Endpoint string
	// Reachable is the cached endpoint verdict; meaningful only when
	// Endpoint != "".
	Reachable bool
	// Detail is the probe's short status ("http 200", "http 503", dial error)
	// — operator-facing, never carries credentials.
	Detail string
	// Rejected is true when the endpoint refused the definitive credential
	// (bad key or empty quota) — the fix is credentials, not connectivity.
	Rejected bool
}

// CheckAgent produces the dispatch-readiness report for one agent.
func (r *Router) CheckAgent(name string, ag ledger.Agent) AgentCheck {
	chk := AgentCheck{}
	chk.Usable, chk.Reason = r.agentUsable(name, ag)
	k, _ := agents.ByAdapter(ag.Adapter)
	spec := r.agentTarget(ag)
	switch {
	case k.SelfContainedModel:
		chk.ModelSrc = "self"
	default:
		if dec := r.InjectionDecision(ag.Adapter); dec.Inject {
			chk.ModelSrc = "injected"
			chk.Model = dec.Model
		} else if own, _ := probeAgentCredentials(ag.Adapter); own {
			chk.ModelSrc = "own"
			chk.Model = spec.Model // the model the agent's config names
		} else {
			chk.ModelSrc = "none"
		}
	}
	chk.Endpoint = spec.Endpoint
	if chk.Endpoint != "" {
		v := r.endpointProbe(spec)
		chk.Reachable, chk.Detail, chk.Rejected = v.OK, v.Detail, v.Rejected
	}
	return chk
}

// credPair is a candidate credential plus whether it is definitive — the
// literal value the run sends (env vars, inline tokens, API keys) versus a
// possibly-stale OAuth token a CLI would refresh on its own.
type credPair struct {
	v   string
	def bool
}

// firstCred returns the first non-empty candidate's value and flag.
func firstCred(ps ...credPair) (string, bool) {
	for _, p := range ps {
		if p.v != "" {
			return p.v, p.def
		}
	}
	return "", false
}

// configuredTarget is everything the harness's own config files reveal about
// the model call its run would make — the provider base URL (where relay
// stations and custom providers live), the credential the run would send
// when the config names one, the wire API and model name, and any
// credential-failure reason the harness recorded itself.
type configuredTarget struct {
	Endpoint string
	APIKey   string
	KeyDef   bool   // APIKey is the literal credential the run sends
	APIType  string // overrides the registry API type when the config says so
	Hint     string // secret-free recorded credential failure (see RejectionHint)
	Model    string
}

// agentConfiguredTarget reads the harness's own config files for the target
// its run would really use (codex's model_providers, claude's settings env,
// opencode's provider options, dsh's credentials refs). KeyDef reports
// whether that credential is the literal one the run uses: env vars, inline
// tokens and secrets-store entries are, OAuth access tokens in auth stores
// are not (the CLI refreshes those itself, so a probe rejection proves
// nothing). An empty Endpoint means nothing configured: the caller falls
// back to the registry's default. Extraction is best-effort; the key is used
// for the probe request only — never logged, cached, or reported.
func agentConfiguredTarget(adapter string) configuredTarget {
	var t configuredTarget
	home, err := homeDir()
	if err != nil {
		return t
	}
	switch adapter {
	case "claude_code.py":
		t.Endpoint = jsonStringAt(home, ".claude/settings.json", "env.ANTHROPIC_BASE_URL")
		t.APIKey, t.KeyDef = firstCred(
			credPair{jsonStringAt(home, ".claude/settings.json", "env.ANTHROPIC_API_KEY", "env.ANTHROPIC_AUTH_TOKEN"), true},
			credPair{jsonStringAt(home, ".claude/config.json", "primaryApiKey"), true},
			credPair{jsonStringAt(home, ".claude/.credentials.json", "claudeAiOauth.accessToken"), false},
		)
		t.Model = firstNonEmpty(
			jsonStringAt(home, ".claude/settings.json", "env.ANTHROPIC_MODEL"),
			jsonStringAt(home, ".claude/settings.json", "model"))
	case "codex.py":
		var envKey, bearer, wire string
		t.Endpoint, envKey, bearer, wire, t.Model = codexConfigTarget(home)
		t.APIKey, t.KeyDef = firstCred(
			credPair{os.Getenv(envKey), envKey != ""},
			credPair{bearer, true},
			credPair{jsonStringAt(home, ".codex/auth.json", "OPENAI_API_KEY", "api_key", "apiKey"), true},
			credPair{jsonStringAt(home, ".codex/auth.json", "token", "access_token", "tokens.access_token"), false},
		)
		if wire == "responses" {
			t.APIType = apiTypeResponses
		}
	case "grok_build.py":
		t.Endpoint = firstNonEmpty(
			tomlStringValue(home, ".grok/config.toml", "base_url"),
			jsonStringAt(home, ".grok/auth.json", "base_url", "baseURL"))
		t.APIKey, t.KeyDef = firstCred(
			credPair{jsonStringAt(home, ".grok/auth.json", "api_key", "apiKey"), true},
			credPair{jsonStringAt(home, ".grok/auth.json", "token", "access_token"), false},
		)
		t.Model = tomlStringValue(home, ".grok/config.toml", "model")
	case "deepseek_harness.py":
		// dsh resolves everything under ~/.dsh: secrets live in
		// .credentials.yaml's refs map (env-name → secret — the literal
		// credential injected into the LLM env, so definitive), the model
		// in settings.yaml's agent-default-model, and any custom provider
		// base_url in settings/legacy config files.
		t.Endpoint = firstNonEmpty(
			yamlLineValue(home, ".dsh/settings.yaml", "base_url", "baseURL", "api_base"),
			jsonStringAt(home, ".dsh/config.json", "base_url", "baseURL", "api_base", "endpoint"),
			jsonStringAt(home, ".dsh/auth.json", "base_url", "baseURL"))
		t.APIKey, t.KeyDef = firstCred(
			credPair{dotenvValue(home, ".dsh/.env", "DEEPSEEK_API_KEY", "OPENAI_API_KEY"), true},
			credPair{yamlLineValue(home, ".dsh/.credentials.yaml", "DEEPSEEK_API_KEY", "OPENAI_API_KEY"), true},
			credPair{jsonStringAt(home, ".dsh/auth.json", "api_key", "apiKey", "DEEPSEEK_API_KEY"), true},
			credPair{jsonStringAt(home, ".dsh/auth.json", "token", "access_token"), false},
		)
		t.Model = yamlNestedValue(home, ".dsh/settings.yaml", "agent-default-model", "model")
	case "openclaw.py":
		t.Endpoint = firstNonEmpty(
			jsonStringAt(home, ".openclaw/config.json", "base_url", "baseURL", "endpoint"),
			jsonStringAt(home, ".openclaw/auth.json", "base_url", "baseURL"))
		t.APIKey, t.KeyDef = firstCred(
			credPair{jsonStringAt(home, ".openclaw/auth.json", "api_key", "apiKey"), true},
			credPair{jsonStringAt(home, ".openclaw/auth.json", "token", "access_token"), false},
		)
		t.Model = jsonStringAt(home, ".openclaw/config.json", "model")
	case "hermes.py":
		t.Endpoint = firstNonEmpty(
			dotenvValue(home, ".hermes/.env", "OPENAI_BASE_URL", "HERMES_BASE_URL", "BASE_URL"),
			yamlLineValue(home, ".hermes/config.yaml", "base_url", "baseURL"),
			jsonStringAt(home, ".hermes/auth.json", "base_url", "baseURL"))
		t.APIKey, t.KeyDef = firstCred(
			credPair{dotenvValue(home, ".hermes/.env", "OPENAI_API_KEY", "HERMES_API_KEY"), true},
			credPair{jsonStringAt(home, ".hermes/auth.json", "api_key", "apiKey"), true},
			credPair{jsonStringAt(home, ".hermes/auth.json", "token", "access_token"), false},
		)
		t.Model = yamlNestedValue(home, ".hermes/config.yaml", "model", "default")
	case "opencode.py":
		t.Endpoint, t.Model = opencodeConfigTarget(home)
		t.APIKey = opencodeAuthKey(home) // provider creds may be OAuth — never definitive
	}
	if t.Hint == "" && t.Endpoint != "" {
		t.Hint = recordedCredentialFailure(adapter, home, t.Endpoint)
	}
	return t
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// recordedCredentialFailure asks the harness's own state whether the
// credential bound to endpoint is already known-dead. Some CLIs store the
// secret itself in an OS keyring (unreachable to a probe), but still record
// the failure their last call hit — hermes's credential_pool marks the
// provider "exhausted / INSUFFICIENT_BALANCE". That record plus a live
// auth-class refusal from the same endpoint is conclusive; "" means the
// harness records nothing (or nothing bad) for this endpoint.
func recordedCredentialFailure(adapter, home, endpoint string) string {
	switch adapter {
	case "hermes.py":
		return hermesCredentialPoolFailure(home, endpoint)
	}
	return ""
}

// hermesCredentialPoolFailure scans ~/.hermes/auth.json's credential_pool for
// the entry serving endpoint (matched by hostname) and reports the recorded
// failure when it is persistent (no reset scheduled) and credential-class —
// "exhausted"/"failed" status, failure_reason "auth", or an auth-class error
// code. A cleared or transient record returns "" so a recovered provider is
// not condemned by history.
func hermesCredentialPoolFailure(home, endpoint string) string {
	obj := readJSONFile(filepath.Join(home, ".hermes", "auth.json"))
	if obj == nil {
		return ""
	}
	var pool map[string]json.RawMessage
	if raw, ok := obj["credential_pool"]; !ok || json.Unmarshal(raw, &pool) != nil {
		return ""
	}
	host := ""
	if u, err := url.Parse(endpoint); err == nil {
		host = u.Hostname()
	}
	if host == "" {
		return ""
	}
	for _, raw := range pool {
		var entries []map[string]any
		if json.Unmarshal(raw, &entries) != nil {
			continue
		}
		for _, e := range entries {
			bu, _ := e["base_url"].(string)
			u, err := url.Parse(bu)
			if err != nil || u.Hostname() != host {
				continue
			}
			if !hermesFailureLive(e) {
				continue
			}
			reason, _ := e["last_error_reason"].(string)
			msg, _ := e["last_error_message"].(string)
			switch {
			case reason != "" && msg != "":
				return reason + " — " + msg
			case reason != "":
				return reason
			case msg != "":
				return msg
			default:
				if s, _ := e["last_status"].(string); s != "" {
					return "credential status " + s
				}
				return "credential failure recorded"
			}
		}
	}
	return ""
}

// hermesFailureLive reports whether one credential_pool entry records a
// persistent credential-class failure: an exhausted/failed status, an
// auth-class failure_reason, or an auth-class last_error_code — unless a
// reset timestamp already passed, in which case the record is stale.
func hermesFailureLive(e map[string]any) bool {
	if at, _ := e["last_error_reset_at"].(string); at != "" {
		if ts, err := time.Parse(time.RFC3339, at); err == nil && time.Now().After(ts) {
			return false
		}
	}
	status, _ := e["last_status"].(string)
	switch status {
	case "exhausted", "failed", "disabled", "revoked", "error":
		return true
	}
	if fr, _ := e["failure_reason"].(string); fr == "auth" || fr == "quota" {
		return true
	}
	if code, ok := e["last_error_code"].(float64); ok {
		switch int(code) {
		case http.StatusUnauthorized, http.StatusPaymentRequired,
			http.StatusForbidden, http.StatusTooManyRequests:
			return true
		}
	}
	return false
}

// jsonStringAt reads a JSON file under home and returns the first non-empty
// string among the dotted paths (e.g. "env.ANTHROPIC_BASE_URL").
func jsonStringAt(home, rel string, paths ...string) string {
	obj := readJSONFile(filepath.Join(home, filepath.FromSlash(rel)))
	if obj == nil {
		return ""
	}
	for _, p := range paths {
		if v := jsonFieldString(obj, p); v != "" {
			return v
		}
	}
	return ""
}

// readJSONFile parses a JSON object; nil on any read/parse failure. Lines
// whose trimmed form starts with "//" are stripped first so JSONC configs
// (opencode.jsonc) still decode.
func readJSONFile(path string) map[string]json.RawMessage {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var kept []string
	for _, line := range strings.Split(string(blob), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			kept = append(kept, line)
		}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.Join(kept, "\n")), &obj); err != nil {
		return nil
	}
	return obj
}

// jsonFieldString resolves a dotted path to a string value ("" otherwise).
func jsonFieldString(obj map[string]json.RawMessage, path string) string {
	parts := strings.Split(path, ".")
	cur, ok := obj[parts[0]]
	if !ok {
		return ""
	}
	for _, p := range parts[1:] {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(cur, &nested); err != nil {
			return ""
		}
		cur, ok = nested[p]
		if !ok {
			return ""
		}
	}
	var s string
	if err := json.Unmarshal(cur, &s); err != nil {
		return ""
	}
	return s
}

// codexConfigTarget extracts the provider base URL codex itself would use —
// model_provider selects a [model_providers.<name>] table whose base_url is
// the effective endpoint — plus the env_key that provider names for its API
// key, so the probe can authenticate the same way the run would. No TOML
// dependency: codex writes the config in one shape, and a line scan over it
// is enough. Endpoint fallback order: the selected provider's base_url, a
// top-level base_url, then the first base_url found.
func codexConfigTarget(home string) (endpoint, envKey, bearer, wireAPI, model string) {
	blob, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		return "", "", "", "", ""
	}
	var provider, anyURL string
	section := ""
	urls := map[string]string{}
	envKeys := map[string]string{}
	bearers := map[string]string{}
	wires := map[string]string{}
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model_provider":
			provider = tomlValue(val)
		case "model":
			if section == "" {
				model = tomlValue(val)
			}
		case "base_url":
			u := tomlValue(val)
			if u == "" {
				continue
			}
			urls[section] = u
			if anyURL == "" {
				anyURL = u
			}
		case "env_key":
			envKeys[section] = tomlValue(val)
		case "experimental_bearer_token":
			bearers[section] = tomlValue(val)
		case "wire_api":
			wires[section] = tomlValue(val)
		}
	}
	if provider != "" {
		sec := "model_providers." + provider
		if u := urls[sec]; u != "" {
			return u, envKeys[sec], bearers[sec], wires[sec], model
		}
	}
	if u := urls[""]; u != "" {
		return u, envKeys[""], bearers[""], wires[""], model
	}
	return anyURL, "", "", "", model
}

// tomlStringValue returns the first value among keys in a flat TOML-ish
// file (e.g. grok's config.toml "model = ...").
func tomlStringValue(home, rel string, keys ...string) string {
	blob, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(blob), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		for _, k := range keys {
			if strings.TrimSpace(key) == k {
				return tomlValue(val)
			}
		}
	}
	return ""
}

// yamlNestedValue returns the scalar value of keys nested inside parent —
// for config like hermes's "model:\n  default: x" or dsh's
// "agent-default-model:\n  model: y" where the same key name also appears
// elsewhere in the file and a flat scan would hit the wrong one.
func yamlNestedValue(home, rel, parent string, keys ...string) string {
	blob, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(blob), "\n")
	parentIndent := -1
	for _, line := range lines {
		ind, key, val, ok := yamlKV(line)
		if !ok {
			continue
		}
		if parentIndent < 0 {
			if key == parent && val == "" {
				parentIndent = ind
			}
			continue
		}
		if ind <= parentIndent {
			return "" // dedented out of the parent block
		}
		for _, k := range keys {
			if key == k {
				return strings.Trim(val, `"'`)
			}
		}
	}
	return ""
}

// tomlValue strips quotes and trailing comments from a TOML scalar.
func tomlValue(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "#"); i >= 0 && !strings.HasPrefix(v, `"`) && !strings.HasPrefix(v, `'`) {
		v = strings.TrimSpace(v[:i])
	}
	return strings.Trim(v, `"'`)
}

// dotenvValue reads KEY=value lines from a dotenv file.
func dotenvValue(home, rel string, keys ...string) string {
	blob, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		for _, k := range keys {
			if strings.TrimSpace(key) == k {
				return strings.Trim(strings.TrimSpace(val), `"'`)
			}
		}
	}
	return ""
}

// yamlLineValue reads flat "key: value" lines from a YAML-ish file — enough
// for a harness config that stores base_url at top level.
func yamlLineValue(home, rel string, keys ...string) string {
	blob, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(blob), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		for _, k := range keys {
			if strings.TrimSpace(key) == k {
				return strings.Trim(strings.TrimSpace(val), `"'`)
			}
		}
	}
	return ""
}

// opencodeConfigTarget resolves the base URL of the provider opencode is
// configured with — provider.<name>.options.baseURL inside opencode.json(c),
// preferring the provider named by the "model": "provider/model" field —
// plus the bare model name (provider prefix stripped: the API request body
// wants "deepseek-chat", not "deepseek/deepseek-chat").
func opencodeConfigTarget(home string) (endpoint, model string) {
	var obj map[string]json.RawMessage
	for _, rel := range []string{".config/opencode/opencode.json", ".config/opencode/opencode.jsonc"} {
		if o := readJSONFile(filepath.Join(home, filepath.FromSlash(rel))); o != nil {
			obj = o
			break
		}
	}
	if obj == nil {
		return "", ""
	}
	if prov, m, ok := strings.Cut(jsonFieldString(obj, "model"), "/"); ok {
		model = m
		var providers map[string]json.RawMessage
		if raw, has := obj["provider"]; has && json.Unmarshal(raw, &providers) == nil {
			if u := providerBaseURL(providers[prov]); u != "" {
				return u, model
			}
		}
	} else {
		model = jsonFieldString(obj, "model")
	}
	var providers map[string]json.RawMessage
	if raw, ok := obj["provider"]; ok && json.Unmarshal(raw, &providers) == nil {
		for name := range providers {
			if u := providerBaseURL(providers[name]); u != "" {
				return u, model
			}
		}
	}
	return "", model
}

// providerBaseURL reads options.baseURL from one provider entry.
func providerBaseURL(raw json.RawMessage) string {
	var p struct {
		Options struct {
			BaseURL string `json:"baseURL"`
		} `json:"options"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return p.Options.BaseURL
}

// opencodeAuthKey extracts a provider credential from opencode's auth store
// so the probe can authenticate like the run would. auth.json holds either a
// flat credential or per-provider entries — try flat keys first, then the
// first non-empty credential found in any provider object.
func opencodeAuthKey(home string) string {
	obj := readJSONFile(filepath.Join(home, ".local/share/opencode/auth.json"))
	if obj == nil {
		return ""
	}
	if v := firstNonEmpty(
		jsonFieldString(obj, "apiKey"), jsonFieldString(obj, "token"),
		jsonFieldString(obj, "access_token"), jsonFieldString(obj, "key")); v != "" {
		return v
	}
	for _, raw := range obj {
		var entry map[string]json.RawMessage
		if json.Unmarshal(raw, &entry) != nil {
			continue
		}
		if v := firstNonEmpty(
			jsonFieldString(entry, "apiKey"), jsonFieldString(entry, "token"),
			jsonFieldString(entry, "access_token"), jsonFieldString(entry, "key"),
			jsonFieldString(entry, "options.apiKey")); v != "" {
			return v
		}
	}
	return ""
}
