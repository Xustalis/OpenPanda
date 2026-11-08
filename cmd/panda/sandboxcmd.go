// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `/sandbox` — the OS subprocess sandbox as a first-class command. Its state is
// config.yaml's sandbox.mode, and it is the one policy whose effect lives
// outside the config file: every spawned subprocess reads a process-wide base
// policy installed at startup (commander.SetSandboxConfig). Writing the file
// alone would leave the running node unconfined until a restart, and reporting
// "strict" while subprocesses still run bare is the worst of both — so the set
// path writes the file, moves the in-memory config, and reinstalls the policy,
// in that order, before it reports anything.

import (
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// sandboxCommand is the whole command: no argument (or "status"/"show")
// reports, a mode argument sets. Callers print what it returns, so the TUI and
// the classic REPL cannot drift on either the text or the effects.
func (r *repl) sandboxCommand(arg string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(arg))
	if want == "" || want == "status" || want == "show" {
		return r.sandboxReport(), nil
	}
	mode, err := config.ParseSandboxMode(want)
	if err != nil {
		return "", err
	}
	// Persist + in-memory write in one critical section, the same rule every
	// other config-writing surface follows: an embedded /web panel sharing
	// this lock would otherwise interleave a whole-document rewrite.
	if err := r.mutateConfigErr(func(c *config.Config) error {
		if c.Sandbox.NormalizedMode() == mode {
			return nil
		}
		if path := configWritePath(r.configPath); path != "" {
			if err := config.UpdateSectionField(path, []string{"sandbox"}, "mode", mode); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
		}
		c.Sandbox.Mode = mode
		return nil
	}); err != nil {
		return "", err
	}
	// Install the policy before building the report: the report is the only
	// evidence the operator gets, and it must not describe a boundary that is
	// not there yet. The engine owns the config/card paths the startup install
	// used, so asking it keeps a runtime switch identical to a restart's.
	backend := security.Backend()
	if eng := r.engine.Load(); eng != nil {
		backend = eng.ApplySandboxPolicy()
	} else {
		card, _ := r.cardInfo()
		r.readConfig(func(c *config.Config) {
			backend = commander.ApplyFromConfig(c, configWritePath(r.configPath), card)
		})
	}
	out := i18n.Tf(r.loc, "repl.sandbox.saved", "mode", mode) + "\n" + r.sandboxReport()
	// A configured mode with no backend is not an error — it is a fact the
	// operator has to hear, because nothing is being confined.
	if mode != config.SandboxModeOff && backend == "" {
		out += "\n" + i18n.T(r.loc, "repl.sandbox.noBackend")
	}
	return out, nil
}

// sandboxReport renders the effective state: what the config asks for, what
// the platform can do, and whether the two meet.
func (r *repl) sandboxReport() string {
	var mode, workPath string
	r.readConfig(func(c *config.Config) {
		mode = c.Sandbox.NormalizedMode()
		workPath = c.Storage.WorkPath
	})
	if mode == "" {
		mode = config.SandboxModeOff
	}
	backend := security.Backend()
	if backend == "" {
		backend = "-"
	}
	active := mode != config.SandboxModeOff && backend != "-"
	var b strings.Builder
	b.WriteString(i18n.T(r.loc, "repl.sandbox.state"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "  mode:    %s\n", mode)
	fmt.Fprintf(&b, "  backend: %s\n", backend)
	fmt.Fprintf(&b, "  active:  %v\n", active)
	if workPath != "" {
		fmt.Fprintf(&b, "  path:    %s\n", workPath)
	}
	b.WriteString("  " + i18n.T(r.loc, "repl.sandbox.hint"))
	return b.String()
}

// cmdSandbox is the classic REPL entry.
func (r *repl) cmdSandbox(arg string) {
	msg, err := r.sandboxCommand(arg)
	if err != nil {
		r.errf("%s\n", "panda: "+err.Error())
		return
	}
	r.outln(msg)
}
