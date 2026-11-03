// SPDX-License-Identifier: AGPL-3.0-or-later

package browser

// The browser host: one long-lived process per node that owns the headless
// Chrome, so an agent's tool calls — across MCP processes, CLI invocations or
// a single long run — all act on the same tab. A short-lived MCP server that
// launched Chrome itself would lose the page between calls; this server also
// keeps the process tree and the browser profile out of every caller.
//
// It listens on loopback with a token written to <state>/host.json (0600),
// exits after an idle period, and holds a file lock so only one host owns the
// browser. Callers that find no healthy host spawn one and wait for it.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/fslock"
)

// StateDirFor names the node's browser state directory. The caller passes the
// data directory that also holds panda.db.
func StateDirFor(dataDir string) string {
	return filepath.Join(dataDir, "browser")
}

// HostOptions configure ServeHost.
type HostOptions struct {
	StateDir string
	// IdleTimeout ends the host (and Chrome) after a quiet period. Zero uses
	// 15 minutes.
	IdleTimeout time.Duration
	// IdleCheck is how often idleness is tested. Zero uses 30 seconds.
	IdleCheck time.Duration
	// ChromePath overrides discovery (OPENPANDA_CHROME also works).
	ChromePath string
	Logger     *slog.Logger
}

// Host is the local control server.
type Host struct {
	opts    HostOptions
	state   string
	lock    string
	token   string
	logger  *slog.Logger
	mgr     *Manager
	lastUse atomic.Int64
	shut    chan struct{}
	once    sync.Once
}

// ServeHost runs until ctx is canceled, the host is closed by a `close` op,
// or it has been idle for the configured period.
func ServeHost(ctx context.Context, o HostOptions) error {
	if o.StateDir == "" {
		return errors.New("browser: host needs a state directory")
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 15 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		return fmt.Errorf("browser: state dir: %w", err)
	}
	lockPath := filepath.Join(o.StateDir, "host.lock")
	release, err := fslock.Acquire(lockPath, false)
	if err != nil {
		if errors.Is(err, fslock.ErrHeld) {
			// Another host owns the browser; this process simply exits.
			return nil
		}
		return fmt.Errorf("browser: host lock: %w", err)
	}
	defer release()

	token, err := hostToken()
	if err != nil {
		return err
	}
	h := &Host{
		opts:   o,
		state:  filepath.Join(o.StateDir, "host.json"),
		lock:   lockPath,
		token:  token,
		logger: o.Logger,
		shut:   make(chan struct{}),
	}
	h.mgr = Start(Options{ChromePath: o.ChromePath, DataDir: o.StateDir, Logger: o.Logger})
	defer h.mgr.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("browser: host listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := h.writeState(port); err != nil {
		_ = ln.Close()
		return err
	}
	defer h.removeState()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.authed(func(w http.ResponseWriter, r *http.Request) {
		writeHostJSON(w, http.StatusOK, map[string]any{"ok": true, "pid": os.Getpid()})
	}))
	mux.HandleFunc("POST /op", h.authed(h.handleOp))
	srv := &http.Server{
		Handler:           h.track(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	h.lastUse.Store(time.Now().UnixNano())
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.logger.Warn("browser host: serve", "err", err)
		}
	}()

	go h.idleLoop(srv)
	select {
	case <-ctx.Done():
	case <-h.shut:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	return nil
}

func (h *Host) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.lastUse.Store(time.Now().UnixNano())
		next.ServeHTTP(w, r)
	})
}

func (h *Host) idleLoop(srv *http.Server) {
	every := h.opts.IdleCheck
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-h.shut:
			return
		case <-t.C:
			last := time.Unix(0, h.lastUse.Load())
			if time.Since(last) > h.opts.IdleTimeout {
				h.shutdown()
				return
			}
		}
	}
}

func (h *Host) shutdown() {
	h.once.Do(func() { close(h.shut) })
}

func (h *Host) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		hdr := r.Header.Get("Authorization")
		got := ""
		if len(hdr) > len(prefix) && subtle.ConstantTimeCompare([]byte(hdr[:len(prefix)]), []byte(prefix)) == 1 {
			got = hdr[len(prefix):]
		}
		gotSum := sha256.Sum256([]byte(got))
		wantSum := sha256.Sum256([]byte(h.token))
		if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
			writeHostJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (h *Host) handleOp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op   string         `json:"op"`
		Args map[string]any `json:"args"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeHostJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Op == "" {
		writeHostJSON(w, http.StatusBadRequest, map[string]any{"error": "op is required"})
		return
	}
	if req.Op == "close" {
		h.mgr.Close()
		writeHostJSON(w, http.StatusOK, map[string]any{"result": Result{Message: "closed"}})
		h.shutdown()
		return
	}
	res, err := dispatch(r.Context(), h.mgr, req.Op, req.Args)
	if err != nil {
		writeHostJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeHostJSON(w, http.StatusOK, map[string]any{"result": res})
}

func dispatch(ctx context.Context, m *Manager, op string, args map[string]any) (Result, error) {
	switch op {
	case "ping":
		return Result{Message: "ok"}, nil
	case "open":
		return m.Open(argString(args, "url"), argString(args, "viewport"))
	case "snapshot":
		return m.Snapshot()
	case "screenshot":
		return m.Screenshot(argBool(args, "full"))
	case "click":
		return m.Click(argString(args, "selector"), argFloat(args, "x"), argFloat(args, "y"))
	case "type":
		return m.Type(argString(args, "selector"), argString(args, "text"),
			argBool(args, "clear"), argBool(args, "submit"))
	case "press":
		return m.Press(argString(args, "key"))
	case "scroll":
		return m.Scroll(argFloat(args, "dx"), argFloat(args, "dy"))
	case "eval":
		return m.Eval(argString(args, "expression"))
	case "console":
		return m.Console(argInt(args, "limit")), nil
	case "network":
		return m.Network(argInt(args, "limit")), nil
	case "viewport":
		return m.Viewport(argString(args, "viewport"))
	default:
		return Result{}, fmt.Errorf("browser: unknown op %q", op)
	}
}

// hostState is what a caller reads to reach a running host.
type hostState struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
	At    int64  `json:"at"`
}

func (h *Host) writeState(port int) error {
	st := hostState{Port: port, Token: h.token, PID: os.Getpid(), At: time.Now().Unix()}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := h.state + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("browser: write state: %w", err)
	}
	if err := os.Rename(tmp, h.state); err != nil {
		return fmt.Errorf("browser: publish state: %w", err)
	}
	return nil
}

func (h *Host) removeState() {
	raw, err := os.ReadFile(h.state)
	if err != nil {
		return
	}
	var st hostState
	if json.Unmarshal(raw, &st) == nil && st.Token == h.token {
		_ = os.Remove(h.state)
	}
}

func hostToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("browser: host token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func writeHostJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- client ---------------------------------------------------------------

// Client reaches (and if needed starts) the node's browser host.
type Client struct {
	stateDir string
	exe      string

	mu    sync.Mutex
	base  string
	token string
}

// NewClient builds a client for the host rooted at stateDir. exe is the panda
// binary used to spawn a missing host; empty resolves os.Executable.
func NewClient(stateDir, exe string) *Client {
	if exe == "" {
		if p, err := os.Executable(); err == nil {
			exe = p
		}
	}
	return &Client{stateDir: stateDir, exe: exe}
}

// OpError is a tool-level failure reported by the host — a bad selector, a
// page that is not open, Chrome missing. It is an answer, not a broken
// transport, so the client returns it instead of replacing the host.
type OpError string

func (e OpError) Error() string { return string(e) }

// Do runs one operation, starting the host first when none is healthy.
func (c *Client) Do(ctx context.Context, op string, args map[string]any) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		base, token, err := c.endpoint(ctx)
		if err != nil {
			return Result{}, err
		}
		res, err := c.call(ctx, base, token, op, args)
		if err == nil {
			return res, nil
		}
		var opErr OpError
		if errors.As(err, &opErr) {
			return Result{}, err
		}
		// A host that died between the health check and the call is replaced
		// once; a real operation failure is returned as-is.
		c.base, c.token = "", ""
		if attempt == 1 {
			return Result{}, err
		}
	}
	return Result{}, errors.New("browser: host unavailable")
}

func (c *Client) endpoint(ctx context.Context) (string, string, error) {
	if c.base != "" && c.healthy(ctx, c.base, c.token) {
		return c.base, c.token, nil
	}
	c.base, c.token = "", ""
	st, err := c.readState()
	if err == nil {
		base := "http://127.0.0.1:" + strconv.Itoa(st.Port)
		if c.healthy(ctx, base, st.Token) {
			c.base, c.token = base, st.Token
			return base, st.Token, nil
		}
		// Leave the state file alone: a live host that is briefly slow still
		// owns the lock, and a replacement host writes its own state over a
		// stale file anyway.
	}
	if err := c.spawn(); err != nil {
		return "", "", err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, err := c.readState()
		if err == nil {
			base := "http://127.0.0.1:" + strconv.Itoa(st.Port)
			if c.healthy(ctx, base, st.Token) {
				c.base, c.token = base, st.Token
				return base, st.Token, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", "", errors.New("browser: the browser host did not start (see " +
		filepath.Join(c.stateDir, "host.log") + ")")
}

func (c *Client) readState() (hostState, error) {
	var st hostState
	raw, err := os.ReadFile(filepath.Join(c.stateDir, "host.json"))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, err
	}
	if st.Port == 0 || st.Token == "" {
		return st, errors.New("browser: malformed host state")
	}
	return st, nil
}

func (c *Client) healthy(ctx context.Context, base, token string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (c *Client) call(ctx context.Context, base, token, op string, args map[string]any) (Result, error) {
	body, err := json.Marshal(map[string]any{"op": op, "args": args})
	if err != nil {
		return Result{}, err
	}
	// A browser launch and a first navigation can be slow; the ceiling keeps
	// a wedged host from holding an agent's tool call forever.
	rctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, base+"/op", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("browser host: %s", clip(string(raw), 500))
	}
	var out struct {
		Result Result `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("browser host: %w", err)
	}
	if out.Error != "" {
		return Result{}, OpError(out.Error)
	}
	return out.Result, nil
}

func (c *Client) spawn() error {
	if c.exe == "" {
		return errors.New("browser: cannot start the host (no panda executable)")
	}
	logPath := filepath.Join(c.stateDir, "host.log")
	if err := os.MkdirAll(c.stateDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("browser: host log: %w", err)
	}
	cmd := exec.Command(c.exe, "browser", "host", "--state", c.stateDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("browser: start host: %w", err)
	}
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return nil
}

// argString/argFloat/argInt/argBool tolerate the JSON shapes an MCP client
// sends (numbers arrive as float64, integers sometimes as json.Number).
func argString(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func argFloat(args map[string]any, key string) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

func argInt(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	}
	return 0
}

func argBool(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}
