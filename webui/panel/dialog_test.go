package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestListDirectories(t *testing.T) {
	tempDir := t.TempDir()
	sub1 := filepath.Join(tempDir, "alpha")
	sub2 := filepath.Join(tempDir, "beta")
	hidden := filepath.Join(tempDir, ".hidden")
	file := filepath.Join(tempDir, "hello.txt")

	_ = os.MkdirAll(sub1, 0755)
	_ = os.MkdirAll(sub2, 0755)
	_ = os.MkdirAll(hidden, 0755)
	_ = os.WriteFile(file, []byte("test"), 0644)

	h := &handler{}
	req := httptest.NewRequest("GET", "/api/fs/directories?path="+tempDir, nil)
	w := httptest.NewRecorder()

	h.listDirectories(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp listDirectoriesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if resp.Current != tempDir {
		t.Errorf("expected current %q, got %q", tempDir, resp.Current)
	}
	if len(resp.Directories) != 2 {
		t.Errorf("expected 2 directories, got %d", len(resp.Directories))
	}
	names := []string{resp.Directories[0].Name, resp.Directories[1].Name}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("expected [alpha, beta], got %v", names)
	}
}
