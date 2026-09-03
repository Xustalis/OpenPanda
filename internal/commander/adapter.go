package commander

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// timeoutKey is the context key carrying a per-task agent timeout override.
// When set, runAdapterProcess uses it instead of the global adapterTimeoutS,
// so task kinds with different runtime characteristics (training vs QA) can
// each get their own budget without changing the process-global default.
type timeoutKey struct{}

// WithAgentTimeout attaches a per-task agent execution timeout to the context;
// runAdapterProcess honours it over the global SetAgentTimeout value.
func WithAgentTimeout(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, timeoutKey{}, d)
}

// progressKey is the context key carrying the live progress sink from the
// orchestration layer (core's execute → RecordEvent) down to the adapter
// process reader, without widening every execution-path signature.
type progressKey struct{}

// resumeKey carries the agent session id a follow-up round continues: the
// supervision loop threads the previous run's session id here so the adapter
// resumes the agent's own conversation instead of cold-starting.
type resumeKey struct{}

// WithResume attaches an agent session id to the execution context;
// runAdapterProcess copies it into AdapterRequest.Resume. Empty is a no-op.
func WithResume(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, resumeKey{}, sessionID)
}

// toolsPolicyKey carries the router's agent tools policy (minimal |
// extended) down to the adapter request without widening the runAdapter
// test seam's signature.
type toolsPolicyKey struct{}

// WithToolsPolicy attaches the normalized tools policy to an execution
// context. The orchestration layer sets it on the execution context to
// override with a per-task policy from the task spec; the Router applies its
// global policy (routing.tools_policy) only when the context carries no
// task-level policy yet, so a per-task override always takes precedence.
func WithToolsPolicy(ctx context.Context, policy string) context.Context {
	if policy == "" {
		return ctx
	}
	return context.WithValue(ctx, toolsPolicyKey{}, policy)
}

// ProgressFunc receives one adapter progress note (a short human-readable
// line, e.g. "Bash: du -ah | sort -rh") as the agent works. The kind
// parameter tags typed events: "" for ordinary tool notes, "subagent" when
// the agent spawns a sub-agent (Claude's Task tool), etc. The orchestration
// layer records the kind so the task timeline shows the delegation chain.
type ProgressFunc func(note, kind string)

// WithProgress attaches a progress sink to an execution context. Adapters
// emit NDJSON progress objects on stderr; runAdapterProcess parses them and
// forwards the note here, so the task's event timeline fills in while the
// agent runs instead of only when it finishes. Nil/absent = no sink; the
// parsing still isolates progress lines from the diagnostic stderr.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

// progressWriter splits an adapter's stderr stream: lines that parse as
// {"type":"progress","note":…} go to the sink (if any); everything else is
// retained for failure diagnosis exactly like the old raw Capture. Writes
// are called from the cmd's scanner goroutine (cmd.Run's copier), so the
// sink must be safe for concurrent use — RecordEvent is.
type progressWriter struct {
	capture executil.Capture
	sink    ProgressFunc
	// partial holds the bytes of the current line that have no terminating
	// '\n' yet. The pipe copier may split one stderr line across several
	// Write calls; buffering here keeps a split line from being misread as a
	// complete line (which would drop a progress note or corrupt diagnostics).
	partial []byte
}

// maxProgressLine bounds partial: a progress line is a short JSON object, so
// anything this long is noise and is spilled to diagnostics, keeping partial
// from growing without limit against pathological stderr (D13).
const maxProgressLine = 4096

func (w *progressWriter) Write(p []byte) (int, error) {
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.line(w.partial[:i])
		w.partial = w.partial[i+1:]
	}
	if len(w.partial) > maxProgressLine {
		w.capture.Write(w.partial)
		w.partial = nil
	}
	return len(p), nil
}

// line handles one complete, '\n'-terminated stderr line (a line is buffered
// in partial by Write until its newline arrives).
func (w *progressWriter) line(b []byte) {
	var probe struct {
		Type string `json:"type"`
		Note string `json:"note"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &probe); err == nil && probe.Type == "progress" && probe.Note != "" {
		note := strings.TrimSpace(probe.Note)
		if len([]rune(note)) > 300 {
			note = string([]rune(note)[:300]) + "\u2026"
		}
		if w.sink != nil {
			w.sink(note, probe.Kind)
		}
		return
	}
	w.capture.Write(b)
	w.capture.Write([]byte("\n"))
}

// String returns the non-progress stderr retained for diagnostics, flushing
// any unterminated trailing bytes (not a complete line, so never progress)
// into the capture first.
func (w *progressWriter) String() string {
	if len(w.partial) > 0 {
		w.capture.Write(w.partial)
		w.partial = nil
	}
	return w.capture.String()
}

// adapterDir is where adapter scripts live; a var so tests can point it at a
// temp dir. Relative values are resolved against the daemon cwd (repo root)
// and then beside the running binary (bin/panda → ../adapters) — the sandbox
// sets the adapter's cwd to the TASK work dir, so a bare relative script path
// would make python look for adapters/ inside the task dir and die with exit 2.
var adapterDir = "adapters"

// adapterDirEnv lets integration environments point a packaged panda binary
// at scenario adapters without copying them into the installation directory.
// Production keeps the bundled adapters/ default; tests and multi-node labs
// can set OPENPANDA_ADAPTER_DIR to an absolute, controlled directory.
const adapterDirEnv = "OPENPANDA_ADAPTER_DIR"

// AdapterDir returns the directory the current process resolves adapter
// scripts from. The self-updater uses it to install updated adapter scripts
// beside the running binary without re-deriving the resolution rules.
func AdapterDir() string { return resolveAdapterDir() }

// resolveAdapterDir absolutizes a relative adapterDir by probing, in order:
// the process cwd, each ancestor of the cwd (repo-subdir runs), and the
// directories beside the running binary (packaged installs). If nothing
// matches, the cwd-absolute path stands so the spawn error names a stable
// path instead of one re-resolved against the sandbox's task cwd.
func resolveAdapterDir() string {
	if override := strings.TrimSpace(os.Getenv(adapterDirEnv)); override != "" {
		if filepath.IsAbs(override) {
			return filepath.Clean(override)
		}
		// Resolve once against the process cwd before the sandbox changes the
		// adapter subprocess cwd to the task work directory.
		if abs, err := filepath.Abs(override); err == nil {
			return abs
		}
		return override
	}
	if filepath.IsAbs(adapterDir) {
		return adapterDir
	}
	if abs, err := filepath.Abs(adapterDir); err == nil {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return abs
		}
		// Walk up from the cwd: `panda` started anywhere inside a repo checkout
		// (e.g. webui/) still finds the repo's adapters/ without the env var.
		for dir := filepath.Dir(abs); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			cand := filepath.Join(dir, "adapters")
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				return cand
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		for _, cand := range adapterCandidateDirs(exe) {
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				return cand
			}
		}
	}
	// No adapters/ found anywhere: keep the cwd-absolute path so the spawn
	// error names a stable location instead of one re-resolved against the
	// sandbox's task directory (which would mislead diagnosis).
	if abs, err := filepath.Abs(adapterDir); err == nil {
		return abs
	}
	return adapterDir
}

// adapterCandidateDirs lists the adapters/ directories to probe for a given
// executable path, in priority order. A packaged install (one-click script,
// Homebrew) symlinks the real binary onto PATH (e.g. ~/.local/bin/panda →
// ~/.local/share/openpanda/panda), so we follow the link and look beside the
// real binary — alongside the repo-layout “../adapters“ fallback — or the
// spawned adapter would name a missing path and die.
func adapterCandidateDirs(exe string) []string {
	if exe == "" {
		return nil
	}
	real := exe
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		real = r
	}
	var out []string
	seen := map[string]bool{}
	for _, base := range []string{exe, real} {
		for _, p := range []string{
			filepath.Join(filepath.Dir(base), "adapters"),
			filepath.Join(filepath.Dir(base), "..", "adapters"),
		} {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// adapterPath joins an adapter name under adapterDir, rejecting any name that
// could escape it via a path separator or a ".." element (P2-5). Adapter names
// are flat filenames, so anything path-like is a traversal attempt.
func adapterPath(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid adapter name %q", name)
	}
	return filepath.Join(resolveAdapterDir(), name), nil
}

// AdapterRequest is written to the adapter's stdin.
type AdapterRequest struct {
	Prompt   string `json:"prompt"`
	TimeoutS int    `json:"timeout_s"`
	CWD      string `json:"cwd,omitempty"`
	// Resume carries the agent session id a previous run returned, so a
	// follow-up round (the supervision loop's "continue" verdict) resumes the
	// agent's own conversation instead of cold-starting on the bare follow-up
	// text. Adapters without session support ignore it.
	Resume string `json:"resume,omitempty"`
	// ToolsPolicy grades the tool face the adapter runs with: minimal (or
	// empty) keeps the adapter's safe whitelist; extended lifts it so the
	// agent's native skills, sub-agent tooling and project MCP servers are
	// reachable. Set by the Router from routing.tools_policy.
	ToolsPolicy string `json:"tools_policy,omitempty"`
}

// UsageDetail is the structured token breakdown an adapter reports alongside
// the flat Tokens total: the total feeds the existing pipeline unchanged,
// the breakdown feeds observability (agent_usage task events) so input,
// output and cache traffic can be told apart.
type UsageDetail struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// modelEnv builds the model provider env injected into the adapter process
// when the injection policy says so (see Router.InjectionDecision). Secrets
// are passed only via env and never echoed to logs. Empty base_url/model
// fall back to the same defaults the entry model applies, so the adapter and
// entry never diverge.
func modelEnv(model config.ModelConfig) []string {
	return modelEnvForAdapter(model, "claude_code.py")
}

// modelEnvForAdapter maps PANDA's provider config onto the adapter's env
// contract declared in the agent registry (credential manifest); adapters
// without a declared mapping — or a config the mapping cannot carry — get no
// override. The DeepSeek flash guard applies here too (effectiveModelName):
// deepseek-v4-pro is never injected on any path.
func modelEnvForAdapter(model config.ModelConfig, adapter string) []string {
	if !supportsModelInjection(adapter, model) {
		return nil
	}
	k, _ := agents.ByAdapter(adapter)
	return []string{
		k.ModelEnv.BaseURL + "=" + effectiveBaseURL(model),
		k.ModelEnv.APIKey + "=" + model.APIKey,
		k.ModelEnv.Model + "=" + effectiveModelName(model),
	}
}

// adapterCredentialEnv preserves only credentials explicitly belonging to the
// selected adapter, per its registry credential manifest. Sandbox.Apply
// clears the parent environment, so without this bridge native Claude/Codex
// credentials detected by InjectionDecision would disappear before the CLI
// starts.
func adapterCredentialEnv(adapter string) []string {
	k, ok := agents.ByAdapter(adapter)
	if !ok {
		return nil
	}
	var out []string
	for _, key := range k.CredentialEnvVars {
		if value := os.Getenv(key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	return out
}

// mergeAdapterEnv builds a duplicate-free environment. Injected values replace
// native values for the same key, which is important for injection.model=always:
// child CLIs must see one deterministic provider credential, not two entries
// whose precedence depends on libc/runtime behavior.
func mergeAdapterEnv(native, injected []string) []string {
	values := make(map[string]string, len(native)+len(injected))
	order := make([]string, 0, len(native)+len(injected))
	add := func(entries []string) {
		for _, kv := range entries {
			key, _, ok := strings.Cut(kv, "=")
			if !ok || key == "" {
				continue
			}
			if _, exists := values[key]; !exists {
				order = append(order, key)
			}
			values[key] = kv[strings.IndexByte(kv, '=')+1:]
		}
	}
	add(native)
	add(injected)
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}

// adapterTimeoutS is the budget advertised to the adapter in its request.
// adapterHardTimeout is the enforced wall-clock limit (P1-18): TimeoutS used
// to be a polite JSON suggestion an adapter could ignore and run forever, so
// the Go side wraps the spawn in a hard context deadline slightly past the
// advertised budget. Combined with process-group cancellation (executil),
// hitting the deadline kills the adapter and every CLI it spawned.
// Both are vars: SetAgentTimeout retunes them from config at startup, and tests
// shrink them.
const defaultAdapterTimeoutS = 600

// hardTimeoutGrace is how far past the advertised budget the enforced deadline
// sits, giving a well-behaved adapter room to wind down and report.
const hardTimeoutGrace = 30 * time.Second

var (
	adapterTimeoutS    = defaultAdapterTimeoutS
	adapterHardTimeout = defaultAdapterTimeoutS*time.Second + hardTimeoutGrace
)

// SetAgentTimeout retunes the agent-adapter execution budget. A deep-learning
// stage legitimately runs far longer than a code edit, so the limit has to be
// operator-tunable rather than a compile-time constant. Values under a minute
// are ignored as misconfiguration. Process-global: call it during startup,
// before any task executes.
func SetAgentTimeout(d time.Duration) {
	if d < time.Minute {
		return
	}
	adapterTimeoutS = int(d / time.Second)
	adapterHardTimeout = d + hardTimeoutGrace
}

// AgentHardTimeout reports the enforced wall-clock limit for one agent
// execution. A task's lease must exceed it, or the lease monitor force-fails
// work that is still legitimately running — see core.Core.SetTimeouts.
func AgentHardTimeout() time.Duration { return adapterHardTimeout }

// runAdapterProcess spawns adapters/<name> with a JSON request on stdin and
// reads a JSON result from stdout. env carries the model-provider override
// when the injection policy decided one is needed (empty otherwise — the
// agent then uses its own model); the subprocess is sandboxed to the task
// directory with a minimal environment (see security.Sandbox). stderr is
// split through progressWriter: NDJSON progress lines go to the context's
// sink, the rest is retained for failure diagnosis.
func runAdapterProcess(ctx context.Context, name string, prompt string, cwd string, env []string) AgentResult {
	timeout := adapterTimeoutS
	if d, ok := ctx.Value(timeoutKey{}).(time.Duration); ok && d > 0 {
		timeout = int(d / time.Second)
	}
	req := AdapterRequest{Prompt: prompt, TimeoutS: timeout, CWD: cwd}
	if rid, ok := ctx.Value(resumeKey{}).(string); ok {
		req.Resume = rid
	}
	if policy, ok := ctx.Value(toolsPolicyKey{}).(string); ok {
		req.ToolsPolicy = policy
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return AgentResult{OK: false, Result: "bad adapter request", ExitCode: 1}
	}

	path, err := adapterPath(name)
	if err != nil {
		return AgentResult{OK: false, Result: security.Redact(err.Error()), ExitCode: 1}
	}
	ctx, cancel := context.WithTimeout(ctx, adapterHardTimeout)
	defer cancel()
	cmd, ok := pyexec.Command(ctx, path)
	if !ok {
		// No interpreter on this host. Say so instead of exec'ing a name that
		// is not there: "python3: no such file or directory" reads like a
		// PANDA bug, and on Windows the bare name can resolve to a Store stub
		// that fails in a way nobody can act on. AgentViable checks the same
		// thing up front so a task normally never reaches here.
		return AgentResult{
			OK: false, ExitCode: 127,
			Result: "no Python 3 interpreter found for adapter " + name +
				" — install Python 3 or set " + pyexec.EnvOverride,
		}
	}
	cmd.Stdin = bytes.NewReader(reqJSON)
	var stdout executil.Capture
	var stderr progressWriter
	if sink, ok := ctx.Value(progressKey{}).(ProgressFunc); ok {
		stderr.sink = sink
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	security.NewSandbox(cwd).Apply(cmd, env...)

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return AgentResult{OK: false, Result: "adapter timed out (hard limit)", ExitCode: 124}
		}
		code := 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		msg := stderr.String()
		if msg == "" {
			msg = err.Error()
		}
		return AgentResult{OK: false, Result: security.Redact(msg), ExitCode: code}
	}

	var out AgentResult
	out.ExitCode = 0
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return AgentResult{OK: false, Result: security.Redact(fmt.Sprintf("adapter output not JSON: %s", stdout.String())), ExitCode: 1}
	}
	// A well-behaved adapter returns JSON; scrub anything secret-shaped it may
	// have echoed into the result before it enters the task/log pipeline.
	out.Result = security.Redact(out.Result)
	out.Stderr = security.Redact(stderr.String())
	return out
}

// transientStatusRE matches a bare HTTP 5xx status token. Word boundaries
// keep the token from matching glued digits ("5000", "15020") — a real task
// failure that mentions a large number or a port must not read as provider 5xx.
var transientStatusRE = regexp.MustCompile(`\b(?:500|502|503|504)\b`)

// transientAgentFailure reports whether an adapter failure looks like
// provider-side turbulence rather than the task itself failing: rate
// limits, overload, 5xx/api errors, dropped connections. The patterns are
// deliberately narrow — an agent that did real work and then failed
// ("command not found", a failed test, a refused permission) never matches,
// so a retry cannot silently duplicate side effects.
func transientAgentFailure(ar AgentResult) bool {
	if ar.OK {
		return false
	}
	msg := strings.ToLower(ar.Result)
	for _, pat := range []string{
		"rate limit", "rate_limit", "429", "overloaded",
		"api error", "bad gateway", "service unavailable",
		"connection reset", "connection refused", "unexpected eof",
		"timed out reading response",
	} {
		if strings.Contains(msg, pat) {
			return true
		}
	}
	// Bare 5xx status mentions ("error 502", "HTTP 503").
	if transientStatusRE.MatchString(msg) {
		return true
	}
	return false
}

// contextOverflowPatterns are the provider-side ways of saying "the prompt
// plus history no longer fits the model's window". Such a failure is
// deterministic: retrying the same prompt cannot fit it, so the upper layer
// parks for a human (compress / split / reduce scope) instead of re-running.
var contextOverflowPatterns = []string{
	"context length", "context_length_exceeded", "maximum context",
	"context window", "prompt is too long", "too many tokens",
	"reduce the length",
}

// ContextOverflow reports whether a failure text looks like the agent's
// context window overflowing. Exported for the orchestration layer, which
// parks such failures in review rather than retrying them.
func ContextOverflow(text string) bool {
	if text == "" {
		return false
	}
	msg := strings.ToLower(text)
	for _, pat := range contextOverflowPatterns {
		if strings.Contains(msg, pat) {
			return true
		}
	}
	return false
}
