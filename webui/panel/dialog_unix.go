//go:build !windows

// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Bound picker helper processes so an unanswered dialog cannot park the
// handler forever. Windows uses an in-process COM picker rather than a helper.
const dialogTimeout = 5 * time.Minute

// openNativeDirectoryDialog raises the desktop's own folder chooser: the
// Finder sheet on macOS, zenity/kdialog on Linux. Anything without one of
// those — a bare server, a container — answers errNoNativeDialog so the
// console opens its in-page browser instead. Every helper runs under
// dialogTimeout: a window nobody answers must not park this handler (and the
// next click) forever.
func openNativeDirectoryDialog(initial, title string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialogTimeout)
	defer cancel()
	if title == "" {
		title = "Select Folder"
	}
	initial = existingDir(initial)

	switch runtime.GOOS {
	case "darwin":
		// `choose folder` answers with an HFS path ("disk:Users:…"), hence the
		// POSIX conversion. Cancel exits 1 with "User canceled. (-128)".
		script := "POSIX path of (choose folder with prompt " + appleScriptString(title) + darwinDefaultLocation(initial) + ")"
		out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output()
		if err != nil {
			if isUserCancel(err) {
				return "", true, nil
			}
			return "", false, err
		}
		return strings.TrimRight(strings.TrimSpace(string(out)), "/"), false, nil

	case "linux":
		if _, err := exec.LookPath("zenity"); err == nil {
			args := []string{"--file-selection", "--directory", "--title=" + title}
			if initial != "" {
				// The trailing separator makes zenity start *inside* the
				// folder rather than selecting it.
				args = append(args, "--filename="+strings.TrimRight(initial, "/")+"/")
			}
			out, err := exec.CommandContext(ctx, "zenity", args...).Output()
			if err != nil {
				return "", true, nil // zenity exits 1 on cancel
			}
			return strings.TrimRight(strings.TrimSpace(string(out)), "/"), false, nil
		}
		if _, err := exec.LookPath("kdialog"); err == nil {
			args := []string{"--getexistingdirectory", initial, "--title", title}
			out, err := exec.CommandContext(ctx, "kdialog", args...).Output()
			if err != nil {
				return "", true, nil // kdialog exits 1 on cancel
			}
			return strings.TrimRight(strings.TrimSpace(string(out)), "/"), false, nil
		}
		return "", false, errNoNativeDialog

	default:
		return "", false, errNoNativeDialog
	}
}

// darwinDefaultLocation appends the "start here" clause to the AppleScript, or
// nothing when there is no folder to start from.
func darwinDefaultLocation(initial string) string {
	if initial == "" {
		return ""
	}
	return " default location POSIX file " + appleScriptString(initial)
}

// appleScriptString quotes a Go string as an AppleScript literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// isUserCancel distinguishes a Cancel click from a broken script.
func isUserCancel(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "exit status 1") || strings.Contains(msg, "User canceled") || strings.Contains(msg, "-128")
}
