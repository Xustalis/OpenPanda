// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/install"
)

// serviceInstall writes the unit and asks the platform's user service manager
// to load it. macOS uses a LaunchAgent; everything else is assumed to be a
// systemd --user host (the Linux installer's contract).
func serviceInstall(spec webServiceSpec, dry bool) int {
	path := serviceUnitPath(spec)
	var content []byte
	if runtime.GOOS == "darwin" {
		content = renderLaunchdPlist(spec)
	} else {
		content = renderSystemdUnit(spec)
	}
	if dry {
		fmt.Printf("unit: %s\n\n%s", path, content)
		return 0
	}
	token, terr := ensurePanelToken()
	if terr != nil {
		fmt.Fprintf(os.Stderr, "service: panel token will be ephemeral: %v\n", terr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1
	}
	logDir := filepath.Dir(spec.LogPath)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1
	}
	_ = os.Chmod(logDir, 0o700)
	// The service's stdout carries the console URL with its bearer token.
	// Create the log 0600 up front so an inherited umask cannot make it
	// world-readable — `panda web` already does this for its own daemon log.
	if lf, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		_ = lf.Close()
		_ = os.Chmod(spec.LogPath, 0o600)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1
	}
	if runtime.GOOS == "darwin" {
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+spec.Label).Run()
		if out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
			// Older macOS releases only know load -w.
			if out2, err2 := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err2 != nil {
				fmt.Fprintf(os.Stderr, "service: launchctl failed: %v\n%s\n%s\n", err, out, out2)
				return 1
			}
		}
	} else {
		if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "service: systemctl daemon-reload failed: %v\n%s\n", err, out)
			return 1
		}
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", spec.UnitName).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "service: systemctl enable failed: %v\n%s\n", err, out)
			return 1
		}
	}
	fmt.Printf("installed %s\n", path)
	fmt.Printf("url   %s\n", consoleURL(token))
	fmt.Printf("log   %s\n", spec.LogPath)
	return 0
}

func serviceStatus(spec webServiceSpec) int {
	path := serviceUnitPath(spec)
	if _, err := os.Stat(path); err != nil {
		fmt.Printf("not installed (%s missing)\n", path)
		return 1
	}
	state := "stopped"
	if runtime.GOOS == "darwin" {
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		if out, err := exec.Command("launchctl", "print", domain+"/"+spec.Label).CombinedOutput(); err == nil &&
			strings.Contains(string(out), "state = running") {
			state = "running"
		}
	} else {
		if out, err := exec.Command("systemctl", "--user", "is-active", spec.UnitName).CombinedOutput(); err == nil &&
			strings.TrimSpace(string(out)) == "active" {
			state = "running"
		}
	}
	fmt.Printf("unit  %s\n", path)
	fmt.Printf("state %s\n", state)
	if token, err := ensurePanelToken(); err == nil {
		fmt.Printf("url   %s\n", consoleURL(token))
	}
	fmt.Printf("log   %s\n", spec.LogPath)
	printNodeRow()
	return 0
}

func serviceRestart(spec webServiceSpec) int {
	if _, err := os.Stat(serviceUnitPath(spec)); err != nil {
		fmt.Fprintln(os.Stderr, "service: not installed; run `panda service install` first")
		return 1
	}
	if runtime.GOOS == "darwin" {
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		if out, err := exec.Command("launchctl", "kickstart", "-k", domain+"/"+spec.Label).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "service: launchctl kickstart failed: %v\n%s\n", err, out)
			return 1
		}
	} else {
		if out, err := exec.Command("systemctl", "--user", "restart", spec.UnitName).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "service: systemctl restart failed: %v\n%s\n", err, out)
			return 1
		}
	}
	fmt.Println("restarted")
	return 0
}

func serviceUninstall(spec webServiceSpec) int {
	path := serviceUnitPath(spec)
	if _, err := os.Stat(path); err != nil {
		fmt.Println("not installed")
		return 0
	}
	if runtime.GOOS == "darwin" {
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+spec.Label).Run()
	} else {
		_ = exec.Command("systemctl", "--user", "disable", "--now", spec.UnitName).Run()
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1
	}
	if runtime.GOOS != "darwin" {
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	fmt.Printf("removed %s\n", path)
	return 0
}

// daemonServiceRunning reports whether the node daemon's own unit is active.
// It distinguishes "the console owns the node row" from "the console borrows
// it from a daemon" in `panda service status`.
func daemonServiceRunning() bool {
	svc := fmt.Sprintf("gui/%d/%s", os.Getuid(), install.NodeServiceLabel)
	if out, err := exec.Command("launchctl", "print", svc).Output(); err == nil &&
		strings.Contains(string(out), "state = running") {
		return true
	}
	if out, err := exec.Command("systemctl", "--user", "is-active", install.NodeServiceUnitName).Output(); err == nil &&
		strings.TrimSpace(string(out)) == "active" {
		return true
	}
	return false
}
