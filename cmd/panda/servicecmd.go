// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda service` registers the web console as a user-level OS service so the
// panel survives the terminal, SSH sessions and reboots. The daemon already
// has its own units from the installers; this command exists because the
// console did not, and a foreground `panda web` was the only way to open it.
//
// Everything here is user-scoped (LaunchAgent, systemd --user, a logon task):
// no elevation, no system directory writes. The unit runs `panda web
// -no-browser`, which owns the node when no daemon holds the identity lock —
// the same split the manual command has.

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/webui/panel"
)

const (
	webServiceLabel    = "com.openpanda.web"
	webServiceUnitName = "openpanda-web.service"
	webServiceTaskName = "OpenPandaWeb"
	webServiceWrapper  = "panda-web.cmd"
	webServiceLogStem  = "web.log"
	defaultServicePath = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
)

// webServiceSpec is everything a unit renderer needs; pure so tests can pin
// the generated text without touching the user's launchd/systemd state.
type webServiceSpec struct {
	Exe      string
	Args     []string
	Home     string
	Path     string
	LogPath  string
	Label    string
	UnitName string
}

func (s webServiceSpec) programArgs() []string {
	return append([]string{s.Exe}, s.Args...)
}

// serviceSpec builds the spec for this machine. exe is the resolved binary —
// not the ~/.local/bin symlink — so an installer swapping the symlink target
// does not leave the service pointing at a stale file.
func serviceSpec(lan bool) (webServiceSpec, error) {
	exe, err := os.Executable()
	if err != nil {
		return webServiceSpec{}, err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return webServiceSpec{}, err
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = defaultServicePath
	}
	// launchd/systemd hand a minimal PATH; prepend the conventional per-user
	// bins so agent CLIs resolve inside the console-owned node exactly as they
	// do in an interactive shell.
	for _, dir := range []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin"} {
		if !containsPath(path, dir) {
			path = dir + string(os.PathListSeparator) + path
		}
	}
	args := []string{"web", "-no-browser"}
	if lan {
		args = append(args, "-lan")
	}
	logPath := filepath.Join(serviceLogDir(home), webServiceLogStem)
	return webServiceSpec{
		Exe: exe, Args: args, Home: home, Path: path, LogPath: logPath,
		Label: webServiceLabel, UnitName: webServiceUnitName,
	}, nil
}

func containsPath(list, want string) bool {
	for _, p := range strings.Split(list, string(os.PathListSeparator)) {
		if p == want {
			return true
		}
	}
	return false
}

func serviceLogDir(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "OpenPanda")
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "openpanda-data")
		}
	}
	return filepath.Join(home, ".local", "state", "openpanda")
}

func serviceUnitPath(spec webServiceSpec) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(spec.Home, "Library", "LaunchAgents", spec.Label+".plist")
	case "windows":
		// The install prefix may be read-only (Program Files); the wrapper
		// lives beside the log in the per-user data dir instead.
		return filepath.Join(serviceLogDir(spec.Home), webServiceWrapper)
	default:
		return filepath.Join(spec.Home, ".config", "systemd", "user", spec.UnitName)
	}
}

// renderLaunchdPlist writes the LaunchAgent. StandardOutPath and
// StandardErrorPath share one append-only file: launchd has no journal.
func renderLaunchdPlist(spec webServiceSpec) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writePlistKey(&b, "Label", spec.Label)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range spec.programArgs() {
		b.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	writePlistKey(&b, "WorkingDirectory", spec.Home)
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	writePlistKey(&b, "PATH", spec.Path)
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	writePlistKey(&b, "ProcessType", "Background")
	writePlistKey(&b, "StandardOutPath", spec.LogPath)
	writePlistKey(&b, "StandardErrorPath", spec.LogPath)
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String())
}

func writePlistKey(b *strings.Builder, key, value string) {
	b.WriteString("\t<key>" + xmlEscape(key) + "</key>\n")
	b.WriteString("\t<string>" + xmlEscape(value) + "</string>\n")
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// renderSystemdUnit writes a user unit. The console owns the node row when no
// daemon is running, so it must outlive the login shell: no session scope, no
// TTY.
func renderSystemdUnit(spec webServiceSpec) []byte {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=OpenPanda web console\n")
	b.WriteString("After=network-online.target\nWants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + systemdQuote(spec.programArgs()) + "\n")
	b.WriteString("WorkingDirectory=" + systemdQuotePath(spec.Home) + "\n")
	b.WriteString("Environment=" + systemdQuote([]string{"PATH=" + spec.Path}) + "\n")
	b.WriteString("Restart=on-failure\nRestartSec=5\n")
	b.WriteString("StandardOutput=append:" + systemdQuotePath(spec.LogPath) + "\n")
	b.WriteString("StandardError=append:" + systemdQuotePath(spec.LogPath) + "\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return []byte(b.String())
}

// systemdQuote renders an argv as one ExecStart value. systemd splits on
// whitespace unless a word is quoted; paths with spaces are the reason this is
// not a plain strings.Join.
func systemdQuote(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, systemdQuotePath(a))
	}
	return strings.Join(parts, " ")
}

// systemdQuotePath quotes a single argument, doubling backslashes and
// escaping an embedded quote, per systemd.syntax.
func systemdQuotePath(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	if strings.ContainsAny(s, " \t\"'\\") || s == "" {
		return `"` + s + `"`
	}
	return s
}

// renderWindowsWrapper is the .cmd schtasks runs. A wrapper file exists
// because schtasks re-quotes /TR and drops inner quote pairs; a command line
// with a path and a redirect cannot survive that round trip.
func renderWindowsWrapper(spec webServiceSpec) []byte {
	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString("rem Generated by `panda service install` — the logon task runs this\r\n")
	b.WriteString("rem wrapper so stdout/stderr land in the console log.\r\n")
	b.WriteString(`"` + spec.Exe + `"`)
	for _, a := range spec.Args {
		b.WriteString(` "` + strings.ReplaceAll(a, `"`, `\"`) + `"`)
	}
	b.WriteString(` >> "` + spec.LogPath + `" 2>&1` + "\r\n")
	return []byte(b.String())
}

// consoleURL is the link a user can open, with the persisted token.
func consoleURL(token string) string {
	host := "127.0.0.1"
	port := "7840"
	if cfg, err := config.Load(config.ResolvePath(cliConfigPath)); err == nil {
		if addr := cfg.Network.PanelAddr; addr != "" {
			if h, p, err := splitHostPort(addr); err == nil {
				if h != "" && h != "0.0.0.0" && h != "::" {
					host = h
				}
				if p != "" {
					port = p
				}
			}
		}
	}
	u := url.URL{Scheme: "http", Host: host + ":" + port}
	if token != "" {
		u.RawQuery = url.Values{"token": {token}}.Encode()
	}
	return u.String()
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", nil
	}
	host := strings.Trim(addr[:i], "[]")
	return host, addr[i+1:], nil
}

// ensurePanelToken persists a stable Bearer token when the config has none, so
// a service restart keeps the same console URL instead of stranding every open
// tab at the gate.
func ensurePanelToken() (string, error) {
	path := config.ResolvePath(cliConfigPath)
	if path == "" {
		return "", errors.New("no config path resolved")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return "", err
	}
	if cfg.Network.PanelToken != "" {
		return cfg.Network.PanelToken, nil
	}
	token := panel.NewToken()
	if err := config.UpdateSectionField(path, []string{"network"}, "panel_token", token); err != nil {
		return "", err
	}
	return token, nil
}

func runService(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: panda service install|status|restart|uninstall [--lan] [--dry-run]")
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("service "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	lan := fs.Bool("lan", false, "bind the console to all interfaces")
	dry := fs.Bool("dry-run", false, "print the unit instead of installing it")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	spec, err := serviceSpec(*lan)
	if err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1
	}
	switch sub {
	case "install":
		return serviceInstall(spec, *dry)
	case "status":
		return serviceStatus(spec)
	case "restart":
		return serviceRestart(spec)
	case "uninstall":
		return serviceUninstall(spec)
	default:
		fmt.Fprintf(os.Stderr, "service: unknown subcommand %q\n", sub)
		return 2
	}
}
