// SPDX-License-Identifier: AGPL-3.0-or-later

package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func TestParseViewport(t *testing.T) {
	mobile, err := ParseViewport("mobile")
	if err != nil || mobile.Width != 390 || !mobile.Mobile || !mobile.Touch {
		t.Fatalf("mobile = %+v, err %v", mobile, err)
	}
	custom, err := ParseViewport("800x600@2")
	if err != nil || custom.Width != 800 || custom.Height != 600 || custom.Scale != 2 {
		t.Fatalf("custom = %+v, err %v", custom, err)
	}
	for _, bad := range []string{"tiny", "0x600", "9000x9000", "800x"} {
		if _, err := ParseViewport(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestNormaliseURL(t *testing.T) {
	if u, err := normaliseURL("example.com/a"); err != nil || u.Scheme != "http" {
		t.Fatalf("bare host = %v, %v", u, err)
	}
	if _, err := normaliseURL("javascript:alert(1)"); err == nil {
		t.Fatal("javascript: was accepted")
	}
	if _, err := normaliseURL(""); err == nil {
		t.Fatal("empty url was accepted")
	}
}

func TestConsoleLogDropsOldest(t *testing.T) {
	l := newConsoleLog(2)
	l.add(ConsoleEntry{Text: "a"})
	l.add(ConsoleEntry{Text: "b"})
	l.add(ConsoleEntry{Text: "c"})
	got := l.tail(0)
	if len(got) != 2 || got[0].Text != "b" || got[1].Text != "c" {
		t.Fatalf("tail = %+v", got)
	}
}

func TestNetLogJoinsRequestAndResponse(t *testing.T) {
	l := newNetLog(4)
	l.request(&network.EventRequestWillBeSent{
		RequestID: "1",
		Request:   &network.Request{Method: "GET", URL: "https://example.test/"},
		Type:      network.ResourceTypeDocument,
	})
	l.response(&network.EventResponseReceived{
		RequestID: "1",
		Response:  &network.Response{Status: 200, MimeType: "text/html"},
	})
	got := l.tail(0)
	if len(got) != 1 || got[0].Status != 200 || got[0].Method != "GET" || got[0].MimeType != "text/html" {
		t.Fatalf("entries = %+v", got)
	}
}

// TestBrowserIntegration drives a real Chrome. It is opt-in because CI images
// may have no browser; run it with OPENPANDA_BROWSER_TEST=1.
func TestBrowserIntegration(t *testing.T) {
	if os.Getenv("OPENPANDA_BROWSER_TEST") != "1" {
		t.Skip("set OPENPANDA_BROWSER_TEST=1 to run the real-browser test")
	}
	if FindChrome() == "" {
		t.Skip("no Chrome/Chromium on this machine")
	}
	page := `<!doctype html><html><head><title>Probe</title></head><body>
<h1>Probe page</h1>
<button id="go" onclick="document.getElementById('out').textContent='clicked'">Go</button>
<p id="out">idle</p>
<label for="name">Name</label><input id="name" />
<script>console.log('hello from probe')</script>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	m := Start(Options{DataDir: t.TempDir()})
	t.Cleanup(m.Close)

	res, err := m.Open(srv.URL, "desktop")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if res.Title != "Probe" || !strings.Contains(res.URL, srv.URL) {
		t.Fatalf("identity = %+v", res)
	}

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(snap.Text, "Probe page") {
		t.Fatalf("text = %q", snap.Text)
	}
	found := false
	for _, c := range snap.Controls {
		if c.Selector == "#go" && c.Role == "button" {
			found = true
		}
	}
	if !found {
		t.Fatalf("controls = %+v", snap.Controls)
	}
	if snap.Tree == "" {
		t.Fatal("no accessibility tree")
	}

	if _, err := m.Click("#go", 0, 0); err != nil {
		t.Fatalf("click: %v", err)
	}
	out, err := m.Eval(`document.getElementById('out').textContent`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got := rawValue(out.Value); !strings.Contains(got, "clicked") {
		t.Fatalf("value = %s", got)
	}

	if _, err := m.Type("#name", "Ada", true, false); err != nil {
		t.Fatalf("type: %v", err)
	}
	name, err := m.Eval(`document.getElementById('name').value`)
	if err != nil {
		t.Fatalf("eval name: %v", err)
	}
	if got := rawValue(name.Value); !strings.Contains(got, "Ada") {
		t.Fatalf("name value = %s", got)
	}

	shot, err := m.Screenshot(false)
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if st, err := os.Stat(shot.Screenshot); err != nil || st.Size() == 0 {
		t.Fatalf("screenshot file: %v (%v)", err, st)
	}
	if shot.Width <= 0 || shot.Height <= 0 {
		t.Fatalf("screenshot size = %dx%d", shot.Width, shot.Height)
	}

	// Console and network subscriptions are asynchronous; give them a moment.
	deadline := time.Now().Add(5 * time.Second)
	for {
		logs := m.Console(0).Console
		has := false
		for _, e := range logs {
			if strings.Contains(e.Text, "hello from probe") {
				has = true
			}
		}
		if has {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("console never captured the page log: %+v", logs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		reqs := m.Network(0).Network
		has := false
		for _, e := range reqs {
			if strings.Contains(e.URL, srv.URL) && e.Status == 200 {
				has = true
			}
		}
		if has {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("network never captured the document: %+v", reqs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	vp, err := m.Viewport("mobile")
	if err != nil {
		t.Fatalf("viewport: %v", err)
	}
	if vp.Width != 390 || vp.Height != 844 {
		t.Fatalf("viewport = %+v", vp)
	}
}

// waitState waits for a host's state file so the client reuses it instead of
// spawning the test binary as a host.
func rawValue(v any) string {
	switch x := v.(type) {
	case json.RawMessage:
		return string(x)
	case []byte:
		return string(x)
	}
	return fmt.Sprint(v)
}

func waitState(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "host.json")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("host never published its state")
}

func TestHostPingAndClose(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeHost(ctx, HostOptions{StateDir: dir, IdleTimeout: time.Minute, IdleCheck: 50 * time.Millisecond})
	}()
	waitState(t, dir)
	client := NewClient(dir, "")
	res, err := client.Do(context.Background(), "ping", nil)
	if err != nil || res.Message != "ok" {
		t.Fatalf("ping = %+v, %v", res, err)
	}
	if _, err := client.Do(context.Background(), "close", nil); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("host exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host did not exit after close")
	}
	if _, err := os.Stat(filepath.Join(dir, "host.json")); !os.IsNotExist(err) {
		t.Fatalf("state file survived close: %v", err)
	}
}

func TestHostIdleExit(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeHost(ctx, HostOptions{
			StateDir:    dir,
			IdleTimeout: 80 * time.Millisecond,
			IdleCheck:   20 * time.Millisecond,
		})
	}()
	waitState(t, dir)
	client := NewClient(dir, "")
	if _, err := client.Do(context.Background(), "ping", nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("host exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle host did not exit")
	}
}

func TestHostRequiresToken(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeHost(ctx, HostOptions{StateDir: dir, IdleTimeout: time.Minute, IdleCheck: 50 * time.Millisecond})
	}()
	waitState(t, dir)
	client := NewClient(dir, "")
	if _, err := client.Do(context.Background(), "ping", nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	st, err := client.readState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/op", st.Port),
		strings.NewReader(`{"op":"ping"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", resp.StatusCode)
	}
	_, _ = client.Do(context.Background(), "close", nil)
	<-done
}
