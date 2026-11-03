// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package devserver

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestManagerStatusBeforeStart(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"command":"sleep 30","url":"http://127.0.0.1:5173","auto_start":true}`)
	m := Start(Options{Resolve: func(name string) (string, bool) { return dir, name == "proj" }})
	t.Cleanup(m.Close)

	st := m.Status("proj", "127.0.0.1:7850")
	if !st.Configured || st.State != StateStopped || !st.AutoStart {
		t.Fatalf("status = %+v", st)
	}
	if got := m.Status("other", "127.0.0.1:7850"); got.Configured {
		t.Fatalf("unknown project reported configured: %+v", got)
	}
}

func waitStatus(t *testing.T, m *Manager, project string, want State) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := m.Status(project, "127.0.0.1:7850")
		if st.State == want {
			return st
		}
		if st.State == StateFailed {
			t.Fatalf("failed while waiting for %s: %s\n%s", want, st.Error, st.Log)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("never reached %s", want)
	return Status{}
}

func TestManagerProxiesDevServer(t *testing.T) {
	var assetHit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset.txt":
			assetHit.Store(true)
			_, _ = w.Write([]byte("asset"))
		case "/go":
			w.Header().Set("Location", upstreamURL(r)+"/next")
			w.WriteHeader(http.StatusFound)
		default:
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
			w.Header().Add("Set-Cookie", "sid=abc; Path=/; Domain=localhost; Secure; SameSite=None")
			_, _ = w.Write([]byte("<html><body>ok</body></html>"))
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	writeConfig(t, dir, fmt.Sprintf(`{"command":"sleep 30","url":%q}`, upstream.URL))
	m := Start(Options{Resolve: func(name string) (string, bool) { return dir, name == "proj" }})
	t.Cleanup(m.Close)

	if _, err := m.StartProject("proj"); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitStatus(t, m, "proj", StateReady)
	if !strings.Contains(st.URL, "/__openpanda/enter?token=") {
		t.Fatalf("url = %q", st.URL)
	}
	base := st.URL[:strings.Index(st.URL, "/__openpanda/")]

	// The entry link authenticates once and sets the listener cookie.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noFollow.Get(st.URL)
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("enter status = %d", resp.StatusCode)
	}
	var token string
	for _, c := range resp.Cookies() {
		if c.Name == devCookie {
			token = c.Value
			if !c.HttpOnly {
				t.Fatal("the preview cookie is readable by scripts")
			}
		}
	}
	if token == "" {
		t.Fatal("enter set no cookie")
	}

	cookieReq := func(path string) *http.Request {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		req.AddCookie(&http.Cookie{Name: devCookie, Value: token})
		return req
	}

	// No cookie, no fetch.
	resp, err = http.Get(base + "/")
	if err != nil {
		t.Fatalf("unauth: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request = %d", resp.StatusCode)
	}

	// The app is served with a frameable policy and origin-neutral cookies.
	resp, err = http.DefaultClient.Do(cookieReq("/"))
	if err != nil {
		t.Fatalf("app: %v", err)
	}
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body[:n]), "ok") {
		t.Fatalf("app = %d %q", resp.StatusCode, body[:n])
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options survived: %q", got)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "'none'") || !strings.Contains(csp, "frame-ancestors *") {
		t.Fatalf("csp = %q", csp)
	}
	for _, c := range resp.Header.Values("Set-Cookie") {
		if strings.Contains(c, "Domain=") || strings.Contains(c, "Secure") || strings.Contains(c, "SameSite=None") {
			t.Fatalf("cookie kept an origin attribute: %q", c)
		}
	}

	// A root-absolute asset stays on the proxy root, which is the whole
	// reason the listener owns a port instead of a path prefix.
	resp, err = http.DefaultClient.Do(cookieReq("/asset.txt"))
	if err != nil {
		t.Fatalf("asset: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !assetHit.Load() {
		t.Fatalf("asset = %d, hit = %v", resp.StatusCode, assetHit.Load())
	}

	// A redirect to the dev server's own origin is rewritten onto this one.
	resp, err = noFollow.Do(cookieReq("/go"))
	if err != nil {
		t.Fatalf("redirect: %v", err)
	}
	_ = resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/next" {
		t.Fatalf("Location = %q, want /next", loc)
	}

	stopped := m.StopProject("proj")
	if stopped.Project != "proj" {
		t.Fatalf("stop = %+v", stopped)
	}
	if got := m.Status("proj", "127.0.0.1:7850"); got.State != StateStopped {
		t.Fatalf("after stop: %+v", got)
	}
	// The listener is gone with the server.
	gone := &http.Client{Timeout: 500 * time.Millisecond}
	if resp, err := gone.Get(base + "/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the preview listener survived Stop")
	}
}

// upstreamURL is the address the test handler is reached at. The handler has
// no direct handle to its own server, so derive it from the request Host and
// the URL scheme the test client used.
func upstreamURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// A dev server's HMR channel is a WebSocket. The proxy must forward the
// upgrade with the listener's cookie, or a Vite/Next page renders once and
// never updates.
func TestManagerProxiesWebSocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, data); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	dir := t.TempDir()
	writeConfig(t, dir, fmt.Sprintf(`{"command":"sleep 30","url":%q}`, upstream.URL))
	m := Start(Options{Resolve: func(name string) (string, bool) { return dir, name == "proj" }})
	t.Cleanup(m.Close)
	if _, err := m.StartProject("proj"); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := waitStatus(t, m, "proj", StateReady)

	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Get(st.URL)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	_ = resp.Body.Close()
	var token string
	for _, c := range resp.Cookies() {
		if c.Name == devCookie {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no cookie")
	}
	u, err := url.Parse(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	wsURL := "ws://" + u.Host + "/ws"
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(wsURL, http.Header{
		"Cookie": []string{devCookie + "=" + token},
	})
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "ping" {
		t.Fatalf("echo = %q", data)
	}
}

// A dev server the agent started is adopted rather than duplicated: launching
// the declared command would lose the bind race and mark a working preview
// failed.
func TestManagerAdoptsRunningDevServer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("adopted"))
	}))
	defer upstream.Close()
	dir := t.TempDir()
	writeConfig(t, dir, fmt.Sprintf(`{"command":"sleep 30","url":%q}`, upstream.URL))
	m := Start(Options{Resolve: func(name string) (string, bool) { return dir, name == "proj" }})
	t.Cleanup(m.Close)
	if _, err := m.StartProject("proj"); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitStatus(t, m, "proj", StateReady)
	m.mu.Lock()
	cmd := m.slots["proj"].cmd
	m.mu.Unlock()
	if cmd != nil {
		t.Fatal("adoption spawned the declared command")
	}
}

// The manager also owns the command it starts: stop kills the process group,
// so a second start can bind the port again.
func TestManagerStartsAndStopsCommand(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	dir := t.TempDir()
	cmd := fmt.Sprintf("exec python3 -m http.server %d --bind 127.0.0.1", port)
	writeConfig(t, dir, fmt.Sprintf(`{"command":%q,"url":"http://127.0.0.1:%d"}`, cmd, port))
	m := Start(Options{Resolve: func(name string) (string, bool) { return dir, name == "proj" }})
	t.Cleanup(m.Close)
	if _, err := m.StartProject("proj"); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitStatus(t, m, "proj", StateReady)
	m.mu.Lock()
	owned := m.slots["proj"].cmd
	m.mu.Unlock()
	if owned == nil {
		t.Fatal("the running server was adopted instead of started")
	}
	m.StopProject("proj")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if probeDevServer(fmt.Sprintf("http://127.0.0.1:%d", port)) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the dev server kept answering after stop")
}
