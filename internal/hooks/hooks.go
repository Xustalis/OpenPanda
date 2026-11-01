// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hooks runs the operator's own commands at task lifecycle points.
//
// This is the one extension point the harness was missing. OpenPanda already
// exposes state transitions as a first-class event surface, so a hook is a
// command bound to one of those edges — a desktop notification, a log line, a
// ticket — and nothing more. It is deliberately not a tool-level hook: tool
// calls are already covered by the MCP server and the skill library, and a hook
// firing per tool call would be a second execution surface nobody reviews.
//
// Every hook runs under the constraints this node applies to any other command
// it executes: the defense tier decides whether it may run unattended, and the
// audit log records that it did. The child environment is a whitelist, so a hook
// never inherits the node's model credentials.
//
// Event values reach the command through that environment, never through the
// command line. A task title comes from a model, and a model writing
// "fix the bug; rm -rf ~" is a sentence, not an instruction — but the template
// author decides where the placeholder sits, and a substitution inside the
// template's own double quotes defeats shell quoting (the quoted literal is an
// ordinary character there, and $(...) still expands). Values therefore travel
// as PANDA_HOOK_* variables that the placeholder references.
package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// The lifecycle edges a hook may bind to. They are the task state machine's own
// names, not a second vocabulary invented for this package.
const (
	EventSubmitted = "task_submitted"
	EventStarted   = "task_started"
	EventDone      = "task_done"
	EventFailed    = "task_failed"
	EventReview    = "task_review"
)

// Events lists every bindable edge, in lifecycle order.
var Events = []string{EventSubmitted, EventStarted, EventDone, EventFailed, EventReview}

// Event is what a hook template can see.
type Event struct {
	Name    string
	TaskID  string
	Title   string
	State   string
	Project string
	Owner   string
}

// Hook is one configured binding.
type Hook struct {
	Event   string
	Command string
	Timeout time.Duration
}

// Config is the resolved hook configuration.
type Config struct {
	Enabled bool
	// Timeout is the default budget for a hook that does not set its own.
	Timeout time.Duration
	// AllowTier2 lets a hook run commands the defense tier grades irreversible.
	// Off by default: a hook is unattended by definition, and an irreversible
	// command with no one watching is the case the tier exists for.
	AllowTier2 bool
	On         []Hook
}

// Runner fires the configured hooks. A nil Runner is a no-op, so callers do not
// need to check before wiring.
type Runner struct {
	cfg    Config
	logger *slog.Logger
	audit  *security.Audit
	nodeID string
	sem    chan struct{}
}

// maxConcurrentHooks bounds how many hook commands run at once. Hooks are
// fire-and-forget, but "fire and forget" must not mean "fork a process per
// event with no ceiling".
const maxConcurrentHooks = 4

// maxAuditDetail bounds what a failure writes into the audit log.
const maxAuditDetail = 400

// New builds a Runner. audit and nodeID may be zero: entries are then skipped
// rather than faked.
func New(cfg Config, logger *slog.Logger, audit *security.Audit, nodeID string) *Runner {
	if !cfg.Enabled || len(cfg.On) == 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		cfg:    cfg,
		logger: logger,
		audit:  audit,
		nodeID: nodeID,
		sem:    make(chan struct{}, maxConcurrentHooks),
	}
}

// Bindings returns the configured hooks, for `panda hooks list`.
func (r *Runner) Bindings() []Hook {
	if r == nil {
		return nil
	}
	return append([]Hook(nil), r.cfg.On...)
}

// Fire runs every hook bound to this event. It never blocks the caller and
// never returns an error: it is called from inside the transaction that recorded
// the event, so a state transition must not wait on somebody's notification
// script — and when the concurrency ceiling is full, the event is dropped with a
// log line rather than backed up into the writer's path.
func (r *Runner) Fire(ev Event) {
	if r == nil {
		return
	}
	for _, h := range r.cfg.On {
		if h.Event != ev.Name {
			continue
		}
		h := h
		select {
		case r.sem <- struct{}{}:
			guard.Go(r.logger, "hooks: run", nil, func() {
				defer func() { <-r.sem }()
				r.execute(h, ev)
			})
		default:
			r.logger.Warn("hooks: concurrency ceiling reached, event dropped",
				"event", ev.Name, "task", ev.TaskID)
		}
	}
}

// ErrTier2 is RunOne's refusal for a command the defense tier grades
// irreversible — `panda hooks test` is still an unattended execution unless the
// user says otherwise, and the test path has no approval channel.
var ErrTier2 = fmt.Errorf("the command is graded irreversible")

// RunOne executes one binding synchronously and returns its combined output.
// This is the `panda hooks test` path: the user asked for this specific hook, is
// watching the terminal, and wants the output rather than a fire-and-forget.
func RunOne(ctx context.Context, h Hook, ev Event, timeout time.Duration) (string, error) {
	line := Expand(h.Command, ev)
	shell, flag := userShell()
	if defense.TierFromCommand("sh", "-c", line) != defense.TierReversible {
		return "", ErrTier2
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := executil.CommandContext(runCtx, shell, flag, line)
	cmd.Env = append(safeEnv(), hookEnv(ev)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// execute runs one binding for real: tier gate, bounded run, audit row.
func (r *Runner) execute(h Hook, ev Event) {
	line := Expand(h.Command, ev)
	shell, flag := userShell()
	// Grade the payload with a FIXED scanner name. TierFromCommand only walks
	// the command text when the interpreter is one it recognises, so keying the
	// scan on the user's $SHELL meant an unrecognised shell (nu, elvish, xonsh)
	// downgraded every hook — a literal `rm -rf /` included — to tier 1 and ran
	// it with no one watching. What shell will execute it is a separate question
	// from what commands it contains.
	tier := defense.TierFromCommand("sh", "-c", line)
	if tier != defense.TierReversible && !r.cfg.AllowTier2 {
		r.logger.Warn("hooks: refused a tier-2 command",
			"event", ev.Name, "task", ev.TaskID, "tier", tier)
		r.record(ev, h, "denied", "the command is graded irreversible; set hooks.allow_tier2 to run it")
		return
	}

	timeout := h.Timeout
	if timeout <= 0 {
		timeout = r.cfg.Timeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := executil.CommandContext(ctx, shell, flag, line)
	cmd.Env = append(safeEnv(), hookEnv(ev)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A hook's output is the user's own command text, but a command that
		// echoes a token (a curl call, a header dump) must not put it in the
		// daemon log. security owns redaction; route through it.
		detail := security.Redact(strings.TrimSpace(string(out)))
		if len(detail) > maxAuditDetail {
			detail = detail[:maxAuditDetail]
		}
		r.logger.Warn("hooks: command failed",
			"event", ev.Name, "task", ev.TaskID, "err", security.Redact(err.Error()), "output", detail)
		r.record(ev, h, "failed", err.Error())
		return
	}
	r.record(ev, h, "ok", "")
}

// record writes one audit row. A node without an audit log or an id keeps
// running; the row is evidence, not a dependency.
func (r *Runner) record(ev Event, h Hook, result, detail string) {
	if r.audit == nil || r.nodeID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = r.audit.Record(ctx, security.Entry{
		Who:    r.nodeID,
		What:   "hook:" + ev.Name,
		Target: h.Command,
		Result: result,
		Detail: detail,
	})
}

// The environment variables a hook command reads the event from. Values travel
// this way rather than being spliced into the command line: a substitution that
// lands inside the template's own double quotes is not protected by shell
// quoting (a single-quoted literal is an ordinary character there, and $(...)
// still expands), so the only construction that holds for every template is one
// where the value never enters the shell's parser at all.
const (
	EnvTaskID  = "PANDA_HOOK_TASK_ID"
	EnvTitle   = "PANDA_HOOK_TITLE"
	EnvState   = "PANDA_HOOK_STATE"
	EnvProject = "PANDA_HOOK_PROJECT"
	EnvOwner   = "PANDA_HOOK_OWNER"
	EnvEvent   = "PANDA_HOOK_EVENT"
)

// Expand substitutes placeholder tokens with references to those variables.
// The reference is emitted double-quoted so it is one word whatever the
// surrounding template does: {title} and "{title}" both end up as a single
// argument, and the value is expanded by the shell, never parsed by it.
func Expand(template string, ev Event) string {
	ref := func(name string) string {
		if runtime.GOOS == "windows" {
			// cmd.exe expands %VAR% inside and outside quotes, and the
			// expanded text is not re-parsed as syntax.
			return `"%` + name + `%"`
		}
		return `"${` + name + `}"`
	}
	repl := strings.NewReplacer(
		"{task_id}", ref(EnvTaskID),
		"{title}", ref(EnvTitle),
		"{state}", ref(EnvState),
		"{project}", ref(EnvProject),
		"{owner}", ref(EnvOwner),
		"{event}", ref(EnvEvent),
	)
	return repl.Replace(template)
}

// hookEnv is the event, as the child sees it.
func hookEnv(ev Event) []string {
	return []string{
		EnvTaskID + "=" + ev.TaskID,
		EnvTitle + "=" + ev.Title,
		EnvState + "=" + ev.State,
		EnvProject + "=" + ev.Project,
		EnvOwner + "=" + ev.Owner,
		EnvEvent + "=" + ev.Name,
	}
}

// safeEnv is the whitelist a hook command inherits. Credentials are absent by
// construction: the node holds model keys and peer tokens in its own process,
// and a hook is a shell command that has no business seeing them.
func safeEnv() []string {
	keep := []string{
		"PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR", "TEMP", "TMP",
		"SHELL", "USER", "LOGNAME",
		// Windows: without SYSTEMROOT most executables cannot even start.
		"COMSPEC", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "USERPROFILE", "PATHEXT",
		"APPDATA", "LOCALAPPDATA",
	}
	env := make([]string, 0, len(keep))
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// userShell is the shell a hook runs under: the user's own, so a hook behaves
// the way the command would in their terminal.
func userShell() (shell, flag string) {
	if runtime.GOOS == "windows" {
		if sh := os.Getenv("COMSPEC"); sh != "" {
			return sh, "/c"
		}
		return "cmd.exe", "/c"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, "-c"
	}
	return "/bin/sh", "-c"
}

// ValidEvent reports whether name is a bindable edge.
func ValidEvent(name string) bool {
	for _, e := range Events {
		if e == name {
			return true
		}
	}
	return false
}

// ParseTimeout reads a "timeout_s" value, treating anything non-positive or
// absent as "use the default".
func ParseTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// Describe renders a binding for the CLI's listing.
func (h Hook) Describe() string {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return fmt.Sprintf("%s → %s (%s)", h.Event, h.Command, timeout)
}
