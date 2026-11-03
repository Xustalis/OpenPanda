// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State is where one project's dev server stands.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateFailed   State = "failed"
)

// Status is the console's whole view of one project's dev server. URL is built
// per request host: the listener follows the console's reach, so the address a
// LAN browser must use is not the address a loopback browser sees.
type Status struct {
	Project       string `json:"project"`
	Configured    bool   `json:"configured"`
	State         State  `json:"state"`
	Command       string `json:"command,omitempty"`
	Target        string `json:"target,omitempty"`
	URL           string `json:"url,omitempty"`
	AutoStart     bool   `json:"auto_start,omitempty"`
	ReadyTimeoutS int    `json:"ready_timeout_s,omitempty"`
	Error         string `json:"error,omitempty"`
	Log           string `json:"log,omitempty"`
	Since         int64  `json:"since,omitempty"`
}

// Options configure a Manager.
type Options struct {
	// Host is the interface the per-project listeners bind. It follows the
	// console: loopback for a loopback console, all interfaces when the console
	// was opened to the LAN. Empty means loopback.
	Host string
	// Resolve names the work dir of a project. nil resolves nothing.
	Resolve func(project string) (workDir string, ok bool)
	Logger  *slog.Logger
	// ShellCommand overrides the platform shell for tests.
	ShellCommand func(command string) (string, []string)
}

// Manager owns every project's dev server, its listener and its status.
type Manager struct {
	opts Options

	mu     sync.Mutex
	slots  map[string]*slot
	closed bool
}

type slot struct {
	project string
	workDir string
	cfg     Config

	state State
	err   string
	since time.Time
	log   *ringLog

	target *url.URL
	token  string
	ln     net.Listener
	srv    *http.Server
	port   int
	proxy  *httputil.ReverseProxy

	cancel context.CancelFunc
	cmd    *exec.Cmd
}

// Start creates a Manager. It starts no listeners and no processes: those
// appear when a project's dev server is started.
func Start(o Options) *Manager {
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.ShellCommand == nil {
		o.ShellCommand = shellCommand
	}
	return &Manager{opts: o, slots: make(map[string]*slot)}
}

// Status reports one project without starting anything. requestHost is the
// host the console request arrived on, used to build a reachable preview URL.
func (m *Manager) Status(project, requestHost string) Status {
	m.mu.Lock()
	s := m.slots[project]
	if s != nil && s.state != StateStopped {
		st := s.status(requestHost)
		m.mu.Unlock()
		return st
	}
	m.mu.Unlock()

	// No running slot: the config is the only source, and it is cheap to read.
	workDir, ok := m.resolve(project)
	if !ok {
		return Status{Project: project}
	}
	cfg, _, err := LoadConfig(workDir)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			return Status{Project: project, State: StateStopped}
		}
		return Status{Project: project, Configured: true, State: StateFailed, Error: err.Error()}
	}
	return Status{
		Project:       project,
		Configured:    true,
		State:         StateStopped,
		Command:       cfg.Command,
		Target:        cfg.URL,
		AutoStart:     cfg.AutoStart,
		ReadyTimeoutS: cfg.ReadyTimeoutS,
	}
}

// StartProject starts a project's dev server if it is not already up. It
// returns as soon as the process and listener exist; the readiness probe runs
// in the background and lands in Status.
func (m *Manager) StartProject(project string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Status{Project: project}, errors.New("devserver: manager is closed")
	}
	if s := m.slots[project]; s != nil {
		switch s.state {
		case StateStarting, StateReady:
			return s.status(""), nil
		default:
			m.closeSlotLocked(s)
			delete(m.slots, project)
		}
	}
	workDir, ok := m.resolve(project)
	if !ok {
		return Status{Project: project}, fmt.Errorf("devserver: project %q has no work dir", project)
	}
	cfg, dir, err := LoadConfig(workDir)
	if err != nil {
		return Status{Project: project}, err
	}
	target, err := url.Parse(cfg.URL)
	if err != nil {
		return Status{Project: project}, fmt.Errorf("devserver: url: %w", err)
	}
	token, err := newToken()
	if err != nil {
		return Status{Project: project}, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(m.opts.Host, "0"))
	if err != nil {
		return Status{Project: project}, fmt.Errorf("devserver: listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s := &slot{
		project: project,
		workDir: workDir,
		cfg:     cfg,
		state:   StateStarting,
		since:   time.Now(),
		log:     newRingLog(64 << 10),
		target:  target,
		token:   token,
		ln:      ln,
		port:    port,
	}
	s.proxy = m.newProxy(s)
	mux := http.NewServeMux()
	mux.HandleFunc("/__openpanda/enter", m.serveEnter(s))
	mux.Handle("/", s.authCookie(s.proxy))
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	m.slots[project] = s
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.opts.Logger.Warn("devserver: serve", "project", project, "err", err)
		}
	}()

	// The agent that built the site often leaves its dev server running, and
	// the project's config names the port. Adopt that server instead of
	// launching a second command that would lose the bind race and report the
	// project failed while the real server is answering.
	if probeDevServer(cfg.URL) == nil {
		s.state = StateReady
		s.since = time.Now()
		go m.watchAdopted(s)
		return s.status(""), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	name, args := m.opts.ShellCommand(cfg.Command)
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the project's own declaration
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.Stdout = s.log
	cmd.Stderr = s.log
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		cancel()
		_ = s.srv.Close()
		s.state = StateFailed
		s.err = fmt.Sprintf("start %q: %v", cfg.Command, err)
		return s.status(""), nil
	}
	s.cmd = cmd
	// The waiter is the only place Wait is called. A server that exits after
	// it became ready is a failure the reader must see, not a silent stop.
	go func() {
		werr := cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.slots[project] != s || s.state == StateStopped {
			return
		}
		s.state = StateFailed
		if werr != nil {
			s.err = fmt.Sprintf("dev server exited: %v", werr)
		} else {
			s.err = "dev server exited"
		}
		m.closeSlotLocked(s)
	}()
	go m.waitReady(s)
	return s.status(""), nil
}

// waitReady polls the target until it answers, the process dies, or the
// declaration's patience runs out. A dev server that never answers is killed:
// leaving it running would hold the port and mislead the next start.
func (m *Manager) waitReady(s *slot) {
	deadline := time.Now().Add(time.Duration(s.cfg.ReadyTimeoutS) * time.Second)
	for {
		m.mu.Lock()
		if m.slots[s.project] != s || s.state != StateStarting {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		if probeDevServer(s.cfg.URL) == nil {
			m.mu.Lock()
			if m.slots[s.project] == s && s.state == StateStarting {
				s.state = StateReady
				s.since = time.Now()
				s.err = ""
			}
			m.mu.Unlock()
			return
		}
		if time.Now().After(deadline) {
			m.mu.Lock()
			if m.slots[s.project] == s && s.state == StateStarting {
				s.state = StateFailed
				s.err = fmt.Sprintf("dev server did not answer at %s within %ds", s.cfg.URL, s.cfg.ReadyTimeoutS)
				m.closeSlotLocked(s)
			}
			m.mu.Unlock()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// probeDevServer reports whether something answers at the project's dev URL.
// Any HTTP response means the server is up; an error means it is not (the
// server may still be compiling, so callers retry).
func probeDevServer(raw string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(raw)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// watchAdopted keeps an adopted server's status honest. The manager did not
// start it, so no process waiter exists; two failed probes in a row turn the
// slot failed rather than leaving a dead preview marked ready.
func (m *Manager) watchAdopted(s *slot) {
	failures := 0
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		if m.slots[s.project] != s || s.state != StateReady {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		if err := probeDevServer(s.cfg.URL); err != nil {
			failures++
			if failures < 2 {
				continue
			}
			m.mu.Lock()
			if m.slots[s.project] == s && s.state == StateReady {
				s.state = StateFailed
				s.err = "dev server stopped answering at " + s.cfg.URL
			}
			m.mu.Unlock()
			return
		}
		failures = 0
	}
}

// Origins lists the frameable origins of ready dev servers as this request
// host can reach them. The console's CSP admits exactly these, because the
// listener ports are ephemeral and a wildcard would admit more than previews.
func (m *Manager) Origins(requestHost string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.slots))
	for _, s := range m.slots {
		if s.state != StateReady || s.ln == nil {
			continue
		}
		host := hostFor(requestHost, s.ln.Addr())
		if host == "" {
			continue
		}
		out = append(out, "http://"+net.JoinHostPort(host, strconv.Itoa(s.port)))
	}
	sort.Strings(out)
	return out
}

// StopProject stops a project's dev server. It is idempotent.
func (m *Manager) StopProject(project string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[project]; s != nil {
		m.closeSlotLocked(s)
		delete(m.slots, project)
	}
	// Report what the project's declaration says now, so the console keeps a
	// Start button after a stop.
	return Status{Project: project}
}

// Close stops every dev server and listener.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for name, s := range m.slots {
		m.closeSlotLocked(s)
		delete(m.slots, name)
	}
}

func (m *Manager) resolve(project string) (string, bool) {
	if m.opts.Resolve == nil {
		return "", false
	}
	return m.opts.Resolve(project)
}

// closeSlotLocked ends a slot's process and listener. The caller holds m.mu.
func (m *Manager) closeSlotLocked(s *slot) {
	if s.cancel != nil {
		s.cancel()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		killProcessGroup(s.cmd)
	}
	if s.srv != nil {
		_ = s.srv.Close()
	}
	s.state = StateStopped
}

func (s *slot) status(requestHost string) Status {
	st := Status{
		Project:       s.project,
		Configured:    true,
		State:         s.state,
		Command:       s.cfg.Command,
		Target:        s.cfg.URL,
		AutoStart:     s.cfg.AutoStart,
		ReadyTimeoutS: s.cfg.ReadyTimeoutS,
		Error:         s.err,
	}
	if !s.since.IsZero() {
		st.Since = s.since.Unix()
	}
	if s.state == StateReady {
		st.URL = entryURL(requestHost, s)
	}
	// The log is a diagnosis, not a stream: send it only when a start or the
	// server itself failed, so the console's 3s poll does not carry 8 KB of
	// output per project per tick.
	if s.log != nil && s.state == StateFailed {
		st.Log = s.log.Tail(8 << 10)
	}
	return st
}

// entryURL builds the address the console frames. The token travels once in
// the entry link; the listener turns it into an HttpOnly cookie so the app's
// own root-absolute requests and WebSocket handshakes authenticate too.
func entryURL(requestHost string, s *slot) string {
	host := hostFor(requestHost, s.ln.Addr())
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.port)) +
		"/__openpanda/enter?token=" + url.QueryEscape(s.token)
}

// hostFor picks the address a browser can use. A listener bound to loopback
// keeps its own host; a wildcard listener inherits the host the console was
// reached on, exactly like the artifact preview origin.
func hostFor(requestHost string, addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP == nil {
		return "127.0.0.1"
	}
	if !tcp.IP.IsUnspecified() {
		return tcp.IP.String()
	}
	if host, _, err := net.SplitHostPort(requestHost); err == nil && host != "" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
			return host
		}
	}
	return "127.0.0.1"
}

// newToken is the capability in a preview entry link.
func newToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("devserver: token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ringLog keeps the tail of a dev server's output: enough to explain a failed
// start, bounded so a chatty server cannot grow the manager's memory.
type ringLog struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newRingLog(max int) *ringLog { return &ringLog{max: max} }

func (l *ringLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if len(l.buf) > l.max {
		l.buf = append([]byte(nil), l.buf[len(l.buf)-l.max:]...)
	}
	return len(p), nil
}

func (l *ringLog) Tail(n int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.buf) {
		n = len(l.buf)
	}
	out := string(l.buf[len(l.buf)-n:])
	// Trim a partial first line so the tail starts somewhere readable.
	if i := strings.IndexByte(out, '\n'); i >= 0 && i+1 < len(out) {
		out = out[i+1:]
	}
	return strings.TrimSpace(out)
}
