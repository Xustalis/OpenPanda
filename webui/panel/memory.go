package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// getMemory serves GET /api/memory — a read-only peek at the Hermes memory
// files (USER.md / MEMORY.md / DREAMS.md) so the console can show what the
// agent remembers and what it dreamed. Each file is capped at 64 KiB; a
// missing file is reported as an empty string, not an error (fresh nodes
// start without memory).
func (h *handler) getMemory(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	root := h.cfg.Storage.MemoryPath
	resp := map[string]any{
		"user":   readFileCapped(filepath.Join(root, "USER.md")),
		"memory": readFileCapped(filepath.Join(root, "MEMORY.md")),
		"dreams": readFileCapped(filepath.Join(root, "DREAMS.md")),
		"time":   time.Now().Format(time.RFC3339),
	}
	writeJSON(w, resp)
}

var errNoMemoryConfig = &staticError{msg: "memory path not configured"}

// readFileCapped returns the file's content capped at maxMemoryView bytes
// (with a truncation marker), or "" when unreadable.
const maxMemoryView = 64 << 10

func readFileCapped(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	if len(data) > maxMemoryView {
		return string(data[:maxMemoryView]) + "\n…（已截断）"
	}
	return string(data)
}
