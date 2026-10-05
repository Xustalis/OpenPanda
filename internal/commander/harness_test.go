package commander

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// tier1Card is a test card whose agent is declared tier 1, so Execute runs
// without authorization and the tests can focus on the harness behavior.
func tier1Card() ledger.Card {
	card := testCard()
	ag := card.Agents["claude_code"]
	ag.Tier = 1
	card.Agents["claude_code"] = ag
	return card
}

func tier1Plan(t *testing.T, r *Router) Plan {
	t.Helper()
	plan, err := r.Route([]string{"code:modify"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	return plan
}

// TestProgressWriterSplitsStream verifies the stderr splitter: NDJSON
// progress lines reach the sink and are excluded from the diagnostic
// buffer; plain lines stay in the buffer.
func TestProgressWriterSplitsStream(t *testing.T) {
	var mu sync.Mutex
	var notes []string
	w := progressWriter{sink: func(note, kind string) {
		mu.Lock()
		defer mu.Unlock()
		notes = append(notes, note)
	}}
	in := `{"type":"progress","note":"Bash: du -ah | sort -rh"}
some plain diagnostic line
{"type":"progress","note":"Read: main.go"}
not json at all
{"type":"other","note":"ignored shape"}
`
	if _, err := w.Write([]byte(in)); err != nil {
		t.Fatalf("write: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 2 || notes[0] != "Bash: du -ah | sort -rh" || notes[1] != "Read: main.go" {
		t.Fatalf("notes = %v", notes)
	}
	diag := w.String()
	if !strings.Contains(diag, "some plain diagnostic line") || !strings.Contains(diag, "not json at all") {
		t.Fatalf("diagnostics lost: %q", diag)
	}
	if strings.Contains(diag, "Bash: du") || strings.Contains(diag, "progress") {
		t.Fatalf("progress leaked into diagnostics: %q", diag)
	}
}

// TestProgressWriterBuffersSplitLines verifies a stderr line split across
// pipe reads is still assembled correctly: progress notes reach the sink and
// noise fragments stay in diagnostics without leaking either way.
func TestProgressWriterBuffersSplitLines(t *testing.T) {
	var notes []string
	w := progressWriter{sink: func(n, _ string) { notes = append(notes, n) }}

	// A progress line split across three writes.
	w.Write([]byte(`{"type":"progr`))
	w.Write([]byte(`ess","note":"Bash: `))
	w.Write([]byte(`ls -la"}` + "\n"))

	// A diagnostic line split across two writes.
	w.Write([]byte("some plain "))
	w.Write([]byte("diagnostic line\n"))

	if len(notes) != 1 || notes[0] != "Bash: ls -la" {
		t.Fatalf("notes = %v", notes)
	}
	diag := w.String()
	if !strings.Contains(diag, "some plain diagnostic line") {
		t.Fatalf("diagnostics lost: %q", diag)
	}
	if strings.Contains(diag, "progress") || strings.Contains(diag, "Bash") {
		t.Fatalf("progress leaked into diagnostics: %q", diag)
	}
}

// TestProgressWriterLongNoteTruncates verifies a pathological note is clipped to
// 300 runes so one event cannot bloat the chain.
func TestProgressLongNoteTruncates(t *testing.T) {
	var got string
	w := progressWriter{sink: func(n, _ string) { got = n }}
	long := strings.Repeat("x", 500)
	w.Write([]byte(fmt.Sprintf(`{"type":"progress","note":%q}`+"\n", long)))
	if n := len([]rune(got)); n > 302 {
		t.Fatalf("note len = %d, want <= 302", n)
	}
}

// TestExecuteAgentForwardsProgress drives the REAL process path (not the
// runAdapter seam): a python stub adapter emits one progress NDJSON line
// and one noise line on stderr, plus the JSON result on stdout. The test
// asserts the progress reaches the context sink and the result resolves —
// exactly what claude_code.py / codex.py now do live.
func TestExecuteAgentForwardsProgress(t *testing.T) {
	dir := t.TempDir()
	old := adapterDir
	adapterDir = dir
	defer func() { adapterDir = old }()

	stub := `import json, sys
sys.stderr.write(json.dumps({"type":"progress","note":"Bash: ls -la"}) + "\n")
sys.stderr.write("noise line\n")
print(json.dumps({"ok": True, "result": "done", "exit_code": 0}))
`
	if err := os.WriteFile(filepath.Join(dir, "claude_code.py"), []byte(stub), 0o755); err != nil {
		t.Fatalf("stub: %v", err)
	}

	r := NewRouter(tier1Card(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	plan := tier1Plan(t, r)

	var mu sync.Mutex
	var notes []string
	ctx := WithProgress(context.Background(), func(note, kind string) {
		mu.Lock()
		defer mu.Unlock()
		notes = append(notes, note)
	})
	res := r.Execute(ctx, plan, "do it", t.TempDir(), true)
	if !res.OK || res.Stdout != "done" {
		t.Fatalf("exec = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 || notes[0] != "Bash: ls -la" {
		t.Fatalf("notes = %v", notes)
	}
}

// TestTransientRetryOnce verifies a transient provider failure (rate limit)
// is retried exactly once and the retry's success wins; a hard failure is
// never retried.
func TestTransientRetryOnce(t *testing.T) {
	r := NewRouter(tier1Card(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	plan := tier1Plan(t, r)

	calls := 0
	r.runAdapter = func(ctx context.Context, adapter, prompt, cwd string) AgentResult {
		calls++
		if calls == 1 {
			return AgentResult{OK: false, Result: "API error: rate limit exceeded (429)", ExitCode: 1}
		}
		return AgentResult{OK: true, Result: "succeeded on retry", ExitCode: 0}
	}
	res := r.Execute(context.Background(), plan, "p", "", true)
	if !res.OK || calls != 2 {
		t.Fatalf("retry: ok=%v calls=%d", res.OK, calls)
	}
	if !strings.Contains(res.Stdout, "[retried once after transient provider error]") {
		t.Fatalf("retry marker missing: %q", res.Stdout)
	}

	// Hard failure: no retry.
	calls = 0
	r.runAdapter = func(ctx context.Context, adapter, prompt, cwd string) AgentResult {
		calls++
		return AgentResult{OK: false, Result: "command not found: duu", ExitCode: 127}
	}
	res = r.Execute(context.Background(), plan, "p", "", true)
	if res.OK || calls != 1 {
		t.Fatalf("hard fail: ok=%v calls=%d", res.OK, calls)
	}
}

// TestTransientRetryStillFails verifies that when the retry also fails the
// failure surfaces (no fake success).
func TestTransientRetryStillFails(t *testing.T) {
	r := NewRouter(tier1Card(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	plan := tier1Plan(t, r)
	calls := 0
	r.runAdapter = func(ctx context.Context, adapter, prompt, cwd string) AgentResult {
		calls++
		return AgentResult{OK: false, Result: "API error: overloaded", ExitCode: 1}
	}
	res := r.Execute(context.Background(), plan, "p", "", true)
	if res.OK || calls != 2 || !strings.Contains(res.Stderr, "overloaded") {
		t.Fatalf("double fail: ok=%v calls=%d stderr=%q", res.OK, calls, res.Stderr)
	}
}

// TestTransientPatterns pins the classification: provider turbulence
// qualifies, real task failures do not.
func TestTransientPatterns(t *testing.T) {
	yes := []string{
		"API error: rate limit exceeded",
		"HTTP 502 Bad Gateway",
		"connection reset by peer",
		"service unavailable",
	}
	no := []string{
		"command not found: duu",
		"permission denied",
		"exit status 1",
		"processed 5000 items",
		"listen on port 15020",
		"",
	}
	for _, s := range yes {
		if !transientAgentFailure(AgentResult{OK: false, Result: s}) {
			t.Errorf("should be transient: %q", s)
		}
	}
	for _, s := range no {
		if transientAgentFailure(AgentResult{OK: false, Result: s}) {
			t.Errorf("should NOT be transient: %q", s)
		}
	}
	if transientAgentFailure(AgentResult{OK: true, Result: "rate limit"}) {
		t.Errorf("success is never transient")
	}
}

// TestAgentSessionPoolReusesProcess drives the REAL session path: a stub
// claude_code.py keeps stdin open after the request line and answers one
// result envelope per turn line. Two Execute calls on the same execution
// context must ride ONE adapter process — the second turn is a stdin line,
// not a spawn — and CloseAll must reap the process.
func TestAgentSessionPoolReusesProcess(t *testing.T) {
	dir := t.TempDir()
	old := adapterDir
	adapterDir = dir
	defer func() { adapterDir = old }()

	stub := `import json, os, sys
marks = os.path.join(os.getcwd(), "marks.log")
def mark(m):
    with open(marks, "a") as f:
        f.write(m + "\n")
mark("spawn:" + str(os.getpid()))
req = json.loads(sys.stdin.readline())
assert req.get("session") is True, req
print(json.dumps({"ok": True, "result": "turn0:" + req["prompt"], "exit_code": 0, "session_id": "sess-T"}))
sys.stdout.flush()
for line in sys.stdin:
    msg = json.loads(line)
    mark("turn:" + msg["text"])
    print(json.dumps({"ok": True, "result": "turnN:" + msg["text"], "exit_code": 0, "session_id": "sess-T"}))
    sys.stdout.flush()
`
	if err := os.WriteFile(filepath.Join(dir, "claude_code.py"), []byte(stub), 0o755); err != nil {
		t.Fatalf("stub: %v", err)
	}

	r := NewRouter(tier1Card(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	plan := tier1Plan(t, r)

	workDir := t.TempDir()
	ctx, pool := WithSessionPool(context.Background())
	ctx = WithSessionMode(ctx)
	res1 := r.Execute(ctx, plan, "first", workDir, true)
	if !res1.OK || res1.Stdout != "turn0:first" {
		t.Fatalf("turn0 = %+v", res1)
	}
	res2 := r.Execute(ctx, plan, "second", workDir, true)
	if !res2.OK || res2.Stdout != "turnN:second" {
		t.Fatalf("turnN = %+v", res2)
	}
	pool.CloseAll()

	marks, err := os.ReadFile(filepath.Join(workDir, "marks.log"))
	if err != nil {
		t.Fatalf("marks: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(marks)), "\n")
	// Exactly one spawn, and the second prompt arrived as a stdin turn line.
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "spawn:") || lines[1] != "turn:second" {
		t.Fatalf("marks = %v — expected one process serving both turns", lines)
	}
	// After CloseAll the session process is gone.
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.live) != 0 {
		t.Fatalf("pool still holds %d sessions after CloseAll", len(pool.live))
	}
}

// TestAgentSessionFallsBackOnSpawnFailure verifies a session-incapable or
// wedge adapter degrades to the one-shot path instead of failing the task.
func TestAgentSessionFallsBackOnSpawnFailure(t *testing.T) {
	dir := t.TempDir()
	old := adapterDir
	adapterDir = dir
	defer func() { adapterDir = old }()

	// Session-unaware stub: answers the request line once and exits — an
	// adapter that ignores "session" and serves the one-shot contract. (It
	// must readline, not json.load: stdin stays open in session mode, so a
	// full-document read would block on EOF forever.) The follow-up Execute
	// then finds the session dead and falls back to a plain spawn, which
	// the same stub serves again.
	stub := `import json, sys
req = json.loads(sys.stdin.readline())
print(json.dumps({"ok": True, "result": "oneshot:" + req["prompt"], "exit_code": 0}))
`
	if err := os.WriteFile(filepath.Join(dir, "claude_code.py"), []byte(stub), 0o755); err != nil {
		t.Fatalf("stub: %v", err)
	}

	r := NewRouter(tier1Card(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	plan := tier1Plan(t, r)

	ctx, pool := WithSessionPool(context.Background())
	defer pool.CloseAll()
	ctx = WithSessionMode(ctx)
	res := r.Execute(ctx, plan, "first", t.TempDir(), true)
	if !res.OK || res.Stdout != "oneshot:first" {
		t.Fatalf("oneshot via session = %+v", res)
	}
	res = r.Execute(ctx, plan, "second", t.TempDir(), true)
	if !res.OK || res.Stdout != "oneshot:second" {
		t.Fatalf("fallback oneshot = %+v", res)
	}
}

// TestRetryContextCancel verifies the retry wait honors cancellation — a
// cancelled task must not sit out the 3s backoff.
func TestRetryContextCancel(t *testing.T) {
	r := NewRouter(tier1Card(), NewExecutor(), config.ModelConfig{}, config.InjectionConfig{}, config.RoutingConfig{})
	r.SetAgentProber(func(string, ledger.Agent) bool { return true })
	plan := tier1Plan(t, r)
	calls := 0
	r.runAdapter = func(ctx context.Context, adapter, prompt, cwd string) AgentResult {
		calls++
		return AgentResult{OK: false, Result: "429 rate limit", ExitCode: 1}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_ = r.Execute(ctx, plan, "p", "", true)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation ignored, took %v", elapsed)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cancelled before retry)", calls)
	}
}
