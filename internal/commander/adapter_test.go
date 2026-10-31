// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// fakeAdapter writes a known JSON result to stdout, so we can test the
// process bridge without invoking a real LLM CLI.
const fakeAdapter = `#!/usr/bin/env python3
import json, sys
req = json.loads(sys.stdin.read())
print(json.dumps({"ok": True, "result": "hello from " + req.get("prompt", ""), "exit_code": 0}))
`

func TestRunAdapterProcess(t *testing.T) {
	dir := t.TempDir()
	adapterPath := filepath.Join(dir, "fake.py")
	if err := os.WriteFile(adapterPath, []byte(fakeAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir // redirect the constant for this test
	defer func() { adapterDir = oldDir }()

	res := runAdapterProcess(context.Background(), "fake.py", "PANDA", "", nil)
	if !res.OK {
		t.Fatalf("adapter failed: %+v", res)
	}
	if res.Result != "hello from PANDA" {
		t.Fatalf("unexpected result: %q", res.Result)
	}
}

// echoRequestAdapter hands the parsed stdin request back, so the test can
// assert exactly which fields crossed the wire.
const echoRequestAdapter = `#!/usr/bin/env python3
import json, sys
req = json.loads(sys.stdin.read())
print(json.dumps({"ok": True, "result": json.dumps(req, sort_keys=True), "exit_code": 0}))
`

// TestRunAdapterProcessCarriesCommand: a card-declared argv template
// (ledger.Agent.Command via WithAgentCommand) rides the request's "cmd"
// field to generic.py; absent the context value the key stays off the wire.
func TestRunAdapterProcessCarriesCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "echo.py"), []byte(echoRequestAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()

	ctx := WithAgentCommand(context.Background(), "zcode run --headless {prompt}")
	res := runAdapterProcess(ctx, "echo.py", "p", "", nil)
	if !res.OK {
		t.Fatalf("adapter failed: %+v", res)
	}
	if !strings.Contains(res.Result, `"cmd": "zcode run --headless {prompt}"`) {
		t.Fatalf("cmd template not on the wire: %s", res.Result)
	}

	res = runAdapterProcess(context.Background(), "echo.py", "p", "", nil)
	if !res.OK {
		t.Fatalf("adapter failed: %+v", res)
	}
	if strings.Contains(res.Result, `"cmd"`) {
		t.Fatalf("cmd must stay off the request when unset: %s", res.Result)
	}
}

func TestRunAdapterMissingBinary(t *testing.T) {
	oldDir := adapterDir
	adapterDir = t.TempDir() // no adapters here
	defer func() { adapterDir = oldDir }()

	res := runAdapterProcess(context.Background(), "nope.py", "x", "", nil)
	if res.OK {
		t.Fatalf("expected failure for missing adapter")
	}
}

// failingAdapter reports a task failure over the wire: ok=false plus the
// exit code the CLI died with, a session id and token count.
const failingAdapter = `#!/usr/bin/env python3
import json, sys
req = json.loads(sys.stdin.read())
print(json.dumps({"ok": False, "result": "cli exploded", "exit_code": 42,
                  "tokens": 7, "cost": 0.02, "session_id": "s-1"}))
`

// TestRunAdapterResultDecodesWireContract is the result-envelope regression:
// the adapter reports failure details in snake_case keys ("exit_code",
// "session_id"), and every one must survive the decode — before AgentResult
// carried explicit tags, Go's case-insensitive-but-not-underscore matching
// dropped "exit_code" entirely, so timeouts (124), missing binaries (127)
// and malformed requests (2) all reported as exit 0 upstream.
func TestRunAdapterResultDecodesWireContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boom.py"), []byte(failingAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()

	res := runAdapterProcess(context.Background(), "boom.py", "x", "", nil)
	if res.OK {
		t.Fatal("ok=false adapter must report failure")
	}
	if res.ExitCode != 42 {
		t.Fatalf("exit_code = %d, want 42 decoded from the wire", res.ExitCode)
	}
	if res.Result != "cli exploded" || res.Tokens != 7 || res.SessionID != "s-1" {
		t.Fatalf("wire fields lost in decode: %+v", res)
	}
}

func TestModelEnvInjectsProvider(t *testing.T) {
	env := modelEnv(config.ModelConfig{
		BaseURL: "https://api.deepseek.com/anthropic",
		APIKey:  "sk-test",
		Model:   "deepseek-chat",
	})
	want := map[string]string{
		"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
		"ANTHROPIC_API_KEY":  "sk-test",
		"ANTHROPIC_MODEL":    "deepseek-chat",
	}
	for _, kv := range env {
		for k, v := range want {
			if kv == k+"="+v {
				delete(want, k)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing env entries: %v", want)
	}
}

func TestModelEnvRejectsUnsupportedProviderMapping(t *testing.T) {
	env := modelEnvForAdapter(config.ModelConfig{
		APIType: config.APITypeOpenAI, BaseURL: "https://api.openai.com/v1", APIKey: "sk-test", Model: "gpt-4o",
	}, "claude_code", "claude_code.py")
	if len(env) != 0 {
		t.Fatalf("openai config must not be mapped to Claude env: %v", env)
	}
}

func TestAdapterCredentialEnvIsWhitelisted(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "unrelated")
	env := adapterCredentialEnv("codex", "codex.py")
	if len(env) != 1 || env[0] != "OPENAI_API_KEY=openai-secret" {
		t.Fatalf("codex credential env = %v, want only OpenAI key", env)
	}
}

func TestMergeAdapterEnvInjectedValueWinsWithoutDuplicates(t *testing.T) {
	got := mergeAdapterEnv(
		[]string{"PATH=/bin", "ANTHROPIC_API_KEY=native", "OPENAI_API_KEY=openai"},
		[]string{"ANTHROPIC_API_KEY=panda", "ANTHROPIC_MODEL=model"},
	)
	want := []string{"PATH=/bin", "ANTHROPIC_API_KEY=panda", "OPENAI_API_KEY=openai", "ANTHROPIC_MODEL=model"}
	if len(got) != len(want) {
		t.Fatalf("merged env = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged env[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

// TestAdapterPathRejectsTraversal verifies adapter names are flat filenames
// (P2-5): anything path-like (.., separators, absolute) is a traversal attempt
// and must be rejected before it reaches exec.CommandContext.
func TestAdapterPathRejectsTraversal(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../evil.py", "a/b.py", `a\b.py`, "/etc/passwd"} {
		if _, err := adapterPath(name); err == nil {
			t.Fatalf("adapterPath(%q): expected error", name)
		}
	}
	p, err := adapterPath("claude_code.py")
	if err != nil {
		t.Fatalf("valid adapter name rejected: %v", err)
	}
	if p == "" {
		t.Fatal("empty path for valid adapter name")
	}
}

func TestResolveAdapterDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(adapterDirEnv, dir)
	if got := resolveAdapterDir(); got != dir {
		t.Fatalf("resolveAdapterDir = %q, want env override %q", got, dir)
	}
}

// TestResolveAdapterDirWalksUpFromCwd covers repo-subdir runs: `panda` started
// from webui/ (or any nested checkout dir) must still find the checkout's
// adapters/ by walking up from the process cwd, without the env override.
func TestResolveAdapterDirWalksUpFromCwd(t *testing.T) {
	root := t.TempDir()
	adapters := filepath.Join(root, "adapters")
	if err := os.MkdirAll(adapters, 0o755); err != nil {
		t.Fatalf("mkdir adapters: %v", err)
	}
	nested := filepath.Join(root, "webui", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	oldDir := adapterDir
	adapterDir = "adapters" // the production default
	defer func() { adapterDir = oldDir }()
	t.Chdir(nested)

	if got := resolveAdapterDir(); got != adapters {
		t.Fatalf("resolveAdapterDir from %q = %q, want ancestor %q", nested, got, adapters)
	}
}

// TestResolveAdapterDirFallsBackToCwdAbsolute verifies the no-match fallback
// is a stable cwd-absolute path, not the bare relative name the sandbox would
// re-resolve against the task directory (the old failure mode printed
// <task-workdir>/adapters/<name>.py and hid the real location).
func TestResolveAdapterDirFallsBackToCwdAbsolute(t *testing.T) {
	empty := t.TempDir()
	oldDir := adapterDir
	adapterDir = "adapters"
	defer func() { adapterDir = oldDir }()
	t.Chdir(empty)

	got := resolveAdapterDir()
	if !filepath.IsAbs(got) {
		t.Fatalf("resolveAdapterDir fallback = %q, want absolute", got)
	}
	if want := filepath.Join(empty, "adapters"); got != want {
		t.Fatalf("resolveAdapterDir fallback = %q, want %q", got, want)
	}
}

// slowAdapter reads the request and then sleeps far past any test budget,
// simulating an adapter that ignores the advertised timeout_s.
const slowAdapter = `#!/usr/bin/env python3
import json, sys, time
req = json.loads(sys.stdin.read())
time.sleep(3600)
print(json.dumps({"ok": True, "result": "done", "exit_code": 0}))
`

// TestAdapterHardTimeout verifies P1-18: an adapter that ignores the timeout
// advertised in its request is killed by the Go-side hard deadline instead of
// running forever.
func TestAdapterHardTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slow.py"), []byte(slowAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldTimeout := adapterHardTimeout
	adapterHardTimeout = 300 * time.Millisecond
	defer func() { adapterHardTimeout = oldTimeout }()
	// The enforced limit is max(adapterHardTimeout, advertised+grace), so the
	// advertised budget AND the grace must shrink with it — otherwise the
	// default 600s timeout_s (or the 30s grace under a shrunken timeout_s)
	// wins the max and the "hard" deadline sits tens of seconds out.
	oldAdvertised := adapterTimeoutS
	adapterTimeoutS = 1
	defer func() { adapterTimeoutS = oldAdvertised }()
	oldGrace := hardTimeoutGrace
	hardTimeoutGrace = 100 * time.Millisecond
	defer func() { hardTimeoutGrace = oldGrace }()

	start := time.Now()
	res := runAdapterProcess(context.Background(), "slow.py", "x", "", nil)
	elapsed := time.Since(start)

	if res.OK {
		t.Fatalf("sleeping adapter reported success")
	}
	if res.ExitCode != 124 {
		t.Fatalf("exit code = %d, want 124 (hard timeout)", res.ExitCode)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hard timeout not enforced promptly: %v", elapsed)
	}
}

// TestAdapterSilenceHeartbeatNotes verifies the always-on watchdog posts
// "still running, no output" progress notes while an adapter sits silent —
// even with the silence kill disabled — so a stalled run looks alive-but-
// quiet on the task card instead of frozen.
func TestAdapterSilenceHeartbeatNotes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slow.py"), []byte(slowAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldHB := silenceHeartbeat
	silenceHeartbeat = 100 * time.Millisecond
	defer func() { silenceHeartbeat = oldHB }()
	// End the run via the hard timeout shortly after the first heartbeat —
	// the kill is not what this test measures.
	oldTimeout := adapterHardTimeout
	adapterHardTimeout = 600 * time.Millisecond
	defer func() { adapterHardTimeout = oldTimeout }()
	oldAdvertised := adapterTimeoutS
	adapterTimeoutS = 0
	defer func() { adapterTimeoutS = oldAdvertised }()
	oldGrace := hardTimeoutGrace
	hardTimeoutGrace = 50 * time.Millisecond
	defer func() { hardTimeoutGrace = oldGrace }()

	var mu sync.Mutex
	var notes []string
	ctx := WithProgress(context.Background(), func(note, kind string) {
		mu.Lock()
		notes = append(notes, note)
		mu.Unlock()
	})
	res := runAdapterProcess(ctx, "slow.py", "x", "", nil)
	if res.OK {
		t.Fatalf("sleeping adapter reported success")
	}
	mu.Lock()
	defer mu.Unlock()
	var heartbeats int
	for _, n := range notes {
		if strings.Contains(n, "no output for") {
			heartbeats++
		}
	}
	if heartbeats == 0 {
		t.Fatalf("no silence heartbeat notes in %v", notes)
	}
}

// startThenSilentAdapter emits one raw stderr line then goes quiet forever —
// the mid-run wedge the tight silence limit exists to catch. The line is
// deliberately not an adapter protocol frame: raw stderr is the CLI speaking,
// so the startup grace window is already closed by the time the silence starts.
const startThenSilentAdapter = `#!/usr/bin/env python3
import json, sys, time
req = json.loads(sys.stdin.read())
print("cli started", file=sys.stderr, flush=True)
time.sleep(3600)
print(json.dumps({"ok": True, "result": "done", "exit_code": 0}))
`

// TestAdapterSilenceKill verifies the watchdog aborts an adapter that goes
// silent mid-run past the configured silence limit — a wedged run dies early
// and reports itself stalled instead of burning the full agent budget.
func TestAdapterSilenceKill(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slow.py"), []byte(startThenSilentAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldSilence := silenceTimeout
	silenceTimeout = 200 * time.Millisecond
	defer func() { silenceTimeout = oldSilence }()
	oldHB := silenceHeartbeat
	silenceHeartbeat = 100 * time.Millisecond
	defer func() { silenceHeartbeat = oldHB }()

	start := time.Now()
	res := runAdapterProcess(context.Background(), "slow.py", "x", "", nil)
	if res.OK {
		t.Fatalf("silent adapter reported success")
	}
	if res.ExitCode != 124 || !strings.Contains(res.Result, "silence timeout") {
		t.Fatalf("res = %+v, want stalled exit-124 silence timeout", res)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("silence kill not enforced promptly: %v", time.Since(start))
	}
}

// TestAdapterStartupGraceKill covers the pre-first-output window: a CLI whose
// first model call legitimately emits nothing for a while must not be killed
// at the silence limit — that just respawns into the same wait — but a
// process that never produces anything still dies, bounded by the startup
// grace rather than the full hard timeout.
func TestAdapterStartupGraceKill(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slow.py"), []byte(slowAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldSilence := silenceTimeout
	silenceTimeout = 200 * time.Millisecond
	defer func() { silenceTimeout = oldSilence }()
	oldGrace := adapterStartupGrace
	adapterStartupGrace = 600 * time.Millisecond
	defer func() { adapterStartupGrace = oldGrace }()
	oldHB := silenceHeartbeat
	silenceHeartbeat = 100 * time.Millisecond
	defer func() { silenceHeartbeat = oldHB }()

	start := time.Now()
	res := runAdapterProcess(context.Background(), "slow.py", "x", "", nil)
	elapsed := time.Since(start)
	if res.OK {
		t.Fatalf("silent adapter reported success")
	}
	if res.ExitCode != 124 || !strings.Contains(res.Result, "startup grace") {
		t.Fatalf("res = %+v, want stalled exit-124 startup grace", res)
	}
	// The grace — not the 200ms silence limit — must have been the bound.
	if elapsed < 550*time.Millisecond {
		t.Fatalf("killed at %v, before startup grace 600ms", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("startup grace not enforced promptly: %v", elapsed)
	}
}

// spawnThenSilentAdapter emits only the adapter's own spawn bookkeeping and
// then nothing — no model output ever arrives.
const spawnThenSilentAdapter = `#!/usr/bin/env python3
import json, sys, time
req = json.loads(sys.stdin.read())
print(json.dumps({"type": "event", "ev": "tool_use", "phase": "spawn",
                  "id": "t", "name": "t", "input": {"argc": 1}}),
      file=sys.stderr, flush=True)
time.sleep(3600)
print(json.dumps({"ok": True, "result": "done", "exit_code": 0}))
`

// TestAdapterStartupGraceSurvivesSpawnBookkeeping pins the pre-first-output
// window against the adapter's own startup frame: {"phase":"spawn"} says the
// task was handed to the CLI, not that a model has spoken. It used to count
// as output, so the grace window closed the instant the process started and
// could never apply no matter what silence_s was set to.
func TestAdapterStartupGraceSurvivesSpawnBookkeeping(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slow.py"), []byte(spawnThenSilentAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldSilence := silenceTimeout
	silenceTimeout = 200 * time.Millisecond
	defer func() { silenceTimeout = oldSilence }()
	oldGrace := adapterStartupGrace
	adapterStartupGrace = 600 * time.Millisecond
	defer func() { adapterStartupGrace = oldGrace }()
	oldHB := silenceHeartbeat
	silenceHeartbeat = 100 * time.Millisecond
	defer func() { silenceHeartbeat = oldHB }()

	start := time.Now()
	res := runAdapterProcess(context.Background(), "slow.py", "x", "", nil)
	elapsed := time.Since(start)
	if res.OK {
		t.Fatalf("silent adapter reported success")
	}
	if res.ExitCode != 124 || !strings.Contains(res.Result, "startup grace") {
		t.Fatalf("res = %+v, want exit-124 startup grace; the spawn frame must not close the window", res)
	}
	if elapsed < 550*time.Millisecond {
		t.Fatalf("killed at %v, before startup grace 600ms — the spawn frame counted as output", elapsed)
	}
}

// TestProgressWriterOutputTracking separates liveness from output: spawn
// bookkeeping, progress heartbeats and session handles only mark activity,
// while raw stderr and real agent events mark output.
func TestProgressWriterOutputTracking(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		output bool
	}{
		{"spawn bookkeeping", `{"type":"event","ev":"tool_use","phase":"spawn","id":"t"}`, false},
		{"progress heartbeat", `{"type":"progress","note":"still running"}`, false},
		{"session handle", `{"type":"event","ev":"session","id":"s1"}`, false},
		{"agent text", `{"type":"event","ev":"text","text":"hello"}`, true},
		{"agent tool use", `{"type":"event","ev":"tool_use","id":"t","name":"Bash"}`, true},
		{"raw stderr", "cli started", true},
	}
	for _, tc := range cases {
		var activity, output bool
		w := &progressWriter{
			onActivity: func() { activity = true },
			onOutput:   func() { output = true },
		}
		if _, err := w.Write([]byte(tc.line + "\n")); err != nil {
			t.Fatalf("%s: write: %v", tc.name, err)
		}
		if !activity {
			t.Fatalf("%s: no activity recorded", tc.name)
		}
		if output != tc.output {
			t.Fatalf("%s: output=%v, want %v", tc.name, output, tc.output)
		}
	}
}

// TestSessionWatchdogRespectsTurnState pins the session-mode silence rules the
// one-shot path already proved: the kill only applies while a turn is in
// flight (a pooled session resting between rounds is not stalled), and a
// turn's pre-first-output window gets the startup grace while mid-turn
// silence keeps the tight limit.
func TestSessionWatchdogRespectsTurnState(t *testing.T) {
	oldGrace := adapterStartupGrace
	adapterStartupGrace = 250 * time.Millisecond
	defer func() { adapterStartupGrace = oldGrace }()

	newSess := func() *agentSession {
		s := &agentSession{stderr: &progressWriter{}}
		return s
	}

	// Idle between turns: stale lastAct must never kill — inTurn is false.
	idle := newSess()
	ictx, icancel := context.WithCancel(context.Background())
	idle.cancel = icancel
	idle.lastAct.Store(time.Now().Add(-time.Hour).UnixNano())
	go idle.watchSilence(ictx, 100*time.Millisecond, 80*time.Millisecond, adapterStartupGrace)
	time.Sleep(300 * time.Millisecond)
	if idle.isDead() {
		t.Fatalf("idle session killed — the watchdog must only guard in-flight turns")
	}
	icancel()

	// In-flight turn, nothing emitted yet: the startup grace — not the
	// silence limit — bounds the kill.
	startup := newSess()
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	startup.cancel = scancel
	startup.inTurn.Store(true)
	startup.lastAct.Store(time.Now().UnixNano())
	start := time.Now()
	go startup.watchSilence(sctx, 100*time.Millisecond, 80*time.Millisecond, adapterStartupGrace)
	deadline := time.After(3 * time.Second)
	for !startup.isDead() {
		select {
		case <-deadline:
			t.Fatalf("startup-stalled session never killed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if e := time.Since(start); e < 200*time.Millisecond {
		t.Fatalf("startup window killed at %v — silence limit 100ms beat the 250ms grace", e)
	}

	// Mid-turn silence after output began: the tight limit applies.
	mid := newSess()
	mctx, mcancel := context.WithCancel(context.Background())
	defer mcancel()
	mid.cancel = mcancel
	mid.inTurn.Store(true)
	mid.turnSeen.Store(true)
	mid.lastAct.Store(time.Now().UnixNano())
	mstart := time.Now()
	go mid.watchSilence(mctx, 100*time.Millisecond, 80*time.Millisecond, adapterStartupGrace)
	mdeadline := time.After(3 * time.Second)
	for !mid.isDead() {
		select {
		case <-mdeadline:
			t.Fatalf("mid-turn-stalled session never killed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if e := time.Since(mstart); e > 2*time.Second {
		t.Fatalf("mid-turn kill took %v — silence limit 100ms not enforced", e)
	}
}

// TestAdapterCandidateDirsFollowsSymlink verifies a packaged install layout:
// the real binary lives at <prefix>/bin/panda with adapters beside it under
// <prefix>/adapters, and a symlink on PATH (~/.local/bin/panda) resolves back
// to it. The candidate list must include <prefix>/adapters when handed the
// symlink path, or the spawned adapter would die with a missing path.
func TestAdapterCandidateDirsFollowsSymlink(t *testing.T) {
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(prefix, "bin", "panda")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Normalize prefix the same way EvalSymlinks does — on macOS /var is a
	// symlink to /private/var, so the candidate under the real binary reports
	// /private/... while t.TempDir() handed us /var/... .
	if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
		prefix = resolved
	}
	want := filepath.Join(prefix, "adapters")

	// Symlinks are unsupported on Windows without privileges; skip there.
	link := filepath.Join(t.TempDir(), "panda")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	found := false
	for _, cand := range adapterCandidateDirs(link) {
		if sameAbs(cand, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("adapterCandidateDirs(%q) missing %q (got %v)", link, want, adapterCandidateDirs(link))
	}
}

// TestAdapterCandidateDirsPrefersInstalledCopy pins the order: a stale
// adapters/ next to the PATH symlink must not outrank the scripts shipped
// beside the real binary. It did once, so a leftover ~/.local/adapters from
// an older layout silently kept running old adapter scripts.
func TestAdapterCandidateDirsPrefersInstalledCopy(t *testing.T) {
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(prefix, "bin", "panda")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
		prefix = resolved
	}
	installed := filepath.Join(prefix, "adapters")

	// Symlinks are unsupported on Windows without privileges; skip there.
	link := filepath.Join(t.TempDir(), "panda")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// The stale sibling a previous layout leaves behind: dir(link)/../adapters.
	stale := filepath.Join(filepath.Dir(filepath.Dir(link)), "adapters")

	idxInstalled, idxStale := -1, -1
	for i, cand := range adapterCandidateDirs(link) {
		if idxInstalled < 0 && sameAbs(cand, installed) {
			idxInstalled = i
		}
		if idxStale < 0 && sameAbs(cand, stale) {
			idxStale = i
		}
	}
	if idxInstalled < 0 || idxStale < 0 {
		t.Fatalf("candidates must name both the install and the stale sibling: %v", adapterCandidateDirs(link))
	}
	if idxInstalled > idxStale {
		t.Fatalf("installed adapters rank after the stale sibling: %v", adapterCandidateDirs(link))
	}
}

// sameAbs compares two paths after absolutizing + cleaning, so the test works
// whether the caller hands an absolute or relative link path.
func sameAbs(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		return a == b
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return a == b
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

// loopingAdapter streams tool_use events forever — the non-converging model
// the Go-side tool-call fuse exists for. Without a kill, the run would hang
// until the hard timeout.
const loopingAdapter = `#!/usr/bin/env python3
import json, sys, time
req = json.loads(sys.stdin.read())
i = 0
while True:
    sys.stderr.write(json.dumps({"type": "event", "ev": "tool_use", "name": "loop", "id": "c%d" % i}) + "\n")
    sys.stderr.flush()
    i += 1
    time.sleep(0.01)
`

// TestAdapterToolCallCapKillsRunaway: an adapter emitting tool calls past the
// fuse gets killed — the wall-clock timeout is a corpse bound, not a loop
// bound, and adapters that ignore max_turns otherwise burn tokens until it.
func TestAdapterToolCallCapKillsRunaway(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "loop.py"), []byte(loopingAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldCap := adapterToolCallCap
	adapterToolCallCap = 8
	defer func() { adapterToolCallCap = oldCap }()

	var seen atomic.Int64
	ctx := WithAgentEvents(context.Background(), func(ev AgentEvent) { seen.Add(1) })
	start := time.Now()
	res := runAdapterProcess(ctx, "loop.py", "p", "", nil)
	if res.OK {
		t.Fatal("runaway adapter must not report ok")
	}
	if res.ExitCode != 124 || !strings.Contains(res.Result, "tool-call budget") {
		t.Fatalf("capped result = code %d %q, want 124 + budget reason", res.ExitCode, res.Result)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("fuse did not kill promptly: %v", time.Since(start))
	}
	if seen.Load() > 8 {
		t.Fatalf("events past the cap still forwarded: %d", seen.Load())
	}
}

// sessionAnnouncingAdapter announces its minted session as an event (the
// adapter-side contract for checkpointing a killed run) and then hangs — so
// only the Go-side kill can end it, and the envelope never gets written.
const sessionAnnouncingAdapter = `#!/usr/bin/env python3
import json, sys, time
req = json.loads(sys.stdin.read())
sys.stderr.write(json.dumps({"type": "event", "ev": "session", "id": "ses_ann123"}) + "\n")
sys.stderr.flush()
time.sleep(3600)
print(json.dumps({"ok": True, "result": "done", "exit_code": 0}))
`

// TestAdapterKillPathsKeepAnnouncedSession: a run killed before its result
// envelope — silence watchdog here — must still report the session the
// adapter announced mid-stream. Without the latch the checkpoint was dropped
// and the orchestrator's retry cold-started, re-burning the whole run.
func TestAdapterKillPathsKeepAnnouncedSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ann.py"), []byte(sessionAnnouncingAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldSilence := silenceTimeout
	silenceTimeout = 200 * time.Millisecond
	defer func() { silenceTimeout = oldSilence }()

	res := runAdapterProcess(context.Background(), "ann.py", "p", "", nil)
	if res.OK || res.ExitCode != 124 {
		t.Fatalf("killed run = %+v, want failed 124", res)
	}
	if res.SessionID != "ses_ann123" {
		t.Fatalf("session = %q, want the announced ses_ann123", res.SessionID)
	}
}

// TestAdapterSessionEventWithoutSinkIsLatched: the checkpoint latch must not
// depend on a transcript consumer — a run with no WithAgentEvents sink still
// reports its announced session when killed.
func TestAdapterSessionEventWithoutSinkIsLatched(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ann.py"), []byte(sessionAnnouncingAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldTimeout := adapterHardTimeout
	adapterHardTimeout = 300 * time.Millisecond
	defer func() { adapterHardTimeout = oldTimeout }()
	oldAdvertised := adapterTimeoutS
	adapterTimeoutS = 1
	defer func() { adapterTimeoutS = oldAdvertised }()
	oldGrace := hardTimeoutGrace
	hardTimeoutGrace = 100 * time.Millisecond
	defer func() { hardTimeoutGrace = oldGrace }()

	// No WithAgentEvents — the latch lives on the writer, not the sink.
	res := runAdapterProcess(context.Background(), "ann.py", "p", "", nil)
	if res.ExitCode != 124 || res.SessionID != "ses_ann123" {
		t.Fatalf("hard-killed run = code %d session %q, want 124 + ses_ann123", res.ExitCode, res.SessionID)
	}
}

// TestAdapterSessionEventReachesSink: the minted session must reach the
// structured-event sink mid-run — the run loop persists it as the task's
// resume checkpoint the moment it arrives, so a kill before the result
// envelope still leaves a consumable handle. The sink copy also carries the
// adapter file that minted it (checkpoint resume is adapter-affine).
func TestAdapterSessionEventReachesSink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ann.py"), []byte(sessionAnnouncingAdapter), 0o755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}
	oldDir := adapterDir
	adapterDir = dir
	defer func() { adapterDir = oldDir }()
	oldTimeout := adapterHardTimeout
	adapterHardTimeout = 300 * time.Millisecond
	defer func() { adapterHardTimeout = oldTimeout }()
	oldAdvertised := adapterTimeoutS
	adapterTimeoutS = 1
	defer func() { adapterTimeoutS = oldAdvertised }()
	oldGrace := hardTimeoutGrace
	hardTimeoutGrace = 100 * time.Millisecond
	defer func() { hardTimeoutGrace = oldGrace }()

	var gotID, gotAdapter string
	ctx := WithAgentEvents(context.Background(), func(ev AgentEvent) {
		if ev.Ev == "session" {
			gotID, gotAdapter = ev.ID, ev.Adapter
		}
	})
	res := runAdapterProcess(ctx, "ann.py", "p", "", nil)
	if res.ExitCode != 124 || res.SessionID != "ses_ann123" {
		t.Fatalf("hard-killed run = code %d session %q, want 124 + ses_ann123", res.ExitCode, res.SessionID)
	}
	if gotID != "ses_ann123" {
		t.Fatalf("sink session id = %q, want ses_ann123 (checkpoint must persist mid-run)", gotID)
	}
	if gotAdapter != "ann.py" {
		t.Fatalf("sink adapter = %q, want ann.py (resume is adapter-affine)", gotAdapter)
	}
}

// TestResumeAdapterAffinity: buildAdapterRequest only sends the resume handle
// to the adapter that minted it — a fallback onto a different adapter must
// drop the foreign id instead of feeding it a session it cannot parse.
func TestResumeAdapterAffinity(t *testing.T) {
	ctx := WithResume(context.Background(), "sess-9", "opencode.py")

	req, _, refusal := buildAdapterRequest(ctx, "opencode.py", "p", "")
	if refusal != nil || req.Resume != "sess-9" {
		t.Fatalf("same-adapter resume = %q refusal %v, want sess-9", req.Resume, refusal)
	}
	req, _, refusal = buildAdapterRequest(ctx, "claude_code.py", "p", "")
	if refusal != nil || req.Resume != "" {
		t.Fatalf("foreign-adapter resume = %q refusal %v, want dropped", req.Resume, refusal)
	}
	// Unknown producer (legacy row): the offer stands — dropping it would
	// strand every session checkpoint written before the column existed.
	req, _, refusal = buildAdapterRequest(WithResume(context.Background(), "sess-old", ""), "claude_code.py", "p", "")
	if refusal != nil || req.Resume != "sess-old" {
		t.Fatalf("legacy resume = %q refusal %v, want sess-old", req.Resume, refusal)
	}
}

// TestBuildAdapterRequestCarriesConsent: the operator's tier-2 consent must
// reach the adapter subprocess, or a CLI that gates its own tool calls keeps
// auto-denying what a human just approved (issue #165). Restricted is the
// narrower promise and outranks consent.
func TestBuildAdapterRequestCarriesConsent(t *testing.T) {
	req, _, refusal := buildAdapterRequest(WithAuthorized(context.Background()), "claude_code.py", "p", "")
	if refusal != nil || !req.Authorized {
		t.Fatalf("consented request: authorized=%v refusal=%v, want true", req.Authorized, refusal)
	}
	req, _, refusal = buildAdapterRequest(context.Background(), "claude_code.py", "p", "")
	if refusal != nil || req.Authorized {
		t.Fatalf("unconsented request: authorized=%v refusal=%v, want false", req.Authorized, refusal)
	}
	req, _, refusal = buildAdapterRequest(WithAuthorized(WithRestricted(context.Background())), "claude_code.py", "p", "")
	if refusal != nil || !req.Restricted || req.Authorized {
		t.Fatalf("restricted consent: restricted=%v authorized=%v refusal=%v, want restricted only",
			req.Restricted, req.Authorized, refusal)
	}
}
