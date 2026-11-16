// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func newSessionDeletePanel(t *testing.T) (http.Handler, *sessions.Store, string) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	store := sessions.NewStore(filepath.Join(root, "sessions"))
	cfg := &config.Config{Storage: config.StorageConfig{WorkPath: work}}
	return New(Deps{Sessions: store, Cfg: cfg, Token: testToken, StaticDir: t.TempDir()}), store, work
}

func deleteSessionRequest(t *testing.T, h http.Handler, id string, bulk bool) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	if bulk {
		body, err := json.Marshal(bulkDeleteSessionRequest{IDs: []string{id}})
		if err != nil {
			t.Fatal(err)
		}
		h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/bulk-delete", strings.NewReader(string(body))))
	} else {
		// Encoded dots exercise the handler even for ".", which the mux would
		// otherwise redirect before decoding the path segment.
		encoded := strings.ReplaceAll(url.PathEscape(id), ".", "%2e")
		h.ServeHTTP(rr, authedReq(http.MethodDelete, "/api/sessions/"+encoded, nil))
	}
	return rr
}

func TestSessionDeleteRejectsUnsafeIDsBeforeCleanup(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		for _, id := range []string{"../../../victim", `..\..\..\victim`, ".", "missing-session"} {
			name := "single/" + id
			if bulk {
				name = "bulk/" + id
			}
			t.Run(name, func(t *testing.T) {
				h, _, work := newSessionDeletePanel(t)
				// Every possible deletion target stays within this test's temp
				// root, including the directory outside the configured work path.
				paths := []string{
					filepath.Join(filepath.Dir(work), "victim", "keep.txt"),
					filepath.Join(work, ".openpanda", "uploads", "other-session", "keep.txt"),
					filepath.Join(work, ".openpanda", "uploads", "missing-session", "keep.txt"),
				}
				for _, path := range paths {
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				rr := deleteSessionRequest(t, h, id, bulk)
				if bulk {
					var result struct {
						Deleted []string          `json:"deleted"`
						Failed  []json.RawMessage `json:"failed"`
					}
					if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if rr.Code != http.StatusOK || len(result.Deleted) != 0 || len(result.Failed) != 1 {
						t.Fatalf("bulk delete = %d %s", rr.Code, rr.Body.String())
					}
				} else if rr.Code != http.StatusNotFound {
					t.Fatalf("delete = %d %s, want 404", rr.Code, rr.Body.String())
				}
				for _, path := range paths {
					if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
						t.Errorf("rejected id %q changed %s: %q, %v", id, path, data, err)
					}
				}
			})
		}
	}
}

func TestSessionDeleteCleansOnlyItsUploads(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			h, store, work := newSessionDeletePanel(t)
			sess, err := store.Create("delete me")
			if err != nil {
				t.Fatal(err)
			}
			own := filepath.Join(work, ".openpanda", "uploads", sess.ID)
			other := filepath.Join(work, ".openpanda", "uploads", "other-session")
			for _, dir := range []string{own, other} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("upload"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if rr := deleteSessionRequest(t, h, sess.ID, bulk); rr.Code != http.StatusOK {
				t.Fatalf("delete = %d %s", rr.Code, rr.Body.String())
			}
			if _, err := store.Get(sess.ID); err != sessions.ErrNotFound {
				t.Errorf("deleted session: %v", err)
			}
			if _, err := os.Stat(own); !os.IsNotExist(err) {
				t.Errorf("own uploads survived: %v", err)
			}
			if _, err := os.Stat(filepath.Join(other, "file.txt")); err != nil {
				t.Errorf("another session's uploads changed: %v", err)
			}
		})
	}
}
