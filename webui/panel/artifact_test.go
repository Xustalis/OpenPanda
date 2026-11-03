// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// artifactPanel builds a handler wired to a real task row whose
// output_artifact points at a packed fixture in a temp pool — the same
// arrangement the daemon ends in after an executed task.
func artifactPanel(t *testing.T) (http.Handler, *artifact.Store, string) {
	t.Helper()
	store := newTestStore(t)
	pool := artifact.NewStore(t.TempDir())

	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "screen.png"),
		[]byte("\x89PNG\r\n\x1a\n"+"fake-pixels"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "report.md"),
		[]byte("# done"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := pool.PackDir(tree)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}

	task, err := store.Create(context.Background(), "", "proj", "with artifact", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetOutputArtifact(context.Background(), task.TaskID, m.Hash); err != nil {
		t.Fatalf("set artifact: %v", err)
	}

	h := New(Deps{Store: store, Artifacts: pool, StaticDir: t.TempDir(), Token: testToken})
	return h, pool, task.TaskID
}

func TestTaskArtifactListAndPreview(t *testing.T) {
	h, _, id := artifactPanel(t)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+id+"/artifact/files", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("files: %d %s", rr.Code, rr.Body.String())
	}
	var list struct {
		Hash    string               `json:"hash"`
		Entries []artifact.EntryMeta `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, e := range list.Entries {
		found[e.Path] = true
	}
	if !found["screen.png"] || !found["report.md"] {
		t.Fatalf("entries = %v", list.Entries)
	}

	// The PNG previews inline as image/png — the screenshot-preview path.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+id+"/artifact/file?path=screen.png", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q, want image/png", ct)
	}
	if !strings.HasPrefix(rr.Body.String(), "\x89PNG") {
		t.Fatal("preview body is not the stored PNG bytes")
	}

	// Text renders as text/plain — never as something a browser would run.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+id+"/artifact/file?path=report.md", nil))
	if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("text content-type = %q", ct)
	}
}

func TestTaskArtifactDownload(t *testing.T) {
	h, _, id := artifactPanel(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+id+"/artifact", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("download: %d", rr.Code)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, ".tar.gz") {
		t.Fatalf("content-disposition = %q", cd)
	}
	body := rr.Body.Bytes()
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Fatal("download body is not a gzip stream")
	}
}

func TestTaskArtifactHonestMisses(t *testing.T) {
	h, _, id := artifactPanel(t)
	store := newTestStore(t)
	bare, _ := store.Create(context.Background(), "", "proj", "no artifact", "node", []string{"node"})
	h2 := New(Deps{Store: store, Artifacts: artifact.NewStore(t.TempDir()), StaticDir: t.TempDir(), Token: testToken})

	// Task with no output artifact → 404 naming the cause.
	rr := httptest.NewRecorder()
	h2.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+bare.TaskID+"/artifact", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("no-artifact task: %d", rr.Code)
	}

	// Artifact recorded but not held locally → 404 saying where it lives:
	// a valid-shape hash the empty pool never received.
	foreign, _ := store.Create(context.Background(), "", "proj", "remote output", "node", []string{"node"})
	if err := store.SetOutputArtifact(context.Background(), foreign.TaskID, strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	h2.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+foreign.TaskID+"/artifact", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("foreign artifact on this store: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "not in this node's pool") {
		t.Fatalf("expected the not-held explanation, got %q", rr.Body.String())
	}

	// Missing entry inside a held artifact → 404; traversal → 400.
	for target, want := range map[string]int{
		"/api/tasks/" + id + "/artifact/file?path=absent.bin": http.StatusNotFound,
		"/api/tasks/" + id + "/artifact/file?path=../x":       http.StatusBadRequest,
	} {
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodGet, target, nil))
		if rr.Code != want {
			t.Fatalf("%s → %d, want %d (%s)", target, rr.Code, want, rr.Body.String())
		}
	}
}

func TestTaskArtifactNoPoolIs503(t *testing.T) {
	store := newTestStore(t)
	task, _ := store.Create(context.Background(), "", "proj", "x", "node", []string{"node"})
	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+task.TaskID+"/artifact", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("pool-less panel: %d, want 503", rr.Code)
	}
	// And downloads stay bounded: the body read path must not hang.
	_, _ = io.Copy(io.Discard, rr.Body)
}

// The origin's row keeps owner_node pointing at itself after a delegation.
// The source must be the dispatch target (or the result's executor), or the
// pull asks the wrong machine and finds nothing.
func TestArtifactSourcePrefersTheExecutor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "delegated", "origin", []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	if got := artifactSource(ctx, store, task); got != "origin" {
		t.Fatalf("local source = %q, want the owner", got)
	}
	if err := store.RecordEvent(ctx, task.TaskID, core.EvDelegate, map[string]any{"target": "peer"}); err != nil {
		t.Fatal(err)
	}
	if got := artifactSource(ctx, store, task); got != "peer" {
		t.Fatalf("delegated source = %q, want the dispatch target", got)
	}

	other, err := store.Create(ctx, "", "proj", "result-attributed", "origin", []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	other.ResultJSON = `{"executor":"peer-2"}`
	if got := artifactSource(ctx, store, other); got != "peer-2" {
		t.Fatalf("result source = %q, want the result executor", got)
	}
}

// A delegated task's row is owned by the origin; the preview must name the
// executor as the source, and say whether this node can pull it.
func TestPreviewReportsTheExecutorAsRemoteSource(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pool := artifact.NewStore(t.TempDir())
	task, err := store.Create(ctx, "", "proj", "remote artifact", "origin", []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetOutputArtifact(ctx, task.TaskID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvent(ctx, task.TaskID, core.EvDelegate, map[string]any{"target": "peer"}); err != nil {
		t.Fatal(err)
	}
	preview, err := StartPreview(PreviewDeps{Artifacts: pool, Host: "127.0.0.1", Ancestors: "*"})
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	h := New(Deps{Store: store, Artifacts: pool, Preview: preview, StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+task.TaskID+"/artifact/preview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		State    string `json:"state"`
		Source   string `json:"source"`
		CanFetch bool   `json:"can_fetch"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "remote" || got.Source != "peer" {
		t.Fatalf("preview = %+v, want remote from peer", got)
	}
	if got.CanFetch {
		t.Fatal("can_fetch must be false when no engine is wired")
	}
}
