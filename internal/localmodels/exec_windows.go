// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package localmodels

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// shimCommand builds the invocation, unwrapping the npm shims.
//
// A .cmd is a batch file, so CreateProcess cannot run it: cmd.exe has to. And
// cmd.exe re-parses its command line, while Go escapes each argument it passes
// — a path with a space in it ("D:/computer app/opencode/opencode.cmd") comes
// back from that round trip as a program name that includes its own quotes. The
// only reliable shape is a hand-built command line: /s makes cmd strip exactly
// one outer pair of quotes, leaving `"<path>" models`.
func shimCommand(ctx context.Context, cli string, args ...string) *exec.Cmd {
	lower := strings.ToLower(cli)
	if !strings.HasSuffix(lower, ".cmd") && !strings.HasSuffix(lower, ".bat") {
		return exec.CommandContext(ctx, cli, args...)
	}
	line := `cmd.exe /d /s /c ""` + filepath.FromSlash(cli) + `"`
	if len(args) > 0 {
		line += " " + strings.Join(args, " ")
	}
	line += `"`
	cmd := exec.CommandContext(ctx, "cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	// cmd.exe is a shim: the real CLI is its child. Killing the shim leaves the
	// grandchild holding our pipe, so a read would block past the deadline.
	// WaitDelay makes Wait give up (and close the pipe) once the context is
	// done, which is what unblocks the reader.
	cmd.WaitDelay = 2 * time.Second
	return cmd
}
