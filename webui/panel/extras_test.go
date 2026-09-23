package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"gopkg.in/yaml.v3"
)

// newFsPanel builds a handler with a real config whose storage roots live in
// a temp dir — the fs endpoints key everything off those roots.
func newFsPanel(t *testing.T) (http.Handler, string) {
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
	h := New(Deps{
		Store:      newTestStore(t),
		Cfg:        cfg,
		ConfigPath: configPath,
		StaticDir:  t.TempDir(),
		Token:      testToken,
	})
	return h, tmp
}

func TestOnboardingRoundTrip(t *testing.T) {
	h, _ := newFsPanel(t)

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

	body := `{"locale":"zh-CN","terms_accepted":true,"approval_mode":"never","onboarded":true}`
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/onboarding", body))
	if code != http.StatusOK {
		t.Fatalf("post status = %d", code)
	}
	if out["locale"] != "zh-CN" || out["onboarded"] != true || out["approval_mode"] != "never" {
		t.Fatalf("post did not persist: %v", out)
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
	h, tmp := newFsPanel(t)

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
	h, tmp := newFsPanel(t)
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

func TestFsFilesConfined(t *testing.T) {
	h, tmp := newFsPanel(t)
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
