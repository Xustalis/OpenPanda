// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// localActionTimeout bounds a local action. It is generous because `doctor`
// probes endpoints, but finite because the ask must never hang on a
// subprocess: an action that runs longer than this is not a small task.
const localActionTimeout = 45 * time.Second

// SetLocalActionRunner replaces how a matched local action is executed. nil
// restores the built-in runner (this binary, bounded). Tests use it to assert
// the short-circuit without spawning processes.
func (e *Engine) SetLocalActionRunner(run func(ctx context.Context, argv []string) (string, error)) {
	e.localRunner = run
}

// RunLocalAction executes a matched action and returns the text to show. ok is
// false when the caller should fall through to the normal pipeline: a failed
// action must degrade to the expensive path, never to an error, because the
// model may still know a better way to do it.
func (e *Engine) RunLocalAction(ctx context.Context, act entry.LocalAction, loc i18n.Locale) (string, bool) {
	// Starting a second console is the one side effect worth preventing: the
	// panel binds with an alternate-port fallback, so a duplicate would not
	// even fail loudly — it would quietly serve a second UI on 7841.
	if act.ID == "web" {
		if url, up := e.consoleURL(); up {
			return i18n.Tf(loc, "ask.localAction.already", "name", "web console", "url", url), true
		}
	}
	run := e.localRunner
	if run == nil {
		run = defaultLocalRunner
	}
	out, err := run(ctx, act.Argv)
	if err != nil && !(act.ExitStatusIsReport && isExitStatus(err)) {
		// The decline is intentional: the normal path can still accomplish
		// this with an agent, so a local failure is a fallback, not a verdict.
		e.logger.Info("local action failed; falling back to the task path",
			"argv", act.Argv, "err", err)
		return "", false
	}
	if err != nil {
		// A report action that exited non-zero still produced the answer
		// (that is what doctor's exit code means). Show it.
		e.logger.Info("local action reported findings", "argv", act.Argv, "err", err)
	}
	text := i18n.Tf(loc, "ask.localAction.done", "cmd", "panda "+strings.Join(act.Argv, " "))
	if s := strings.TrimSpace(out); s != "" {
		text += "\n\n" + s
	}
	return text, true
}

// isExitStatus distinguishes "the child ran and chose a non-zero status" from
// "the child never ran" (missing binary, permission, timeout). Only the first
// is a report for actions that mark it so; a timeout must always fall back.
func isExitStatus(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)
}

// consoleURL reports whether a web console already answers on the configured
// panel address, and where.
func (e *Engine) consoleURL() (string, bool) {
	addr := "127.0.0.1:7840"
	if e.cfg != nil {
		if v := strings.TrimSpace(e.cfg.Network.PanelAddr); v != "" {
			addr = v
		}
	}
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return "", false
	}
	_ = conn.Close()
	return "http://" + addrsForURL(addr), true
}

// addrsForURL turns a bind address into one a browser can open: a wildcard
// bind means "this machine", so it becomes loopback.
func addrsForURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// defaultLocalRunner executes the action with this same binary. It is
// deliberately not a shell: argv comes from a closed table, and a shell would
// turn a future table entry into an injection surface.
func defaultLocalRunner(ctx context.Context, argv []string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, localActionTimeout)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(cctx, exe, argv...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.Stdin = nil
	// The buffered text is what the user asked for; a non-zero exit is
	// reported as an error so the caller falls through to the task path with
	// the output already captured for the log.
	err = cmd.Run()
	if err != nil {
		// A killed child looks exactly like a non-zero exit, so name the
		// timeout here: the caller must not mistake "took too long" for
		// "ran and found problems" (see LocalAction.ExitStatusIsReport).
		if cerr := cctx.Err(); cerr != nil {
			return buf.String(), fmt.Errorf("local action %v: %w", argv, cerr)
		}
		return buf.String(), err
	}
	return buf.String(), nil
}
