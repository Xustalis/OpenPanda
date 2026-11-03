// SPDX-License-Identifier: AGPL-3.0-or-later

// Package devserver runs a project's own development server and exposes it to
// the console through a reverse proxy that speaks the whole protocol the app
// expects: HTTP methods and bodies, cookies, redirects, streaming and
// WebSockets. It is the live-preview counterpart to the artifact preview
// origin: a packed archive shows what a task produced, while this shows what
// the project's dev server is serving right now, HMR included.
//
// A project opts in with .panda/dev.json in its work directory:
//
//	{
//	  "command": "npm run dev",
//	  "url": "http://127.0.0.1:5173",
//	  "auto_start": true,
//	  "ready_timeout_s": 60
//	}
//
// The target must be loopback: the command runs on this node, and a public or
// LAN address here would turn the proxy into a general-purpose fetcher. The
// proxy itself listens on a fresh port per project, so root-absolute asset
// URLs and the dev server's own WebSocket work without rewriting: the port is
// the origin, and a per-start token is exchanged for an HttpOnly cookie before
// the app is served.
package devserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ConfigFile is the project-level opt-in, relative to the project work dir.
const ConfigFile = ".panda/dev.json"

// DefaultReadyTimeout is how long a start waits for the dev server to answer.
const DefaultReadyTimeout = 60

// MaxReadyTimeout bounds a config's own patience.
const MaxReadyTimeout = 300

// Config is one project's dev-server declaration.
type Config struct {
	// Command is run through the platform shell in the project work dir.
	Command string `json:"command"`
	// URL is the loopback address the dev server serves. The reverse proxy
	// targets exactly this; the command must bring the server up there.
	URL string `json:"url"`
	// Cwd, when set, is where the command runs. Relative paths stay under the
	// work dir.
	Cwd string `json:"cwd,omitempty"`
	// AutoStart opens the preview after a task settles without asking.
	AutoStart bool `json:"auto_start"`
	// ReadyTimeoutS bounds the readiness wait; zero uses the default.
	ReadyTimeoutS int `json:"ready_timeout_s,omitempty"`
}

// ErrNotConfigured is returned by LoadConfig when the project has no
// .panda/dev.json. It is not a failure: most projects have no dev server.
var ErrNotConfigured = errors.New("devserver: project has no " + ConfigFile)

// LoadConfig reads and validates a project's dev declaration. The second
// return is the absolute working directory the command runs in.
func LoadConfig(workDir string) (Config, string, error) {
	var cfg Config
	if workDir == "" {
		return cfg, "", ErrNotConfigured
	}
	path := filepath.Join(workDir, ConfigFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, "", ErrNotConfigured
		}
		return cfg, "", fmt.Errorf("devserver: read %s: %w", ConfigFile, err)
	}
	// A config written by a person, not a machine: tolerate comments? JSON
	// has none, and accepting a near-JSON dialect would surprise editors.
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, "", fmt.Errorf("devserver: parse %s: %w", ConfigFile, err)
	}
	if err := cfg.normalize(workDir); err != nil {
		return cfg, "", err
	}
	dir := workDir
	if cfg.Cwd != "" {
		dir = cfg.Cwd
	}
	return cfg, dir, nil
}

// normalize validates the declaration and resolves the working directory.
func (c *Config) normalize(workDir string) error {
	c.Command = strings.TrimSpace(c.Command)
	if c.Command == "" {
		return errors.New("devserver: command is required")
	}
	raw := strings.TrimSpace(c.URL)
	if raw == "" {
		return errors.New("devserver: url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("devserver: url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("devserver: url must be http or https (got %q)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if !isLoopbackHost(host) {
		return fmt.Errorf("devserver: url must point at loopback (got %q); the command runs on this node", u.Host)
	}
	if u.Port() == "" {
		return errors.New("devserver: url must name a port")
	}
	// The listener's root is the app's root. A base path would have to be
	// stripped from every route, asset and WebSocket path the dev server
	// emits; requiring "/" keeps the proxy honest.
	if p := strings.TrimRight(u.Path, "/"); p != "" {
		return errors.New("devserver: url must serve at the root (no path)")
	}
	u.Path = ""
	// A fragment never reaches a server; keep it out of the proxy target.
	u.Fragment = ""
	c.URL = strings.TrimRight(u.String(), "/")
	if c.ReadyTimeoutS <= 0 {
		c.ReadyTimeoutS = DefaultReadyTimeout
	}
	if c.ReadyTimeoutS > MaxReadyTimeout {
		c.ReadyTimeoutS = MaxReadyTimeout
	}
	if c.Cwd == "" {
		return nil
	}
	dir := c.Cwd
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workDir, dir)
	}
	dir = filepath.Clean(dir)
	root := filepath.Clean(workDir)
	if dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return errors.New("devserver: cwd leaves the project work dir")
	}
	c.Cwd = dir
	return nil
}

// isLoopbackHost reports whether a host names this machine. The dev server is
// started here; anything else would make the proxy reach a network this node
// does not own.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
