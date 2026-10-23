// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// dropConnection closes the socket without writing a response, which is what
// the client observes as the peer going away mid-request — the transient
// failure the retry loop exists for. Hijacking is the only way a handler can
// abort that abruptly; returning normally would send a well-formed reply.
func dropConnection(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatalf("test server does not support hijacking")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	conn.Close()
}

func TestFetchTextRetriesATransientFailure(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			dropConnection(t, w)
			return
		}
		_, _ = io.WriteString(w, "checksums")
	}))
	defer srv.Close()

	got, err := fetchText(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchText: %v", err)
	}
	if got != "checksums" {
		t.Fatalf("body = %q, want %q", got, "checksums")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("server saw %d requests, want 2 (one failure, one retry)", n)
	}
}

// A definitive answer must not be repeated: GitHub data-race reporting aside,
// three 404s produce the same error as one, three seconds later.
func TestFetchTextDoesNotRetryADefinitiveResponse(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := fetchText(context.Background(), srv.URL); err == nil {
		t.Fatal("a 404 was treated as a success")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("server saw %d requests, want 1", n)
	}
}

// The retry loop must honour the caller's deadline instead of sleeping through
// it: an update that gives up is fine, an update that ignores its deadline is
// a hang.
func TestWithRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var calls atomic.Int32
	start := time.Now()
	_, err := withRetry(ctx, 5, func(context.Context) (int, error) {
		calls.Add(1)
		return 0, &timeoutError{}
	})
	if err == nil {
		t.Fatal("withRetry returned no error for a permanently failing operation")
	}
	if n := calls.Load(); n > 2 {
		t.Fatalf("withRetry made %d attempts past a 50ms deadline", n)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("withRetry waited %s past its deadline", elapsed)
	}
}

// timeoutError satisfies net.Error without being a real network error.
// Temporary is part of the interface net.Error still declares, even though its
// meaning was deprecated — a stub that omits it does not satisfy errors.As.
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "i/o timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"timeout", &timeoutError{}, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"eof", io.EOF, true},
		{"connection reset", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}, true},
		{"plain", fmt.Errorf("checksums.txt has no entry"), false},
		{"status text", fmt.Errorf("404 Not Found"), false},
	}
	for _, c := range cases {
		if got := retryable(c.err); got != c.want {
			t.Errorf("%s: retryable = %v, want %v", c.name, got, c.want)
		}
	}
}

// redactURL must keep the URL usable in a message while dropping the signed
// query GitHub puts on asset URLs — several hundred bytes of token that push
// the actual cause off screen.
func TestRedactURL(t *testing.T) {
	in := "https://release-assets.githubusercontent.com/x/checksums.txt?sp=r&sig=abc"
	got := redactURL(in)
	if strings.Contains(got, "sig=abc") {
		t.Fatalf("redactURL kept the query: %s", got)
	}
	if !strings.HasPrefix(got, "https://release-assets.githubusercontent.com/x/checksums.txt") {
		t.Fatalf("redactURL lost the path: %s", got)
	}
	if got := redactURL("http://127.0.0.1:9/a.txt"); got != "http://127.0.0.1:9/a.txt" {
		t.Fatalf("redactURL rewrote a query-less URL: %s", got)
	}
}

// downloadRelease's baseOverride replaces the whole per-version asset
// directory, which is the shape a mirror URL takes. The canonical base is
// pointed at a closed port so a regression that ignores the override fails
// fast instead of silently fetching from GitHub.
func TestDownloadReleaseBaseOverride(t *testing.T) {
	const version = "9.9.9"
	asset, err := AssetName(version)
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}
	payload := []byte("not really a zip")
	sums := fmt.Sprintf("%x  %s\n", sha256.Sum256(payload), asset)

	mux := http.NewServeMux()
	mux.HandleFunc("/mirror/releases/download/v"+version+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, sums)
	})
	mux.HandleFunc("/mirror/releases/download/v"+version+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cleanup := SetDownloadBaseForTest("http://127.0.0.1:1")
	defer cleanup()

	got, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), "", true, srv.URL+"/mirror/releases/download/v"+version)
	if err != nil {
		t.Fatalf("downloadRelease with base override: %v", err)
	}
	if !strings.HasSuffix(got, asset) {
		t.Fatalf("archive path = %s, want suffix %s", got, asset)
	}
}
