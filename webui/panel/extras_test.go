// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"gopkg.in/yaml.v3"
)

// newFsPanel builds a handler with a real config whose storage roots live in
// a temp dir — the fs endpoints key everything off those roots. The returned
// DB is the handler's own store handle (audit_log lives there).
func newFsPanel(t *testing.T) (http.Handler, string, *sql.DB) {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Storage.WorkPath = tmp
	cfg.Storage.MemoryPath = filepath.Join(tmp, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(tmp, "projects")
	configPath := filepath.Join(tmp, "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	h := New(Deps{
		Store:      newTestStore(t),
		DB:         db,
		Cfg:        cfg,
		ConfigPath: configPath,
		StaticDir:  t.TempDir(),
		Token:      testToken,
	})
	return h, tmp, db
}

func TestOnboardingRoundTrip(t *testing.T) {
	h, _, db := newFsPanel(t)

	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/onboarding", nil))
	if code != http.StatusOK {
		t.Fatalf("get status = %d", code)
	}
	if out["onboarded"] != false || out["terms_accepted"] != false {
		t.Fatalf("defaults should be false: %v", out)
	}
	if out["approval_mode"] != config.ApprovalModeOnRequest {
		t.Fatalf("approval default wrong: %v", out["approval_mode"])
	}
	// The console must not carry its own copy of the terms revision — it
	// compares against this field, so a backend bump cannot be silently
	// outvoted by a stale hard-coded constant in the browser bundle.
	if out["terms_version_current"] != float64(config.TermsVersionCurrent) {
		t.Fatalf("terms_version_current = %v, want %d", out["terms_version_current"], config.TermsVersionCurrent)
	}

	body := `{"locale":"zh-CN","terms_accepted":true,"approval_mode":"never","onboarded":true}`
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/onboarding", body))
	if code != http.StatusOK {
		t.Fatalf("post status = %d", code)
	}
	if out["locale"] != "zh-CN" || out["onboarded"] != true || out["approval_mode"] != "never" {
		t.Fatalf("post did not persist: %v", out)
	}
	// Accepting terms stamps the current revision so a license change can
	// re-prompt stale consents, and leaves consent evidence: a UTC timestamp
	// plus a row on the signed audit chain.
	if out["terms_version"] != float64(config.TermsVersionCurrent) {
		t.Fatalf("terms_version not stamped: %v", out["terms_version"])
	}
	if out["terms_accepted_at"] == "" {
		t.Fatalf("terms_accepted_at not stamped: %v", out["terms_accepted_at"])
	}
	var auditWhat string
	if err := db.QueryRow(`SELECT what FROM audit_log WHERE what = 'terms:accept'`).Scan(&auditWhat); err != nil {
		t.Fatalf("terms consent not audited: %v", err)
	}

	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/onboarding", `{"approval_mode":"yolo"}`))
	if code != http.StatusBadRequest {
		t.Fatalf("bad approval accepted: %d", code)
	}
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/onboarding", `{"locale":"klingon"}`))
	if code != http.StatusBadRequest {
		t.Fatalf("bad locale accepted: %d", code)
	}
}

func TestFsReadConfinedToRoots(t *testing.T) {
	h, tmp, _ := newFsPanel(t)

	inside := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(inside, []byte("hi panda"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Absolute path inside the work root reads fine.
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/fs/read?path="+inside, nil))
	if code != http.StatusOK || out["content"] != "hi panda" {
		t.Fatalf("inside read: %d %v", code, out)
	}

	// The same file by relative name resolves against work_path.
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/read?path=hello.txt", nil))
	if code != http.StatusOK || out["content"] != "hi panda" {
		t.Fatalf("relative read: %d %v", code, out)
	}

	// Outside every root is refused — the panel is a network surface.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _ = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/read?path="+outside, nil))
	if code != http.StatusForbidden {
		t.Fatalf("outside read status = %d", code)
	}

	// Traversal out of the work root is refused too.
	code, _ = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/read?path=../"+filepath.Base(outside), nil))
	if code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("traversal status = %d", code)
	}

	// A directory is not a file.
	code, _ = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/read?path="+tmp, nil))
	if code != http.StatusNotFound {
		t.Fatalf("dir read status = %d", code)
	}
}

func TestFsReadTruncates(t *testing.T) {
	h, tmp, _ := newFsPanel(t)
	big := filepath.Join(tmp, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", maxReadBytes+512)), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/fs/read?path=big.txt", nil))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if out["truncated"] != true {
		t.Fatalf("expected truncation: %v", out["truncated"])
	}
	if len(out["content"].(string)) != maxReadBytes {
		t.Fatalf("content len = %d", len(out["content"].(string)))
	}
}

// pngBytes is a real (2×2) PNG — the image endpoint hands back the file's own
// bytes, so the test needs something a decoder would accept.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFsImageServesPreviewableBytes(t *testing.T) {
	h, tmp, _ := newFsPanel(t)
	raw := pngBytes(t)
	shot := filepath.Join(tmp, "shot.png")
	if err := os.WriteFile(shot, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/fs/image?path="+url.QueryEscape(shot), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("image status = %d body %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content-type = %q", got)
	}
	// The bytes must arrive untouched — a preview that re-encodes would blur
	// a screenshot the transcript is showing as evidence.
	if !bytes.Equal(rr.Body.Bytes(), raw) {
		t.Fatalf("body is not the file's bytes: %d vs %d", rr.Body.Len(), len(raw))
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff")
	}

	// A non-image extension is refused before any bytes move: the endpoint
	// exists for previews, not as a second /api/fs/read.
	if err := os.WriteFile(filepath.Join(tmp, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/fs/image?path=notes.txt", nil))
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text file status = %d", rr.Code)
	}

	// Missing file: 404, not a zero-byte 200.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/fs/image?path=absent.png", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", rr.Code)
	}
}

// TestFsImageTempRootWidensReadOnlyForImages: agents park screenshots in a
// scratch dir outside every storage root, and the transcript still has to show
// them. The widening is scoped to the image endpoint — /api/fs/read keeps
// refusing the very same file.
func TestFsImageTempRootWidensReadOnlyForImages(t *testing.T) {
	h, _, _ := newFsPanel(t)
	// t.TempDir() lives under the OS temp tree and outside the work root.
	scratch := t.TempDir()
	shot := filepath.Join(scratch, "capture.png")
	if err := os.WriteFile(shot, pngBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/fs/image?path="+url.QueryEscape(shot), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("temp image status = %d body %s", rr.Code, rr.Body.String())
	}

	code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/fs/read?path="+url.QueryEscape(shot), nil))
	if code != http.StatusForbidden {
		t.Fatalf("fs/read on a temp file status = %d, want 403", code)
	}

	// Outside both the roots and the temp tree: refused.
	//
	// The temp tree's parent, not filepath.Dir(os.TempDir()): os.TempDir
	// returns $TMPDIR verbatim, and macOS sets that with a trailing slash
	// ("/var/folders/…/T/"), so Dir() lands back on the temp tree itself and
	// the "outside" path would sit inside the root the endpoint allows.
	// Clean() first, then one Dir() up, gives a path above the tree on
	// every platform.
	outside := filepath.Join(filepath.Dir(filepath.Clean(os.TempDir())), "panda-image-outside.png")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/fs/image?path="+url.QueryEscape(outside), nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("outside status = %d, want 403", rr.Code)
	}
}

func TestFsImageRejectsOversize(t *testing.T) {
	h, tmp, _ := newFsPanel(t)
	// A sparse file: stat size is all the handler needs to refuse it, and the
	// test should not write 16 MiB to disk to prove that.
	big := filepath.Join(tmp, "huge.png")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxImageBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/fs/image?path=huge.png", nil))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d", rr.Code)
	}
}

func TestFsFilesConfined(t *testing.T) {
	h, tmp, _ := newFsPanel(t)
	if err := os.WriteFile(filepath.Join(tmp, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Empty path lists the work root.
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/fs/files", nil))
	if code != http.StatusOK {
		t.Fatalf("root list status = %d", code)
	}
	names := map[string]bool{}
	for _, e := range out["entries"].([]any) {
		names[e.(map[string]any)["name"].(string)] = true
	}
	if !names["a.go"] {
		t.Fatalf("a.go missing from listing: %v", names)
	}

	// Outside the roots the listing is refused — it would only advertise
	// paths /api/fs/read rejects anyway. A second TempDir is absolute on
	// every platform and outside the work/memory roots; a bare "/etc" is
	// not absolute on Windows and would resolve inside the workspace.
	code, _ = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/files?path="+t.TempDir(), nil))
	if code != http.StatusForbidden {
		t.Fatalf("outside list status = %d", code)
	}
}

// TestSafeReadRootRejectsWideWorkDirs pins the read-side half of the breadth
// rule. projects.Create/Update validate on write, but a row written before that
// rule existed keeps its work dir across an upgrade, and admitting it here would
// widen /api/fs/read to the whole disk.
func TestSafeReadRootRejectsWideWorkDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	for _, dir := range []string{string(filepath.Separator), home, filepath.Dir(home)} {
		if safeReadRoot(dir) {
			t.Errorf("safeReadRoot(%q) = true, want false", dir)
		}
	}
	if safeReadRoot("") {
		t.Error(`safeReadRoot("") = true, want false`)
	}
	if !safeReadRoot(t.TempDir()) {
		t.Error("safeReadRoot(ordinary project dir) = false, want true")
	}
}

func TestFsRevealOpensAllowedDir(t *testing.T) {
	h, tmp, _ := newFsPanel(t)

	var got string
	orig := openInFileManager
	openInFileManager = func(dir string) error {
		got = dir
		return nil
	}
	t.Cleanup(func() { openInFileManager = orig })

	inside := filepath.Join(tmp, "work")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"path": inside})
	if err != nil {
		t.Fatal(err)
	}
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/fs/reveal", string(body)))
	if code != http.StatusOK {
		t.Fatalf("reveal status = %d", code)
	}
	// The handler reports the resolved path; TempDir may itself be a symlink
	// on some platforms, so compare against the resolved form.
	want, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("opened %q, want %q", got, want)
	}
	if out["opened"] != got {
		t.Fatalf("response opened = %v, want %q", out["opened"], got)
	}

	// Outside the owned roots the desktop shell is never invoked.
	got = ""
	body, err = json.Marshal(map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/fs/reveal", string(body)))
	if code != http.StatusForbidden {
		t.Fatalf("outside reveal status = %d", code)
	}
	if got != "" {
		t.Fatalf("outside reveal opened %q", got)
	}

	// A path that is not a directory, and an empty one, are refused before
	// the shell is touched.
	file := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(map[string]string{"path": file})
	if err != nil {
		t.Fatal(err)
	}
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/fs/reveal", string(body)))
	if code != http.StatusNotFound {
		t.Fatalf("file reveal status = %d", code)
	}
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/fs/reveal", `{"path":"  "}`))
	if code != http.StatusBadRequest {
		t.Fatalf("empty reveal status = %d", code)
	}
	if got != "" {
		t.Fatalf("refused reveal still opened %q", got)
	}
}
