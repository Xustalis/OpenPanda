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
// The two shell families get different reference syntax because their parsers
// expand at different phases: POSIX after parsing (${VAR}), cmd.exe before
// parsing unless delayed expansion is on (!VAR!, with /V:ON from userShell).
func TestExpandEmitsEnvReferences(t *testing.T) {
	for _, tc := range []struct {
		goos string
		want string
	}{
		{"linux", `echo "${PANDA_HOOK_TASK_ID}" "${PANDA_HOOK_TITLE}"`},
		{"windows", `echo "!PANDA_HOOK_TASK_ID!" "!PANDA_HOOK_TITLE!"`},
	} {
		if got := expandFor(tc.goos, `echo {task_id} {title}`); got != tc.want {
			t.Errorf("%s: Expand = %q, want %q", tc.goos, got, tc.want)
		}
	}
	// A `%VAR%` reference is the bug this shape replaced: cmd.exe expands it
	// before quote parsing, so a title containing a quote can start a command.
	if got := expandFor("windows", `echo {title}`); strings.Contains(got, "%PANDA_HOOK") {
		t.Errorf("Windows reference still uses percent expansion: %q", got)
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

// TestExpandSourcePlaceholders pins the reply-hook vocabulary: an operator can
// answer back to the thread a request came from without splitting
// PANDA_HOOK_SOURCE_THREAD in the shell.
func TestExpandSourcePlaceholders(t *testing.T) {
	got := expandFor("linux", `gh issue comment {source_number} --repo {source_repo} --body {state}`)
	want := `gh issue comment "${PANDA_HOOK_SOURCE_NUMBER}" --repo "${PANDA_HOOK_SOURCE_REPO}" --body "${PANDA_HOOK_STATE}"`
	if got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
}

func TestHookEnvCarriesSourceFacts(t *testing.T) {
	env := strings.Join(hookEnv(Event{
		Name: EventDone, TaskID: "t", State: "done",
		Source: "github", SourceRepo: "acme/widgets", SourceNumber: "42",
		SourceURL: "https://github.com/acme/widgets/issues/42", SourceThread: "acme/widgets#42",
	}), "\n")
	for _, want := range []string{
		"PANDA_HOOK_SOURCE=github",
		"PANDA_HOOK_SOURCE_REPO=acme/widgets",
		"PANDA_HOOK_SOURCE_NUMBER=42",
		"PANDA_HOOK_SOURCE_URL=https://github.com/acme/widgets/issues/42",
		"PANDA_HOOK_SOURCE_THREAD=acme/widgets#42",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q:\n%s", want, env)
		}
	}
}
