package commander

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestGenericHelperProcess is the child the native generic executor runs:
// it echoes argv, and when PANDA_HELPER_STDIN is set it prefixes the prompt
// it read from stdin. Never runs as a real test — -test.run selects it in a
// subprocess gated by PANDA_HELPER=1.
func TestGenericHelperProcess(t *testing.T) {
	if os.Getenv("PANDA_HELPER") != "1" {
		return
	}
	if d := os.Getenv("PANDA_HELPER_SLEEP"); d != "" {
		if dur, err := time.ParseDuration(d + "s"); err == nil {
			time.Sleep(dur)
		}
	}
	fmt.Println("helper-argv:" + strings.Join(os.Args[1:], " "))
	if os.Getenv("PANDA_HELPER_STDIN") == "1" {
		data, _ := io.ReadAll(os.Stdin)
		fmt.Println("helper-stdin:" + string(data))
	}
	os.Exit(0)
}

func helperCmd(t *testing.T, extra string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Quoted: the test binary path may contain spaces; the placeholder tail
	// rides after -test.run so the fake CLI sees it as plain argv.
	return fmt.Sprintf(`%q -test.run=TestGenericHelperProcess %s`, self, extra)
}

func TestExpandGenericTemplate(t *testing.T) {
	req := AdapterRequest{Prompt: "do it now", Cmd: `tool --prompt {prompt} --quiet`}
	argv, stdin := expandGenericTemplate(req)
	if stdin {
		t.Fatal("stdin claimed without {stdin}")
	}
	if got := strings.Join(argv, "|"); got != "tool|--prompt|do it now|--quiet" {
		t.Fatalf("argv = %q", got)
	}

	// Embedded form keeps the prompt inside one element; no placeholder
	// appends it as the last argument.
	req.Cmd = `tool run --p={prompt}`
	argv, _ = expandGenericTemplate(req)
	if got := strings.Join(argv, "|"); got != "tool|run|--p=do it now" {
		t.Fatalf("embedded argv = %q", got)
	}
	req.Cmd = `tool exec`
	argv, _ = expandGenericTemplate(req)
	if argv[len(argv)-1] != "do it now" {
		t.Fatalf("missing prompt append: %v", argv)
	}

	// {stdin} drops its element and claims the prompt for the pipe.
	req.Cmd = `tool exec - {stdin}`
	argv, stdin = expandGenericTemplate(req)
	if !stdin || strings.Join(argv, "|") != "tool|exec|-" {
		t.Fatalf("stdin form: argv=%v stdin=%v", argv, stdin)
	}

	// Unset optionals drop: the joined form loses the element, the bare
	// two-token form also loses the flag that would eat the next arg.
	req.Cmd = `tool run --session {resume} --turns={max_turns} {prompt}`
	argv, _ = expandGenericTemplate(req)
	if got := strings.Join(argv, "|"); got != "tool|run|do it now" {
		t.Fatalf("dropped argv = %q", got)
	}
	req.Resume = "sess-9"
	req.MaxTurns = 7
	argv, _ = expandGenericTemplate(req)
	if got := strings.Join(argv, "|"); got != "tool|run|--session|sess-9|--turns=7|do it now" {
		t.Fatalf("filled argv = %q", got)
	}
	req.Resume, req.MaxTurns = "", 0

	// New placeholders: task_id, effort, system_prompt, timeout_s.
	req.Cmd = `tool --task {task_id} --effort {effort} --sysp {system_prompt} --dl {timeout_s} {prompt}`
	req.TaskID, req.Effort, req.SystemPrompt, req.TimeoutS = "t-1", "high", "sys text", 30
	argv, _ = expandGenericTemplate(req)
	want := "tool|--task|t-1|--effort|high|--sysp|sys text|--dl|30|do it now"
	if got := strings.Join(argv, "|"); got != want {
		t.Fatalf("extended argv = %q, want %q", got, want)
	}

	// {env:NAME} resolves from the process env; unset drops flag + element.
	t.Setenv("PANDA_T_KEY", "sekrit")
	req = AdapterRequest{Prompt: "p", Cmd: `tool --key {env:PANDA_T_KEY} --other {env:PANDA_T_MISSING} end`}
	argv, _ = expandGenericTemplate(req)
	// No {prompt} placeholder → the prompt appends as the last argument.
	if got := strings.Join(argv, "|"); got != "tool|--key|sekrit|end|p" {
		t.Fatalf("env argv = %q", got)
	}

	// cwd resolves to the request dir (or the process cwd when unset).
	req = AdapterRequest{Prompt: "p", Cmd: `tool --dir {cwd} {prompt}`, CWD: "/w"}
	argv, _ = expandGenericTemplate(req)
	if argv[1] != "--dir" || argv[2] != "/w" {
		t.Fatalf("cwd argv = %v", argv)
	}

	// Prompt text is substituted once and never rescanned: a literal
	// "{cwd}" inside the prompt survives verbatim.
	req = AdapterRequest{Prompt: "fix {cwd} and {resume}", Cmd: `tool --p {prompt}`, CWD: "/w"}
	argv, _ = expandGenericTemplate(req)
	if got := strings.Join(argv, "|"); got != "tool|--p|fix {cwd} and {resume}" {
		t.Fatalf("rescanned prompt: %q", got)
	}

	// Empty/expands-to-nothing templates are config errors.
	for _, cmd := range []string{"", "  ", "{resume}"} {
		if argv, _ := expandGenericTemplate(AdapterRequest{Prompt: "p", Cmd: cmd}); argv != nil {
			t.Fatalf("template %q should expand to nil, got %v", cmd, argv)
		}
	}
}

func TestRunGenericNativeExec(t *testing.T) {
	ctx := WithAgentCommand(context.Background(), helperCmd(t, `{prompt}`))
	res := runGenericNative(ctx, "fakecli", "the prompt", t.TempDir(),
		[]string{"PANDA_HELPER=1"})
	if !res.OK {
		t.Fatalf("run failed: %+v", res)
	}
	if !strings.Contains(res.Result, "helper-argv:") ||
		!strings.Contains(res.Result, "the prompt") {
		t.Fatalf("result = %q", res.Result)
	}
}

func TestRunGenericNativeStdin(t *testing.T) {
	ctx := WithAgentCommand(context.Background(), helperCmd(t, `{stdin}`))
	res := runGenericNative(ctx, "fakecli", "piped prompt", t.TempDir(),
		[]string{"PANDA_HELPER=1", "PANDA_HELPER_STDIN=1"})
	if !res.OK {
		t.Fatalf("run failed: %+v", res)
	}
	if !strings.Contains(res.Result, "helper-stdin:piped prompt") {
		t.Fatalf("result = %q", res.Result)
	}
}

func TestRunGenericNativeExitCodes(t *testing.T) {
	// Empty template → config error 2, nothing spawned.
	ctx := WithAgentCommand(context.Background(), "")
	res := runGenericNative(ctx, "fakecli", "p", t.TempDir(), nil)
	if res.OK || res.ExitCode != 2 {
		t.Fatalf("empty template: %+v", res)
	}
	// Missing binary → 127.
	ctx = WithAgentCommand(context.Background(), "definitely-not-on-path-xyz {prompt}")
	res = runGenericNative(ctx, "fakecli", "p", t.TempDir(), nil)
	if res.OK || res.ExitCode != 127 {
		t.Fatalf("missing binary: %+v", res)
	}
	// Non-zero exit propagates the child's code and stderr diagnosis.
	self, _ := os.Executable()
	ctx = WithAgentCommand(context.Background(),
		fmt.Sprintf(`%q -test.run=TestGenericHelperFail`, self))
	res = runGenericNative(ctx, "fakecli", "p", t.TempDir(), []string{"PANDA_HELPER=1"})
	if res.OK || res.ExitCode != 3 {
		t.Fatalf("fail exit: %+v", res)
	}
	if !strings.Contains(res.Result, "helper-fail") {
		t.Fatalf("fail result = %q", res.Result)
	}
}

// TestGenericHelperFail is the failing fake CLI: exit 3 with a stderr line.
// Gated like the helper above so the suite itself never takes the exit.
func TestGenericHelperFail(t *testing.T) {
	if os.Getenv("PANDA_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "helper-fail diagnosis")
	os.Exit(3)
}

func TestRunGenericNativeTimeout(t *testing.T) {
	// The native executor is its own watchdog: a tiny advertised budget must
	// actually kill the run (script adapters enforce it inside the process).
	// Shrink the post-budget grace so the deadline lands at ~1.1s.
	old := hardTimeoutGrace
	hardTimeoutGrace = 100 * time.Millisecond
	defer func() { hardTimeoutGrace = old }()
	ctx := WithAgentTimeout(context.Background(), 1*time.Second)
	ctx = WithAgentCommand(ctx, helperCmd(t, ``))
	res := runGenericNative(ctx, "fakecli", "p", t.TempDir(),
		[]string{"PANDA_HELPER=1", "PANDA_HELPER_SLEEP=3"})
	if res.OK {
		t.Fatalf("timeout run reported ok: %+v", res)
	}
	if res.ExitCode != 124 {
		t.Fatalf("timeout exit = %d, want 124", res.ExitCode)
	}
}
