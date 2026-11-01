// SPDX-License-Identifier: AGPL-3.0-or-later

package hooks

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestExpandEmitsEnvReferences pins the substitution shape: placeholders become
// references to the event's environment variables, never the values themselves.
func TestExpandEmitsEnvReferences(t *testing.T) {
	want := `echo "${PANDA_HOOK_TASK_ID}" "${PANDA_HOOK_TITLE}"`
	if runtime.GOOS == "windows" {
		want = `echo "%PANDA_HOOK_TASK_ID%" "%PANDA_HOOK_TITLE%"`
	}
	if got := Expand(`echo {task_id} {title}`, Event{TaskID: "t1", Title: "x"}); got != want {
		t.Errorf("Expand = %q, want %q", got, want)
	}
}

// TestHostileTitleStaysLiteral is the property the whole design rests on: a task
// title is model output, so it must reach the command as a value however the
// template quotes the placeholder. An earlier version spliced in single-quoted
// literals, which a template writing "{title}" inside its own double quotes
// defeated: the single quotes became ordinary characters there, and $(...)
// executed.
func TestHostileTitleStaysLiteral(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	payloads := []string{
		`$(echo PWNED)`,
		"`echo PWNED`",
		`'; echo PWNED; '`,
		`" ; echo PWNED ; "`,
		`$HOME`,
	}
	templates := []struct {
		tpl  string
		want func(v string) string
	}{
		{`echo {title}`, func(v string) string { return v }},
		{`echo "{title}"`, func(v string) string { return v }},
		{`echo prefix-{title}-suffix`, func(v string) string { return "prefix-" + v + "-suffix" }},
	}
	for _, payload := range payloads {
		for _, tc := range templates {
			out, err := RunOne(context.Background(), Hook{Command: tc.tpl},
				Event{Name: EventDone, Title: payload}, 5*time.Second)
			if err != nil {
				t.Fatalf("%q with %q: %v", tc.tpl, payload, err)
			}
			if got, want := strings.TrimSpace(out), tc.want(payload); got != want {
				t.Errorf("template %q with title %q printed %q, want %q — the value was executed, not passed",
					tc.tpl, payload, got, want)
			}
		}
	}
}

func TestValidEvent(t *testing.T) {
	for _, e := range Events {
		if !ValidEvent(e) {
			t.Errorf("%s should be valid", e)
		}
	}
	for _, bad := range []string{"", "task_start", "TaskDone", "done"} {
		if ValidEvent(bad) {
			t.Errorf("%q should not be valid", bad)
		}
	}
}

// TestRunOneRefusesTier2: the test path has no approval channel, so an
// irreversible command is refused rather than run.
func TestRunOneRefusesTier2(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	h := Hook{Event: EventDone, Command: "rm -rf /"}
	if _, err := RunOne(context.Background(), h, Event{Name: EventDone}, time.Second); !errors.Is(err, ErrTier2) {
		t.Errorf("err = %v, want ErrTier2", err)
	}
}

func TestRunOneExecutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	h := Hook{Event: EventDone, Command: "echo hello-{state}"}
	out, err := RunOne(context.Background(), h, Event{Name: EventDone, State: "done"}, 5*time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "hello-'done'") && !strings.Contains(out, "hello-done") {
		t.Errorf("output = %q", out)
	}
}

// TestRunOneHonoursTheTimeout: a hook that hangs must not hold a slot forever.
func TestRunOneHonoursTheTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	h := Hook{Event: EventDone, Command: "sleep 5"}
	start := time.Now()
	if _, err := RunOne(context.Background(), h, Event{Name: EventDone}, 150*time.Millisecond); err == nil {
		t.Error("a hanging command reported success")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout did not apply: %s", elapsed)
	}
}

// TestSafeEnvDropsCredentials is the "no model credentials in a hook" promise.
func TestSafeEnvDropsCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("PANDA_PANEL_TOKEN", "tok-secret")
	t.Setenv("PATH", "/usr/bin:/bin")

	env := safeEnv()
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "PANDA_PANEL_TOKEN", "sk-secret", "tok-secret"} {
		if strings.Contains(joined, secret) {
			t.Errorf("%s leaked into the hook environment", secret)
		}
	}
	if !strings.Contains(joined, "PATH=") {
		t.Error("PATH is missing: a hook could not run anything")
	}
	_ = os.Getenv("PATH")
}

// TestNewIsInertWithoutConfig: the default node must not even allocate a runner.
func TestNewIsInertWithoutConfig(t *testing.T) {
	if r := New(Config{}, nil, nil, ""); r != nil {
		t.Error("a disabled config produced a runner")
	}
	if r := New(Config{Enabled: true}, nil, nil, ""); r != nil {
		t.Error("an enabled config with no bindings produced a runner")
	}
}

// TestFireIgnoresUnboundEvents keeps the dispatch table honest.
func TestFireIgnoresUnboundEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixtures")
	}
	dir := t.TempDir()
	marker := dir + "/fired"
	r := New(Config{
		Enabled: true,
		On:      []Hook{{Event: EventDone, Command: "touch " + marker}},
	}, nil, nil, "")
	if r == nil {
		t.Fatal("runner not built")
	}
	r.Fire(Event{Name: EventFailed, TaskID: "t"})
	time.Sleep(120 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook fired for an event it was not bound to")
	}

	r.Fire(Event{Name: EventDone, TaskID: "t"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the bound hook never ran")
}
