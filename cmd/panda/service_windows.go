// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/install"
)

// serviceInstall writes the wrapper .cmd and registers a logon task. A task,
// not a service: it runs as the signed-in user, which is where the agent CLIs
// and the user's config live.
func serviceInstall(spec webServiceSpec, dry bool) int {
	wrapper := serviceUnitPath(spec)
	content := renderWindowsWrapper(spec)
	if dry {
		fmt.Printf("wrapper: %s\n\n%s\n", wrapper, content)
		fmt.Printf("task: schtasks /Create /TN %s /SC ONLOGON /RL LIMITED /TR %q /F\n", webServiceTaskName, wrapper)
		return 0
	}
	token, terr := ensurePanelToken()
	if terr != nil {
		fmt.Fprintf(os.Stderr, "service: panel token will be ephemeral: %v\n", terr)
	}
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1
	}
	if err := os.WriteFile(wrapper, content, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1
	}
	if out, err := exec.Command("schtasks.exe", "/Create", "/F", "/SC", "ONLOGON",
		"/RL", "LIMITED", "/TN", webServiceTaskName, "/TR", wrapper).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "service: schtasks failed: %v\n%s\n", err, out)
		return 1
	}
	// Registering does not start it; start now so install is one action.
	_ = exec.Command("schtasks.exe", "/Run", "/TN", webServiceTaskName).Run()
	fmt.Printf("installed task %s (wrapper %s)\n", webServiceTaskName, wrapper)
	fmt.Printf("url   %s\n", consoleURL(token))
	fmt.Printf("log   %s\n", spec.LogPath)
	return 0
}

func serviceStatus(spec webServiceSpec) int {
	out, err := exec.Command("schtasks.exe", "/Query", "/TN", webServiceTaskName).CombinedOutput()
	if err != nil {
		fmt.Printf("not installed (task %s missing)\n", webServiceTaskName)
		return 1
	}
	state := "registered"
	if text := string(out); !containsFold(text, "Running") {
		state = "stopped"
	}
	fmt.Printf("task  %s\n", webServiceTaskName)
	fmt.Printf("state %s\n", state)
	if token, err := ensurePanelToken(); err == nil {
		fmt.Printf("url   %s\n", consoleURL(token))
	}
	fmt.Printf("log   %s\n", spec.LogPath)
	printNodeRow()
	return 0
}

func containsFold(s, sub string) bool {
	// Small ASCII case-insensitive search; schtasks output is locale-dependent,
	// and "Running" is the English task state this parser expects.
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func serviceRestart(spec webServiceSpec) int {
	if out, err := exec.Command("schtasks.exe", "/Query", "/TN", webServiceTaskName).CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "service: not installed; run `panda service install` first")
		_ = out
		return 1
	}
	_ = exec.Command("schtasks.exe", "/End", "/TN", webServiceTaskName).Run()
	if out, err := exec.Command("schtasks.exe", "/Run", "/TN", webServiceTaskName).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "service: schtasks /Run failed: %v\n%s\n", err, out)
		return 1
	}
	fmt.Println("restarted")
	return 0
}

func serviceUninstall(spec webServiceSpec) int {
	if out, err := exec.Command("schtasks.exe", "/Query", "/TN", webServiceTaskName).CombinedOutput(); err != nil {
		fmt.Println("not installed")
		_ = out
		return 0
	}
	_ = exec.Command("schtasks.exe", "/End", "/TN", webServiceTaskName).Run()
	if out, err := exec.Command("schtasks.exe", "/Delete", "/F", "/TN", webServiceTaskName).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "service: schtasks /Delete failed: %v\n%s\n", err, out)
		return 1
	}
	_ = os.Remove(serviceUnitPath(spec))
	fmt.Printf("removed task %s\n", webServiceTaskName)
	return 0
}

// daemonServiceRunning reports whether the node daemon's logon task is active,
// so `panda service status` can say whether the console owns or borrows the
// node row.
func daemonServiceRunning() bool {
	out, err := exec.Command("schtasks.exe", "/Query", "/TN", install.NodeTaskName, "/FO", "LIST").Output()
	return err == nil && strings.Contains(string(out), "Running")
}
