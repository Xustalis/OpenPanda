// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/updater"
)

func TestBackgroundUpdateSurvivesRequestCancel(t *testing.T) {
	name, err := updater.AssetName("9.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	stopped := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/releases", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[{\"tag_name\":\"v9.0.0\"}]") })
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), name)
	})
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", "8")
			fmt.Fprint(w, "half")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(stopped)
			return
		}
		fmt.Fprint(w, "bad file")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer updater.SetAPIBaseForTest(srv.URL)()
	manager := updater.New(updater.Options{Repo: "test/repo", Current: "1.0.0", DownloadBase: srv.URL, AllowUnsigned: true})
	h := &handler{updater: manager, serviceCtx: context.Background()}
	defer manager.Cancel()
	if err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := func() {
		req := httptest.NewRequest(http.MethodPost, "/api/update/download?background=1", nil)
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		rec := httptest.NewRecorder()
		h.downloadUpdate(rec, req.WithContext(ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("download returned %d: %s", rec.Code, rec.Body.String())
		}
	}
	waitFor := func(match func(updater.Status) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if match(manager.Status()) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("unexpected status: %+v", manager.Status())
	}
	start()
	waitFor(func(s updater.Status) bool {
		return s.Stage == updater.StageDownloading && s.DownloadedBytes == 4 && s.TotalBytes == 8
	})
	start()
	h.cancelUpdate(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/update/cancel", nil))
	start()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer was not canceled")
	}
	waitFor(func(s updater.Status) bool {
		return s.Stage == updater.StageError && strings.Contains(s.Error, "SHA-256 mismatch")
	})
	if err := manager.Apply(context.Background()); err == nil {
		t.Fatal("corrupt download was staged")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("got %d archive requests, want 2", got)
	}
}
