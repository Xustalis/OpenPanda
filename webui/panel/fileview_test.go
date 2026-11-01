// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"gopkg.in/yaml.v3"
)

// newFileViewPanel builds a panel whose work_path and session store are both
// throwaway, so a worktree outside work_path is still a legal base.
func newFileViewPanel(t *testing.T) (http.Handler, string, *sessions.Store) {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Storage.WorkPath = filepath.Join(tmp, "work")
	cfg.Storage.MemoryPath = filepath.Join(tmp, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(tmp, "projects")
	for _, d := range []string{cfg.Storage.WorkPath, cfg.Storage.MemoryPath, cfg.Storage.ProjectsPath} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
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

	store := sessions.NewStore(filepath.Join(tmp, "sessions"))
	h := New(Deps{
		Store:      newTestStore(t),
		DB:         db,
		Cfg:        cfg,
		ConfigPath: configPath,
		StaticDir:  t.TempDir(),
		Token:      testToken,
		Sessions:   store,
	})
	return h, tmp, store
}

// numOf reads a numeric field out of a decoded JSON object.
func numOf(t *testing.T, out map[string]any, key string) float64 {
	t.Helper()
	n, ok := out[key].(float64)
	if !ok {
		t.Fatalf("%s is not a number: %#v", key, out[key])
	}
	return n
}

func TestViewFileResolvesAgainstSessionWorktree(t *testing.T) {
	h, tmp, store := newFileViewPanel(t)

	// A worktree outside work_path: the case that made a thread's own files
	// unreadable before this endpoint existed.
	worktree := filepath.Join(tmp, "repo", ".openpanda", "worktrees", "s1")
	if err := os.MkdirAll(filepath.Join(worktree, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(worktree, "src", "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := store.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorktree(sess.ID, worktree, "feat"); err != nil {
		t.Fatal(err)
	}

	code, out := doJSON(t, h, authedReq(http.MethodGet,
		"/api/fs/file?session="+sess.ID+"&path=src/main.go", nil))
	if code != http.StatusOK {
		t.Fatalf("session-relative read: %d %v", code, out)
	}
	if out["content"] != "package main\n\nfunc main() {}" {
		t.Errorf("content = %q", out["content"])
	}
	if out["rel"] != "src/main.go" {
		t.Errorf("rel = %q", out["rel"])
	}
	if n := numOf(t, out, "total_lines"); n != 3 {
		t.Errorf("total_lines = %v", n)
	}

	// Without the session the same relative path misses: it is not under
	// work_path, and the endpoint must not guess.
	code, _ = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/file?path=src/main.go", nil))
	if code == http.StatusOK {
		t.Error("a worktree path resolved without its session")
	}
}

func TestViewFileWindowAroundLine(t *testing.T) {
	h, tmp, _ := newFileViewPanel(t)

	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(tmp, "work", "long.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := doJSON(t, h, authedReq(http.MethodGet,
		"/api/fs/file?path=long.txt&line=100&count=2", nil))
	if code != http.StatusOK {
		t.Fatalf("window read: %d %v", code, out)
	}
	if n := numOf(t, out, "start"); n != 98 {
		t.Errorf("start = %v", n)
	}
	if n := numOf(t, out, "end"); n != 102 {
		t.Errorf("end = %v", n)
	}
	if n := numOf(t, out, "total_lines"); n != 200 {
		t.Errorf("total_lines = %v", n)
	}
	content, _ := out["content"].(string)
	if !strings.Contains(content, "line 100") {
		t.Errorf("window misses the focus line: %q", content)
	}
	if strings.Contains(content, "line 91") {
		t.Errorf("window reaches further than the count allows: %q", content)
	}
}

func TestViewFileRefusesOutsideEveryRoot(t *testing.T) {
	h, _, _ := newFileViewPanel(t)

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/fs/file?path="+outside, nil))
	if code != http.StatusForbidden {
		t.Errorf("outside read status = %d", code)
	}

	code, _ = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/file?path=../secret.txt", nil))
	if code == http.StatusOK {
		t.Error("traversal read was served")
	}
}

func TestViewFileFlagsBinaryAndDirectories(t *testing.T) {
	h, tmp, _ := newFileViewPanel(t)

	bin := filepath.Join(tmp, "work", "blob.bin")
	if err := os.WriteFile(bin, []byte{0x00, 0x01, 0x02, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/fs/file?path=blob.bin", nil))
	if code != http.StatusOK {
		t.Fatalf("binary read: %d %v", code, out)
	}
	if out["binary"] != true {
		t.Errorf("binary flag = %v", out["binary"])
	}
	if out["content"] != "" {
		t.Errorf("binary content leaked: %q", out["content"])
	}

	code, _ = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/file?path=.", nil))
	if code != http.StatusNotFound {
		t.Errorf("directory read status = %d", code)
	}
}

func TestViewFileStreamsLargeFiles(t *testing.T) {
	h, tmp, _ := newFileViewPanel(t)

	// Larger than the cap: the endpoint must answer the window without
	// reading the whole thing, and without claiming a line count it never made.
	var b strings.Builder
	for b.Len() < maxViewBytes+2048 {
		b.WriteString("0123456789abcdef0123456789abcdef\n")
	}
	if err := os.WriteFile(filepath.Join(tmp, "work", "huge.log"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := doJSON(t, h, authedReq(http.MethodGet,
		"/api/fs/file?path=huge.log&line=10&count=1", nil))
	if code != http.StatusOK {
		t.Fatalf("large window read: %d %v", code, out)
	}
	if n := numOf(t, out, "start"); n != 9 {
		t.Errorf("start = %v", n)
	}
	if n := numOf(t, out, "end"); n != 11 {
		t.Errorf("end = %v", n)
	}
	if n := numOf(t, out, "total_lines"); n != -1 {
		t.Errorf("total_lines = %v, want -1 (unknown)", n)
	}
	if content, _ := out["content"].(string); len(content) > maxViewBytes {
		t.Errorf("content exceeded the cap: %d bytes", len(content))
	}

	// Without a focus line a huge file is still answerable, but only the head.
	code, out = doJSON(t, h, authedReq(http.MethodGet, "/api/fs/file?path=huge.log&count=2", nil))
	if code != http.StatusOK {
		t.Fatalf("head read: %d %v", code, out)
	}
	if out["truncated"] != true {
		t.Errorf("truncated = %v", out["truncated"])
	}
	if n := numOf(t, out, "start"); n != 1 {
		t.Errorf("start = %v", n)
	}
}

// TestViewFileRejectsSessionWorktreeOutsideBreadthRule pins the widening's
// limit: a session whose worktree is the filesystem root contributes no root,
// exactly as a project's would not.
func TestViewFileRejectsSessionWorktreeOutsideBreadthRule(t *testing.T) {
	h, _, store := newFileViewPanel(t)

	sess, err := store.Create("bad")
	if err != nil {
		t.Fatal(err)
	}
	// Written straight through the store, as a corrupted or hand-edited file
	// would be — the endpoints never write a worktree like this themselves.
	if err := store.SetWorktree(sess.ID, string(filepath.Separator), "x"); err != nil {
		t.Fatal(err)
	}

	code, _ := doJSON(t, h, authedReq(http.MethodGet,
		"/api/fs/file?session="+sess.ID+"&path=etc/hosts", nil))
	if code == http.StatusOK {
		t.Error("a worktree at the filesystem root was admitted")
	}
}
