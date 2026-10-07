// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// ResumeID reads the session id WithResume attached — "" when none. The
// adapter runner and tests use it to observe what the supervision loop
// threaded through.
func ResumeID(ctx context.Context) string {
	if v, ok := ctx.Value(resumeKey{}).(string); ok {
		return v
	}
	return ""
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

// remoteTaskKey marks an execution whose prompt was authored on another node.
// Remote intent is untrusted text: a mesh member (or anything that ever
// rides a relay) controls the words an agent subprocess acts on, so without
// the origin's consent the run must not get a writable, shell-capable tool
// face — the same reasoning that keeps tier-2 behind Authorize applies to a
// prompt that IS the command.
type remoteTaskKey struct{}

// WithRemoteTask marks the execution context as carrying a remote-origin
// task. Execute uses it to hold agent plans to the restricted tool face
// whenever the task arrived unconsented.
func WithRemoteTask(ctx context.Context) context.Context {
	return context.WithValue(ctx, remoteTaskKey{}, true)
}

// RemoteTask reports whether the execution context carries a remote-origin
// task (set by the orchestration layer before Execute).
func RemoteTask(ctx context.Context) bool {
	v, _ := ctx.Value(remoteTaskKey{}).(bool)
	return v
}

// restrictedKey asks the adapter for its narrowest read-only tool face. It is
// set on the run context, not the request the caller writes, so the runAdapter
// test seam's signature stays unchanged.
type restrictedKey struct{}

// WithRestricted asks the adapter subprocess to run with only read-capable
// tools. Adapters without a restricted mode must never receive it — the
// execAgent loop filters on AdapterSupportsRestricted before spawning.
func WithRestricted(ctx context.Context) context.Context {
	return context.WithValue(ctx, restrictedKey{}, true)
}

// Restricted reports whether the context asks for a read-only agent run.
func Restricted(ctx context.Context) bool {
	v, _ := ctx.Value(restrictedKey{}).(bool)
	return v
}

// AdapterSupportsRestricted reports whether the named adapter can honor a
// read-only run. The answer comes from the agent registry's capability
// declaration (Capabilities.SupportsRestricted), not a local table, so the
// allowlist cannot drift from the manifest that describes the adapter.
// Unknown adapters get false — a miss means refuse.
func AdapterSupportsRestricted(adapter string) bool {
	k, ok := agents.ByAdapter(adapter)
	return ok && k.Capabilities.SupportsRestricted
}

// maxTurnsKey carries a per-task agent turn cap (spec.max_turns) down to the
// adapter request without widening the runAdapter seam's signature.
type maxTurnsKey struct{}

// WithMaxTurns attaches a per-task agent turn cap to the execution context;
// runAdapterProcess copies it into AdapterRequest.MaxTurns. Non-positive is a
// no-op — the adapter then falls back to its own default.
func WithMaxTurns(ctx context.Context, n int) context.Context {
	if n <= 0 {
		return ctx
	}
	return context.WithValue(ctx, maxTurnsKey{}, n)
}

// agentCmdKey carries the card-declared argv template for the generic adapter
// (ledger.Agent.Command) down to the request without widening the runAdapter
// seam's signature.
type agentCmdKey struct{}

// WithAgentCommand attaches a command template to the execution context;
// runAdapterProcess copies it into AdapterRequest.Cmd for adapters that read
// it (generic.py). Empty is a no-op.
func WithAgentCommand(ctx context.Context, cmd string) context.Context {
	if cmd == "" {
		return ctx
	}
	return context.WithValue(ctx, agentCmdKey{}, cmd)
}

// agentNameKey carries the card's agent name (the map key in
// card.Agents, e.g. "claude_code") down to runAdapterDefault, so
// registry lookups can key on the agent rather than the adapter script —
// essential for generic.py, where the script name alone cannot identify
// which CLI the card wired.
type agentNameKey struct{}

// WithAgentName attaches the card agent name to the execution context;
// runAdapterDefault reads it for name-aware registry lookups. Empty is a
// no-op.
func WithAgentName(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, agentNameKey{}, name)
}

// AgentName reads the card agent name WithAgentName attached — "" when none.
func AgentName(ctx context.Context) string {
	if v, ok := ctx.Value(agentNameKey{}).(string); ok {
		return v
	}
	return ""
}

// taskIDKey carries the OpenPanda task ID down to the adapter subprocess.
type taskIDKey struct{}

// WithTaskID attaches an OpenPanda task ID to the execution context;
// runAdapterProcess injects PANDA_TASK_ID into the spawned process environment
// and sets task_id in the adapter request JSON.
func WithTaskID(ctx context.Context, taskID string) context.Context {
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, taskIDKey{}, taskID)
}

// TaskID reads the task ID WithTaskID attached — "" when none.
func TaskID(ctx context.Context) string {
	if v, ok := ctx.Value(taskIDKey{}).(string); ok {
		return v
	}
	return ""
}

// systemPromptKey carries the adapter-level system-prompt rider
// (--append-system-prompt) without widening the runAdapter seam.
type systemPromptKey struct{}

// WithSystemPrompt attaches the static protocol/system rider text the
// adapter passes to the CLI's append-system-prompt flag. Empty is a no-op.
func WithSystemPrompt(ctx context.Context, text string) context.Context {
	if text == "" {
		return ctx
	}
	return context.WithValue(ctx, systemPromptKey{}, text)
}

// resultSchemaKey carries the JSON Schema a run's result validates against.
type resultSchemaKey struct{}

// WithResultSchema attaches the JSON Schema for the run's structured
// output; adapters whose CLI lacks the flag ignore it and the marker
// protocol remains the fallback. Empty is a no-op.
func WithResultSchema(ctx context.Context, schema string) context.Context {
	if schema == "" {
		return ctx
	}
	return context.WithValue(ctx, resultSchemaKey{}, schema)
}

// mcpConfigKey carries the ready-made {"mcpServers":{…}} document an
// MCPConfigFlag-capable adapter passes to its CLI.
type mcpConfigKey struct{}

// WithMCPConfig attaches the passthrough MCP config document. Empty is a
// no-op.
func WithMCPConfig(ctx context.Context, doc string) context.Context {
	if doc == "" {
		return ctx
	}
	return context.WithValue(ctx, mcpConfigKey{}, doc)
}

// effortKey carries the per-task reasoning-effort level to the adapter.
type effortKey struct{}

// WithEffort attaches a reasoning-effort level (e.g. "high"); adapters
// without an effort flag ignore it. Empty is a no-op.
func WithEffort(ctx context.Context, effort string) context.Context {
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, effortKey{}, effort)
}

// sessionKey requests the adapter's interactive session mode.
type sessionKey struct{}

// WithSessionMode asks the adapter to keep the agent process alive across
// turns. Only set for adapters whose registry entry declares
// Capabilities.SupportsSession.
func WithSessionMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, sessionKey{}, true)
}

// SessionMode reports whether the context asks for session execution.
func SessionMode(ctx context.Context) bool {
	v, _ := ctx.Value(sessionKey{}).(bool)
	return v
}

// ProgressFunc receives one adapter progress note (a short human-readable
// line, e.g. "Bash: du -ah | sort -rh") as the agent works. The kind
// parameter tags typed events: "" for ordinary tool notes, "subagent" when
// the agent spawns a sub-agent (Claude's Task tool), etc. The orchestration
// layer records the kind so the task timeline shows the delegation chain.
type ProgressFunc func(note, kind string)

// AgentEvent is one structured activity block the adapter streamed back —
// a text/thinking/tool_use/tool_result block from the harness's event
// feed. Parent carries the enclosing tool_use id when the block ran inside
// a sub-agent (e.g. a Claude Task call), which is what lets the display
// layer nest the delegation tree.
type AgentEvent struct {
	// Ev is the block kind: "text" | "thinking" | "tool_use" |
	// "tool_result" | adapter-specific extensions.
	Ev string `json:"ev"`
	// ID is the block's own id (tool_use blocks), Parent the enclosing
	// tool_use id for sub-agent content.
	ID     string `json:"id,omitempty"`
	Parent string `json:"parent,omitempty"`
	// Name is the tool name for tool_use; ToolUseID the call a tool_result
	// answers.
	Name      string `json:"name,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Text/Thinking/Content carry the block's body (bounded by the adapter
	// and hard-capped again here); Input is the tool_use argument object.
	Text     string          `json:"text,omitempty"`
	Thinking string          `json:"thinking,omitempty"`
	Content  string          `json:"content,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`
}

// AgentEventFunc receives one structured agent-activity event. It is the
// transcript channel — every block the harness emitted — where ProgressFunc
// is the compact status line.
type AgentEventFunc func(AgentEvent)

// WithProgress attaches a compact progress-note sink to an execution
// context: the agent's one-line status notes ("Bash: ls -la", "subagent:
// Explore") the orchestration layer records on the task timeline. Nil =
// notes are parsed out of diagnostics and dropped.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

// agentEventsKey carries the structured transcript sink without widening
// the runAdapter seam's signature.
type agentEventsKey struct{}

// WithAgentEvents attaches a structured-event sink to an execution context.
// Adapters emit {"type":"event",…} NDJSON on stderr alongside the progress
// notes; runAdapterProcess parses and forwards them here. Nil/absent = the
// events are parsed out of diagnostics and dropped.
func WithAgentEvents(ctx context.Context, fn AgentEventFunc) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, agentEventsKey{}, fn)
}

// AgentEvents reads the structured-event sink WithAgentEvents attached —
// nil when none.
func AgentEvents(ctx context.Context) AgentEventFunc {
	if v, ok := ctx.Value(agentEventsKey{}).(AgentEventFunc); ok {
		return v
	}
	return nil
}

// progressWriter splits an adapter's stderr stream: {"type":"progress",…}
// lines go to the compact-note sink (if any), {"type":"event",…} lines go
// to the structured transcript sink; everything else is retained for
// failure diagnosis exactly like the old raw Capture. Writes are called
// from the cmd's scanner goroutine (cmd.Run's copier), so the sinks must be
// safe for concurrent use — RecordEvent is.
type progressWriter struct {
	capture    executil.Capture
	sink       ProgressFunc
	evSink     AgentEventFunc
	onActivity func() // set before Start; read-only afterwards
	// mu guards partial and capture: a session-mode caller snapshots
	// diagnostics mid-stream (await) while the copier goroutine is still
	// writing, and bytes.Buffer is not concurrent-safe.
	mu sync.Mutex
	// partial holds the bytes of the current line that have no terminating
	// '\n' yet. The pipe copier may split one stderr line across several
	// Write calls; buffering here keeps a split line from being misread as a
	// complete line (which would drop a progress note or corrupt diagnostics).
	partial []byte
}

// maxProgressLine bounds partial: an event line carries bounded-but-real
// payload (the adapter clamps text fields at ~16KB), so anything past this
// is noise spilled to diagnostics, keeping partial from growing without
// limit against pathological stderr (D13).
const maxProgressLine = 24 * 1024

func (w *progressWriter) Write(p []byte) (int, error) {
	if w.onActivity != nil && len(p) > 0 {
		w.onActivity()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
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

// activityWriter wraps an executil.Capture and triggers onActivity on every write.
type activityWriter struct {
	capture    executil.Capture
	onActivity func()
}

func (a *activityWriter) Write(p []byte) (int, error) {
	if a.onActivity != nil && len(p) > 0 {
		a.onActivity()
	}
	return a.capture.Write(p)
}

func (a *activityWriter) Bytes() []byte  { return a.capture.Bytes() }
func (a *activityWriter) String() string { return a.capture.String() }

// line handles one complete, '\n'-terminated stderr line (a line is buffered
// in partial by Write until its newline arrives).
func (w *progressWriter) line(b []byte) {
	var probe struct {
		Type string `json:"type"`
		Note string `json:"note"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &probe); err == nil {
		switch probe.Type {
		case "progress":
			note := strings.TrimSpace(probe.Note)
			if note == "" {
				// A well-formed progress envelope with an empty note carries
				// no information; drop it rather than parking a blank event
				// on the task timeline.
				return
			}
			if len([]rune(note)) > 300 {
				note = string([]rune(note)[:300]) + "\u2026"
			}
			if w.sink != nil {
				// The note lands in the task event stream and logs: scrub
				// anything secret-shaped the adapter echoed into it.
				w.sink(security.Redact(note), probe.Kind)
			}
			return
		case "event":
			w.event(b)
			return
		}
	}
	w.capture.Write(b)
	w.capture.Write([]byte("\n"))
}

// maxAgentEventText is the Go-side hard cap per text-bearing event field.
// The adapter clamps tighter (≈16KB) but the boundary re-checks: a buggy or
// non-conforming adapter cannot overflow a task event row.
const maxAgentEventText = 32 * 1024

// maxAgentEventInput bounds a tool_use input blob stored on the event.
const maxAgentEventInput = 8 * 1024

// event parses one {"type":"event",…} line and forwards it to the
// structured sink, after redaction and the hard bounds. Malformed events
// are dropped, not diagnosed: the CLI's own transcript stays authoritative.
func (w *progressWriter) event(b []byte) {
	if w.evSink == nil {
		return
	}
	var ev struct {
		Ev        string          `json:"ev"`
		ID        string          `json:"id"`
		Parent    string          `json:"parent"`
		Name      string          `json:"name"`
		ToolUseID string          `json:"tool_use_id"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Content   string          `json:"content"`
		Input     json.RawMessage `json:"input"`
		IsError   bool            `json:"is_error"`
	}
	if err := json.Unmarshal(b, &ev); err != nil || ev.Ev == "" {
		return
	}
	clamp := func(s string) string {
		if r := []rune(s); len(r) > maxAgentEventText {
			return string(r[:maxAgentEventText]) + "…"
		}
		return s
	}
	out := AgentEvent{
		Ev:        ev.Ev,
		ID:        ev.ID,
		Parent:    ev.Parent,
		Name:      ev.Name,
		ToolUseID: ev.ToolUseID,
		Text:      security.Redact(clamp(ev.Text)),
		Thinking:  security.Redact(clamp(ev.Thinking)),
		Content:   security.Redact(clamp(ev.Content)),
		IsError:   ev.IsError,
	}
	if len(ev.Input) > 0 {
		if len(ev.Input) > maxAgentEventInput {
			// An oversized input degrades to a quoted string of its head:
			// valid JSON either way, and rune-truncation keeps the cut from
			// splitting a multi-byte UTF-8 sequence mid-rune.
			r := []rune(string(ev.Input))
			if len(r) > maxAgentEventInput {
				r = r[:maxAgentEventInput]
			}
			out.Input = json.RawMessage(strconv.Quote(string(r) + "…"))
		} else {
			out.Input = ev.Input
		}
	}
	w.evSink(out)
}

// String returns the non-progress stderr retained for diagnostics, flushing
// any unterminated trailing bytes (not a complete line, so never progress)
// into the capture first. Call only after the copier has stopped — the
// mid-stream variant is diag.
func (w *progressWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		w.capture.Write(w.partial)
		w.partial = nil
	}
	return w.capture.String()
}

// diag snapshots the diagnostic buffer WITHOUT consuming an in-flight
// partial line: a session-mode reader (await) samples diagnostics while the
// copier is still writing, and flushing partial here would split a line that
// completes on the next Write.
func (w *progressWriter) diag() string {
	w.mu.Lock()
	defer w.mu.Unlock()
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
	add := func(p string) {
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, base := range []string{exe, real} {
		add(filepath.Join(filepath.Dir(base), "adapters"))
		add(filepath.Join(filepath.Dir(base), "..", "adapters"))
		add(filepath.Join(filepath.Dir(base), "..", "share", "openpanda", "adapters"))
	}
	return out
}

// adapterPath joins an adapter name under adapterDir, rejecting any name that
// could escape it via a path separator or a ".." element (P2-5). Adapter names
// are flat filenames, so anything path-like is a traversal attempt. A colon is
// rejected too — on Windows "C:" / "file:stream" name a drive or an alternate
// data stream, and no legitimate adapter name needs one.
func adapterPath(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`) {
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
	// Restricted asks for the adapter's read-only mode and OUTRANKS
	// ToolsPolicy: an unconsented remote task on an operator who chose
	// extended still gets no shell. Only adapters whose registry entry
	// declares Capabilities.SupportsRestricted ever see it set.
	Restricted bool `json:"restricted,omitempty"`
	// TaskID carries the OpenPanda task ID that this process executes for.
	// Used for causal subagent tree linkage and subtask dispatch.
	TaskID string `json:"task_id,omitempty"`
	// Cmd is the argv template a generic adapter expands ({prompt}
	// placeholder). It comes from the card's agents.<name>.command field, so
	// a node can wire a new CLI without shipping a bespoke adapter script.
	Cmd string `json:"cmd,omitempty"`
	// MaxTurns is a per-task agent turn cap from the task spec
	// (spec.max_turns). Adapters without a turn-limit flag ignore it; those
	// that have one apply it instead of their own default.
	MaxTurns int `json:"max_turns,omitempty"`
	// SystemPrompt is appended to the agent's own system prompt
	// (--append-system-prompt on claude): the static protocol riders ride
	// there so the user prompt carries only the task and the stable prefix
	// caches across rounds. Ignored by adapters without the flag.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// ResultSchema is a JSON Schema the run's final reply validates against
	// (claude --json-schema): status/question/delegate_requests arrive
	// parsed instead of as text markers. Empty = no structured contract.
	ResultSchema string `json:"result_schema,omitempty"`
	// MCPConfig is a ready-made {"mcpServers":{…}} document for CLIs that
	// take an MCP config flag (claude --mcp-config): the passthrough servers
	// ride the request instead of a .mcp.json written into the work dir.
	MCPConfig string `json:"mcp_config,omitempty"`
	// MaxBudgetUSD caps the run's spend inside the harness where the CLI
	// supports it (claude --max-budget-usd). 0 = unset.
	MaxBudgetUSD float64 `json:"max_budget_usd,omitempty"`
	// Effort names the CLI's reasoning-effort level (claude --effort).
	Effort string `json:"effort,omitempty"`
	// Session asks the adapter to keep the agent process alive across
	// turns: the request is followed by line-framed {"type":"user","text"}
	// turn inputs and each turn produces one result envelope (Phase 6).
	Session bool `json:"session,omitempty"`
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
	return modelEnvForAdapter(model, "claude_code", "claude_code.py")
}

// modelEnvForAdapter maps PANDA's provider config onto the adapter's env
// contract declared in the agent registry (credential manifest); adapters
// without a declared mapping — or a config the mapping cannot carry — get no
// override. The registry record is resolved by agent name + adapter (see
// agents.Lookup), so a custom CLI on generic.py never receives another
// agent's env mapping. The DeepSeek flash guard applies here too
// (effectiveModelName): deepseek-v4-pro is never injected on any path.
func modelEnvForAdapter(model config.ModelConfig, name, adapter string) []string {
	if !supportsModelInjection(name, adapter, model) {
		return nil
	}
	k, _ := agents.Lookup(name, adapter)
	if k.ModelEnv == nil {
		return nil
	}
	var env []string
	if k.ModelEnv.BaseURL != "" {
		env = append(env, k.ModelEnv.BaseURL+"="+effectiveBaseURLFor(name, adapter, model))
	}
	if k.ModelEnv.APIKey != "" {
		env = append(env, k.ModelEnv.APIKey+"="+model.APIKey)
	}
	if k.ModelEnv.Model != "" {
		env = append(env, k.ModelEnv.Model+"="+effectiveModelNameFor(name, adapter, model))
	}
	env = append(env, "OPENPANDA_INJECTED_MODEL=1",
		// The configured protocol rides alongside the endpoint vars so
		// adapters for multi-protocol CLIs (pi, which declares a dialect
		// per provider) know which wire format the endpoint speaks.
		"OPENPANDA_MODEL_API_TYPE="+model.NormalizedAPIType())
	if adapter == "claude_code.py" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN=")
	}
	return env
}

// adapterCredentialEnv preserves only credentials explicitly belonging to the
// selected agent, per its registry credential manifest. Sandbox.Apply
// clears the parent environment, so without this bridge native Claude/Codex
// credentials detected by InjectionDecision would disappear before the CLI
// starts. The manifest is resolved by agent name + adapter (agents.Lookup):
// a generic.py custom CLI has no manifest and gets no forwarded keys.
func adapterCredentialEnv(name, adapter string) []string {
	k, ok := agents.Lookup(name, adapter)
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
// sits, giving a well-behaved adapter room to wind down and report. It is a
// var for the same reason the two budgets are: tests shrink it alongside
// adapterTimeoutS — the enforced limit is max(adapterHardTimeout,
// timeout_s+grace), so a grace left at its real 30s would dominate every
// shrunken test budget and keep the "hard" deadline far away.
var (
	hardTimeoutGrace   = 30 * time.Second
	adapterTimeoutS    = defaultAdapterTimeoutS
	adapterHardTimeout = defaultAdapterTimeoutS*time.Second + hardTimeoutGrace
	silenceTimeout     time.Duration
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

// SetSilenceTimeout retunes the progress silence limit. 0 means disabled.
func SetSilenceTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	silenceTimeout = d
}

// AgentHardTimeout reports the enforced wall-clock limit for one agent
// execution. A task's lease must exceed it, or the lease monitor force-fails
// work that is still legitimately running — see core.Core.SetTimeouts.
func AgentHardTimeout() time.Duration { return adapterHardTimeout }

// buildAdapterRequest maps the execution context onto the wire request:
// the prompt/timeout/cwd basics plus every opt-in field an adapter may
// understand (resume, tools policy, restricted, turn cap, cmd template,
// system prompt, result schema, MCP config, effort). The Session flag is
// decided by the caller — it depends on the pool existing and the adapter
// declaring the capability, which are per-run facts, not request fields.
// A non-nil refusal result means the request must not be sent at all
// (restricted work at an adapter that cannot express a read-only face).
func buildAdapterRequest(ctx context.Context, name, prompt, cwd string) (AdapterRequest, int, *AgentResult) {
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
	if Restricted(ctx) {
		// Fail closed at the process boundary too, not just in execAgent's
		// candidate filter: a restricted request sent to an adapter that
		// cannot express a read-only face would silently run full-power.
		if !AdapterSupportsRestricted(name) {
			return req, timeout, &AgentResult{
				OK:       false,
				ExitCode: 1,
				Result:   "adapter " + name + " has no restricted mode; refusing unconsented remote task",
			}
		}
		req.Restricted = true
	}
	if mt, ok := ctx.Value(maxTurnsKey{}).(int); ok && mt > 0 {
		req.MaxTurns = mt
	}
	if cmd, ok := ctx.Value(agentCmdKey{}).(string); ok {
		req.Cmd = cmd
	}
	if sp, ok := ctx.Value(systemPromptKey{}).(string); ok {
		req.SystemPrompt = sp
	}
	if rs, ok := ctx.Value(resultSchemaKey{}).(string); ok {
		req.ResultSchema = rs
	}
	if mc, ok := ctx.Value(mcpConfigKey{}).(string); ok {
		req.MCPConfig = mc
	}
	if ef, ok := ctx.Value(effortKey{}).(string); ok {
		req.Effort = ef
	}
	if tid := TaskID(ctx); tid != "" {
		req.TaskID = tid
	}
	return req, timeout, nil
}

// adapterSupportsSession reports whether the resolved agent's adapter can
// hold one CLI process open across turns (registry SupportsSession). The
// name comes from Lookup so a generic.py entry never borrows another
// agent's declaration.
func adapterSupportsSession(agent, adapter string) bool {
	k, ok := agents.Lookup(agent, adapter)
	return ok && k.Capabilities.SupportsSession
}

// runAdapterProcess spawns adapters/<name> with a JSON request on stdin and
// reads a JSON result from stdout. env carries the model-provider override
// when the injection policy decided one is needed (empty otherwise — the
// agent then uses its own model); the subprocess is sandboxed to the task
// directory with a minimal environment (see security.Sandbox). stderr is
// split through progressWriter: NDJSON progress lines go to the context's
// sink, the rest is retained for failure diagnosis.
func runAdapterProcess(ctx context.Context, name string, prompt string, cwd string, env []string) AgentResult {
	// adapter: "generic" (no .py) is the built-in template executor: same
	// contract as generic.py — argv expansion, exit-code mapping, sandbox —
	// implemented natively so a node without a Python interpreter can still
	// serve as an execution endpoint. Script adapters continue below.
	if name == agents.GenericNativeAdapter {
		return runGenericNative(ctx, AgentName(ctx), prompt, cwd, env)
	}
	silenceLimit := silenceTimeout

	req, timeout, refusal := buildAdapterRequest(ctx, name, prompt, cwd)
	if refusal != nil {
		return *refusal
	}
	// {env:NAME} placeholders in a generic template resolve inside the
	// adapter's own environment — which the sandbox filters down to the
	// allow-list plus whatever the caller injected, so a custom name like
	// MYTOOL_API_KEY would never resolve. Forward the names the template
	// actually references (operator-declared, never task-controlled) so the
	// script expands against the same daemon env the native generic path
	// reads directly.
	if req.Cmd != "" {
		forward := append([]string(nil), env...)
		for _, n := range templateEnvRefs(req.Cmd) {
			if v, ok := os.LookupEnv(n); ok {
				forward = append(forward, n+"="+v)
			}
		}
		env = forward
	}
	// Session mode: when the orchestration layer asked for it
	// (WithSessionMode), the task carries a session pool and this agent's
	// registry entry declares the capability, the adapter process stays
	// alive across supervision rounds — a continue turn is one stdin line
	// instead of a spawn + session reload. Any session failure falls back
	// to the plain spawn below (the round still has --resume semantics via
	// req.Resume), so a too-old CLI degrades rather than fails.
	if SessionMode(ctx) && adapterSupportsSession(AgentName(ctx), name) {
		if pool := sessionPoolOf(ctx); pool != nil {
			req.Session = true
			key := AgentName(ctx) + "|" + name
			if res, ok := pool.turn(ctx, key, name, req, cwd, env); ok {
				return res
			}
			req.Session = false
		}
	}
	if tid := TaskID(ctx); tid != "" {
		// Copy before appending: the caller's slice may share a backing
		// array with headroom, and this frame's addition must not leak into
		// the caller's next reuse of it.
		env = append(append([]string(nil), env...), "PANDA_TASK_ID="+tid)
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return AgentResult{OK: false, Result: "bad adapter request", ExitCode: 1}
	}

	path, err := adapterPath(name)
	if err != nil {
		return AgentResult{OK: false, Result: security.Redact(err.Error()), ExitCode: 1}
	}
	// The enforced wall-clock limit is the advertised budget plus the wind-down
	// grace. A per-task override larger than the global default must push the
	// hard limit out with it — otherwise a training stage given two hours via
	// WithAgentTimeout would still be killed at the default deadline.
	hardLimit := adapterHardTimeout
	if perTask := time.Duration(timeout)*time.Second + hardTimeoutGrace; perTask > hardLimit {
		hardLimit = perTask
	}
	ctx, cancel := context.WithTimeout(ctx, hardLimit)
	defer cancel()

	var (
		actMu        sync.Mutex
		lastActivity = time.Now()
		stalled      bool
	)
	touch := func() {
		actMu.Lock()
		lastActivity = time.Now()
		actMu.Unlock()
	}

	if silenceLimit > 0 {
		stopWatchdog := make(chan struct{})
		defer close(stopWatchdog)
		go func() {
			tick := silenceLimit / 4
			if tick < 50*time.Millisecond {
				tick = 50 * time.Millisecond
			}
			t := time.NewTicker(tick)
			defer t.Stop()
			for {
				select {
				case <-stopWatchdog:
					return
				case <-ctx.Done():
					return
				case <-t.C:
					actMu.Lock()
					silent := time.Since(lastActivity) >= silenceLimit
					actMu.Unlock()
					if silent {
						actMu.Lock()
						stalled = true
						actMu.Unlock()
						cancel()
						return
					}
				}
			}
		}()
	}

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
	var stdout activityWriter
	stdout.onActivity = touch
	var stderr progressWriter
	stderr.onActivity = touch
	if sink, ok := ctx.Value(progressKey{}).(ProgressFunc); ok {
		stderr.sink = sink
	}
	stderr.evSink = AgentEvents(ctx)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Custom writers spawn copy goroutines Wait blocks on; a detached
	// grandchild inheriting the pipes would otherwise hold them open and
	// wedge Wait forever after the adapter itself exits (or is killed).
	// WaitDelay bounds that wait: the goroutines are abandoned, and since
	// executil kills the whole process group the pipes close anyway.
	cmd.WaitDelay = 5 * time.Second
	// ApplyPolicy installs the filtered env/cwd and — when the configured
	// sandbox mode is on — wraps the adapter in the OS confinement profile:
	// workdir+toolchain writable, this agent's credential dirs writable,
	// foreign credential dirs and OS secrets read-denied under strict.
	security.NewSandbox(cwd).ApplyPolicy(cmd,
		adapterSandboxPolicy(AgentName(ctx), name, cwd), env...)

	if err := cmd.Run(); err != nil {
		actMu.Lock()
		wasStalled := stalled
		actMu.Unlock()
		if wasStalled {
			return AgentResult{OK: false, Result: fmt.Sprintf("adapter stalled: no progress or output received for %v (silence timeout)", silenceLimit), ExitCode: 124}
		}
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

// ---------------------------------------------------------------------------
// Session mode (Phase 6): one adapter process serves every supervision turn.
//
// One-shot execution spends a process spawn AND a session reload per round:
// the follow-up prompt arrives at a cold CLI that must rehydrate the whole
// conversation (--resume rereads the transcript from disk). A session-mode
// adapter instead keeps the CLI alive and serves one turn per stdin line —
// a continue verdict costs a write. The pool lives on the task's execution
// context; runAdapterProcess prefers a live session and silently degrades to
// the one-shot path whenever the session cannot start or dies mid-turn, so a
// CLI too old for --input-format stream-json never fails a task it could
// have run.
// ---------------------------------------------------------------------------

// sessionPoolKey carries the per-task AgentSessions pool on the execution
// context without widening the runAdapter seam's signature.
type sessionPoolKey struct{}

// AgentSessions holds the session-mode adapter processes one task opened —
// keyed by adapter so a plan whose fallback chain crosses adapters keeps one
// session per CLI. The pool's context is the task's execution context: a
// task abort cancels every live session without waiting on turn boundaries.
type AgentSessions struct {
	mu   sync.Mutex
	ctx  context.Context
	live map[string]*agentSession
	shut bool
}

// WithSessionPool attaches a session pool to the execution context and
// returns both; the caller defers pool.CloseAll() so no adapter process
// outlives the task.
func WithSessionPool(ctx context.Context) (context.Context, *AgentSessions) {
	p := &AgentSessions{ctx: ctx, live: map[string]*agentSession{}}
	return context.WithValue(ctx, sessionPoolKey{}, p), p
}

// sessionPoolOf reads the pool WithSessionPool attached — nil when the task
// runs without one (unsupervised, non-agent plan, …).
func sessionPoolOf(ctx context.Context) *AgentSessions {
	p, _ := ctx.Value(sessionPoolKey{}).(*AgentSessions)
	return p
}

// CloseAll kills every live session. Idempotent; called when the task's
// supervision loop finishes — whatever the verdict.
func (p *AgentSessions) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shut = true
	for _, s := range p.live {
		s.kill()
	}
	p.live = map[string]*agentSession{}
}

// turn runs one prompt on the keyed session: spawning it when none exists,
// writing the follow-up message when one does, and reading the turn's
// result envelope either way. key is the agent identity (name+adapter) — a
// generic.py card row must never share a process with another agent that
// happens to reuse the script. The second return value is false when the
// session path could not produce a result (spawn failure, dead process,
// undecodable envelope) — the caller then falls back to a one-shot spawn,
// which is where the old-CLI degradation lives.
func (p *AgentSessions) turn(ctx context.Context, key, adapter string, req AdapterRequest, cwd string, env []string) (AgentResult, bool) {
	p.mu.Lock()
	if p.shut {
		p.mu.Unlock()
		return AgentResult{}, false
	}
	s := p.live[key]
	p.mu.Unlock()

	fresh := false
	if s == nil {
		ns, err := startAgentSession(p.ctx, ctx, adapter, req, cwd, env)
		if err != nil {
			return AgentResult{}, false
		}
		p.mu.Lock()
		if p.shut {
			p.mu.Unlock()
			ns.kill()
			return AgentResult{}, false
		}
		if prev := p.live[key]; prev != nil {
			// A concurrent turn won the spawn race; a loser cannot just drop
			// its process — kill it and serve the prompt on the winner's.
			p.mu.Unlock()
			ns.kill()
			s = prev
		} else {
			p.live[key] = ns
			p.mu.Unlock()
			s = ns
			fresh = true
		}
	}
	// Serialize turns per session: the stdout decoder is not concurrent-safe,
	// and a second send must not interleave between this turn's envelope.
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if !fresh {
		if s.isDead() {
			// A dead session's agent conversation still exists on the CLI's
			// side: respawning with --resume (req.Resume already carries the
			// id the last turn returned) continues it one-shot.
			return AgentResult{}, false
		}
		if err := s.send(req.Prompt); err != nil {
			s.kill()
			p.mu.Lock()
			delete(p.live, key)
			p.mu.Unlock()
			return AgentResult{}, false
		}
	}
	// Turn 0's prompt rode inside the request line — nothing to send for a
	// fresh session.
	// The per-turn hard limit mirrors the one-shot path: the advertised
	// budget plus wind-down grace, and never under the global default. The
	// adapter's own per-turn watchdog normally fires first; this bound
	// exists for the wedge case where it cannot.
	hard := adapterHardTimeout
	if pt := time.Duration(req.TimeoutS)*time.Second + hardTimeoutGrace; pt > hard {
		hard = pt
	}
	turnCtx, cancel := context.WithTimeout(ctx, hard)
	defer cancel()
	res := s.await(turnCtx)
	if !res.OK && (s.isDead() || res.SessionDead) {
		// The envelope arrived but the process is gone and the turn failed:
		// one more shot via the plain spawn — which also covers the
		// "session flag rejected" case an adapter may report this way.
		// isDead() alone races: await() delivers the envelope before
		// cmd.Wait() settles the exit marker when the adapter kills the
		// CLI on its way out, so the adapter also reports the verdict
		// in-band via session_dead.
		s.kill()
		p.mu.Lock()
		delete(p.live, key)
		p.mu.Unlock()
		return AgentResult{}, false
	}
	return res, true
}

// agentSession is one live session-mode adapter process: the Python side
// reads turn messages from stdin and emits one result envelope per turn on
// stdout; stderr keeps the progress/event demux of a one-shot run.
type agentSession struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	dec     *json.Decoder
	stderr  *progressWriter
	cancel  context.CancelFunc
	dead    atomic.Bool
	lastAct atomic.Int64 // unixnano; the silence watchdog's clock
	// turnMu serializes send+await: a session serves one turn at a time and
	// the stdout decoder must never be driven by two readers at once.
	turnMu sync.Mutex
}

// startAgentSession spawns the adapter with stdin held open, writes the
// request line (req.Session is already true), and returns the process ready
// for its first turn's envelope. spawnCtx bounds the session's lifetime
// (the task's execution context — a task abort kills the process); turnCtx
// is this round's context, used only for the progress/event sinks.
func startAgentSession(spawnCtx, turnCtx context.Context, name string, req AdapterRequest, cwd string, env []string) (*agentSession, error) {
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	path, err := adapterPath(name)
	if err != nil {
		return nil, err
	}
	if tid := TaskID(turnCtx); tid != "" {
		env = append(append([]string(nil), env...), "PANDA_TASK_ID="+tid)
	}
	sessCtx, cancel := context.WithCancel(spawnCtx)
	cmd, ok := pyexec.Command(sessCtx, path)
	if !ok {
		cancel()
		return nil, fmt.Errorf("no Python 3 interpreter for adapter %s", name)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &progressWriter{}
	if sink, ok := turnCtx.Value(progressKey{}).(ProgressFunc); ok {
		stderr.sink = sink
	}
	stderr.evSink = AgentEvents(turnCtx)
	s := &agentSession{
		cmd: cmd, stdin: stdin, stderr: stderr, cancel: cancel,
		dec: json.NewDecoder(stdout),
	}
	s.lastAct.Store(time.Now().UnixNano())
	// onActivity must be bound before Start: the stderr copier goroutine
	// starts with the process, so an assignment afterwards would race its
	// Write reads.
	stderr.onActivity = func() { s.lastAct.Store(time.Now().UnixNano()) }
	cmd.Stderr = stderr
	// WaitDelay matches the one-shot path: a detached grandchild inheriting
	// the pipes must not wedge Wait (and with it the dead marker) forever.
	cmd.WaitDelay = 5 * time.Second
	security.NewSandbox(cwd).ApplyPolicy(cmd,
		adapterSandboxPolicy(AgentName(turnCtx), name, cwd), env...)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	if _, err := stdin.Write(append(reqJSON, '\n')); err != nil {
		// Cancel triggers the group kill via the ctx watcher, but only Wait
		// reaps the process — without it the adapter stays a zombie.
		cancel()
		_ = cmd.Wait()
		return nil, err
	}
	// Process exit → dead: the adapter exits when the CLI dies or stdin
	// closes, and a dead session never serves another turn.
	go func() {
		_ = cmd.Wait()
		s.dead.Store(true)
	}()
	// Silence watchdog: a session produces event/progress traffic while it
	// works; a wedge (CLI alive, nothing flowing) is indistinguishable from
	// a long quiet think without it.
	if silenceTimeout > 0 {
		go s.watchSilence(sessCtx)
	}
	return s, nil
}

// watchSilence kills the session when no stderr activity is observed for the
// configured silence limit.
func (s *agentSession) watchSilence(ctx context.Context) {
	tick := silenceTimeout / 4
	if tick < 50*time.Millisecond {
		tick = 50 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			last := time.Unix(0, s.lastAct.Load())
			if silent := time.Since(last) >= silenceTimeout; silent || s.isDead() {
				if silent {
					s.kill()
				}
				return
			}
		}
	}
}

// send writes one follow-up turn message to the adapter's stdin.
func (s *agentSession) send(text string) error {
	msg, err := json.Marshal(map[string]string{"type": "user", "text": text})
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(msg, '\n'))
	return err
}

// await reads the next result envelope off the session's stdout: one JSON
// object per turn, decoded straight into the wire shape. A decode failure or
// EOF marks the session dead and reports a non-OK result so the caller can
// decide between trusting it and falling back to a one-shot spawn.
func (s *agentSession) await(ctx context.Context) AgentResult {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			s.kill()
		case <-done:
		}
	}()
	var res AgentResult
	if err := s.dec.Decode(&res); err != nil {
		s.dead.Store(true)
		return AgentResult{
			OK: false, ExitCode: 1,
			Result: security.Redact("adapter session ended: " + s.stderr.diag()),
		}
	}
	res.Result = security.Redact(res.Result)
	// diag, not String: the copier may be mid-line — a flush here would
	// steal the partial buffer and split the line across an event boundary.
	res.Stderr = security.Redact(s.stderr.diag())
	return res
}

// isDead reports whether the adapter process already exited.
func (s *agentSession) isDead() bool { return s.dead.Load() }

// kill marks the session dead and cancels its process context, which kills
// the adapter process tree (executil process-group semantics).
func (s *agentSession) kill() {
	s.dead.Store(true)
	s.cancel()
}
