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

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// memoryFilesKey is the context key carrying the selective-loading memory
// file list (A3) from the orchestration layer down to the adapter request,
// without widening every execution-path signature in between.
type memoryFilesKey struct{}

// WithMemoryFiles attaches the node's memory file paths (absolute) to an
// execution context; runAdapterProcess copies them into AdapterRequest so an
// agent that received the prompt manifest can read the listed files itself.
func WithMemoryFiles(ctx context.Context, paths []string) context.Context {
	if len(paths) == 0 {
		return ctx
	}
	return context.WithValue(ctx, memoryFilesKey{}, paths)
}

// progressKey is the context key carrying the live progress sink from the
// orchestration layer (core's execute → RecordEvent) down to the adapter
// process reader, without widening every execution-path signature.
type progressKey struct{}

// ProgressFunc receives one adapter progress note (a short human-readable
// line, e.g. "Bash: du -ah | sort -rh") as the agent works.
type ProgressFunc func(note string)

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
	}
	if err := json.Unmarshal(b, &probe); err == nil && probe.Type == "progress" && probe.Note != "" {
		note := strings.TrimSpace(probe.Note)
		if len([]rune(note)) > 300 {
			note = string([]rune(note)[:300]) + "…"
		}
		if w.sink != nil {
			w.sink(note)
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

// resolveAdapterDir absolutizes a relative adapterDir when an adapters/ dir
// exists beside the cwd or the executable; otherwise the relative name stands
// (the spawn error then names the missing path naturally).
func resolveAdapterDir() string {
	if filepath.IsAbs(adapterDir) {
		return adapterDir
	}
	if abs, err := filepath.Abs(adapterDir); err == nil {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return abs
		}
	}
	if exe, err := os.Executable(); err == nil {
		for _, cand := range []string{
			filepath.Join(filepath.Dir(exe), "..", "adapters"),
			filepath.Join(filepath.Dir(exe), "adapters"),
		} {
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				return cand
			}
		}
	}
	return adapterDir
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
	// MemoryFiles lists the absolute paths of the node's personal memory
	// files (A3 selective loading). The prompt carries the manifest (file
	// index + summaries); this list gives the orchestration layer's view of
	// the same files so adapters that support file access can let the agent
	// read only what it needs instead of the whole memory content.
	MemoryFiles []string `json:"memory_files,omitempty"`
}

// modelEnv builds the model provider env injected into the adapter process
// when the injection policy says so (see Router.InjectionDecision). Secrets
// are passed only via env and never echoed to logs. Empty base_url/model
// fall back to the same defaults the entry model applies, so the adapter and
// entry never diverge.
func modelEnv(model config.ModelConfig) []string {
	base := model.BaseURL
	if base == "" {
		base = "https://api.deepseek.com/anthropic"
	}
	name := model.Model
	if name == "" {
		name = "deepseek-chat"
	}
	env := []string{
		"ANTHROPIC_BASE_URL=" + base,
		"ANTHROPIC_API_KEY=" + model.APIKey,
		"ANTHROPIC_MODEL=" + name,
	}
	return env
}

// adapterTimeoutS is the budget advertised to the adapter in its request.
// adapterHardTimeout is the enforced wall-clock limit (P1-18): TimeoutS used
// to be a polite JSON suggestion an adapter could ignore and run forever, so
// the Go side wraps the spawn in a hard context deadline slightly past the
// advertised budget. Combined with process-group cancellation (executil),
// hitting the deadline kills the adapter and every CLI it spawned.
// adapterHardTimeout is a var so tests can shrink it.
const adapterTimeoutS = 600

var adapterHardTimeout = (adapterTimeoutS + 30) * time.Second

// runAdapterProcess spawns adapters/<name> with a JSON request on stdin and
// reads a JSON result from stdout. env carries the model-provider override
// when the injection policy decided one is needed (empty otherwise — the
// agent then uses its own model); the subprocess is sandboxed to the task
// directory with a minimal environment (see security.Sandbox). stderr is
// split through progressWriter: NDJSON progress lines go to the context's
// sink, the rest is retained for failure diagnosis.
func runAdapterProcess(ctx context.Context, name string, prompt string, cwd string, env []string) AgentResult {
	req := AdapterRequest{Prompt: prompt, TimeoutS: adapterTimeoutS, CWD: cwd}
	if mf, ok := ctx.Value(memoryFilesKey{}).([]string); ok {
		req.MemoryFiles = mf
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
	cmd := executil.CommandContext(ctx, "python3", path)
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
