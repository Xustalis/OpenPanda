// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/devserver"
)

func TestDevAPIWithoutManager(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/dev/proj", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestDevAPIStartStatusStop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>dev app</body></html>"))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".panda"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"command":"sleep 30","url":%q}`, upstream.URL)
	if err := os.WriteFile(filepath.Join(dir, devserver.ConfigFile), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := devserver.Start(devserver.Options{
		Resolve: func(name string) (string, bool) { return dir, name == "proj" },
	})
	t.Cleanup(mgr.Close)
	h := New(Deps{Store: newTestStore(t), Dev: mgr, StaticDir: t.TempDir(), Token: testToken})

	get := func() devserver.Status {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/dev/proj", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status: %d %s", rr.Code, rr.Body.String())
		}
		var st devserver.Status
		if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	st := get()
	if !st.Configured || st.State != devserver.StateStopped {
		t.Fatalf("before start: %+v", st)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/dev/proj/start", strings.NewReader("{}")))
	if rr.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for st.State != devserver.StateReady {
		if st.State == devserver.StateFailed {
			t.Fatalf("failed: %s", st.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("never ready: %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
		st = get()
	}
	if st.URL == "" || st.Target != upstream.URL {
		t.Fatalf("ready status: %+v", st)
	}

	// The entry link is the console's door: one redirect, then the app.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(st.URL)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("entry = %d", resp.StatusCode)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/dev/proj/stop", strings.NewReader("{}")))
	if rr.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", rr.Code, rr.Body.String())
	}
	var after devserver.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.State != devserver.StateStopped || !after.Configured {
		t.Fatalf("after stop: %+v", after)
	}
}

func TestDevAPIUnknownProject(t *testing.T) {
	mgr := devserver.Start(devserver.Options{
		Resolve: func(string) (string, bool) { return "", false },
	})
	t.Cleanup(mgr.Close)
	h := New(Deps{Store: newTestStore(t), Dev: mgr, StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/dev/nope/start", strings.NewReader("{}")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "no work dir") {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

// The console's CSP must admit dev-server frames. Their ports do not exist
// when the document loads, so the policy carries the console host with a port
// wildcard; without it the iframe is blocked and the preview stays blank.
func TestConsoleCSPAdmitsDevPorts(t *testing.T) {
	mgr := devserver.Start(devserver.Options{Resolve: func(string) (string, bool) { return "", false }})
	t.Cleanup(mgr.Close)
	h := New(Deps{Store: newTestStore(t), Dev: mgr, StaticDir: t.TempDir(), Token: testToken})
	req := authedReq(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:7850"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-src") || !strings.Contains(csp, "http://127.0.0.1:*") {
		t.Fatalf("csp = %q", csp)
	}
}
