// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// /edit <path>[:line] — open a file at a line in the user's own editor.
//
// This is the other half of `panda read path:line`: read shows the window, edit
// puts the cursor there. Neither is a viewer the console has to grow — the
// console's drawer is for reading in a browser, and an editor is what a person
// already trusts to change a file.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// editTarget resolves an /edit argument into an absolute path and a focus line.
// A relative path is read against the thread's workspace — its worktree when it
// has one, the node's work path otherwise — because that is where the agent it
// is talking about was working.
func editTarget(r *repl, arg string) (string, int, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", 0, errors.New("usage")
	}
	path, line := splitLineSuffix(arg)
	path = strings.TrimSpace(path)
	if path == "" {
		return "", 0, errors.New("usage")
	}
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if !filepath.IsAbs(path) {
		if base := replWorkDir(r); base != "" {
			path = filepath.Join(base, path)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", 0, err
	}
	if st.IsDir() {
		return "", 0, fmt.Errorf("%s is a directory", abs)
	}
	return abs, line, nil
}

// replWorkDir is the directory this REPL's turns run in.
func replWorkDir(r *repl) string {
	if r == nil {
		return ""
	}
	if id := r.sessID(); id != "" && r.sessionsSt != nil {
		if sess, err := r.sessionsSt.Get(id); err == nil && sess.Worktree != "" {
			return sess.Worktree
		}
	}
	if eng := r.engine.Load(); eng != nil {
		return eng.WorkPath()
	}
	return ""
}

// cmdEdit answers /edit in the line-oriented REPL: the editor takes the
// terminal, and the loop resumes when it exits.
func (r *repl) cmdEdit(arg string) {
	out := r.commandOutput()
	path, line, err := editTarget(r, arg)
	if err != nil {
		if err.Error() == "usage" {
			fmt.Fprintln(out, i18n.T(r.loc, "repl.edit.usage"))
			return
		}
		fmt.Fprintf(out, "%s: %v\n", i18n.T(r.loc, "repl.edit.failed"), err)
		return
	}
	cmd, err := editorCommand(path, line)
	if err != nil {
		fmt.Fprintf(out, "%s: %v\n", i18n.T(r.loc, "repl.edit.failed"), err)
		return
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(out, "%s: %v\n", i18n.T(r.loc, "repl.edit.failed"), err)
	}
}
