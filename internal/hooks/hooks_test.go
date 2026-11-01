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

// TestExpandQuotesEveryValue is the security property this package exists to
// hold: a task title is model output, and model output is a sentence, not a
// script. An unquoted substitution would make every title a command line.
func TestExpandQuotesEveryValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cmd.exe quoting differs")
	}
	evil := Event{
		Name:    EventDone,
		TaskID:  "t1; rm -rf ~",
		Title:   "fix $(whoami) the bug `id`",
		Project: "proj && echo pwned",
		Owner:   "node|cat /etc/passwd",
	}
	got := Expand(`echo {task_id} {title} {project} {owner} {event}`, evil)

	for _, bad := range []string{"rm -rf ~", "$(whoami)", "`id`", "&& echo pwned", "|cat"} {
		if strings.Contains(got, bad) && !strings.Contains(got, "'"+bad) {
			// The dangerous text may appear only inside single quotes.
			idx := strings.Index(got, bad)
			if !insideSingleQuotes(got, idx) {
				t.Errorf("%q escaped quoting: %s", bad, got)
			}
		}
	}
	if !strings.Contains(got, "'") {
		t.Errorf("no quoting was applied at all: %s", got)
	}
}

// insideSingleQuotes reports whether position i falls inside a single-quoted
// run, honouring the '\” escape this package emits.
func insideSingleQuotes(s string, i int) bool {
	quoted := false
	for j := 0; j < i && j < len(s); j++ {
		if s[j] != '\'' {
			continue
		}
		// A closing ' immediately followed by \' is the escape, not a close.
		if quoted && j+2 < len(s) && s[j+1] == '\\' && s[j+2] == '\'' {
			continue
		}
		quoted = !quoted
	}
	return quoted
}

// TestExpandQuotesASingleQuote keeps the escape correct: an apostrophe inside a
// title must not end the literal.
func TestExpandQuotesASingleQuote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cmd.exe quoting differs")
	}
	got := Expand(`{title}`, Event{Title: "it's broken"})
	if got != `'it'\''s broken'` {
		t.Errorf("quoting = %q", got)
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
