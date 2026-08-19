package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Xustalis/OpenPanda/internal/memory"
)

// getMemory serves GET /api/memory — a read-only peek at the Hermes memory
// files (USER.md / MEMORY.md / DREAMS.md) so the console can show what the
// agent remembers and what it dreamed. Each file is capped at 64 KiB; a
// missing file is reported as an empty string, not an error (fresh nodes
// start without memory). The edit caps ride along so the console can render
// a live character counter.
func (h *handler) getMemory(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	root := h.cfg.Storage.MemoryPath
	resp := map[string]any{
		"user":       readFileCapped(filepath.Join(root, "USER.md")),
		"memory":     readFileCapped(filepath.Join(root, "MEMORY.md")),
		"dreams":     readFileCapped(filepath.Join(root, "DREAMS.md")),
		"time":       time.Now().Format(time.RFC3339),
		"user_limit": memory.UserCharLimit,
		"mem_limit":  memory.MemoryCharLimit,
	}
	writeJSON(w, resp)
}

var errNoMemoryConfig = &staticError{msg: "memory path not configured"}

// memoryFile maps the wire name of an editable memory file to its character
// cap. DREAMS.md is deliberately absent: it is the Dreamer's diary, written
// only by the dream engine.
func memoryFile(name string) (limit int, ok bool) {
	switch name {
	case "user":
		return memory.UserCharLimit, true
	case "memory":
		return memory.MemoryCharLimit, true
	}
	return 0, false
}

// putMemory serves PUT /api/memory/{file} — the editable half of the memory
// console (P1: memory 页产品化). USER.md and MEMORY.md are plain text the
// user may rewrite wholesale; the write goes through the Hermes store so the
// character caps are enforced and the file lands atomically. The dreams
// diary is read-only and rejected.
func (h *handler) putMemory(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	name := r.PathValue("file")
	limit, ok := memoryFile(name)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown memory file (editable: user, memory)"))
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}

	// Route through the Hermes store: it re-parses (§-separated entries,
	// whitespace-tolerant), enforces the cap with a clear error, and writes
	// atomically — a console edit can never tear the file the agent reads.
	mf := memory.ParseMem([]byte(req.Content))
	mf.Limit = limit
	hermes := memory.NewHermes(h.cfg.Storage.MemoryPath)
	var err error
	if name == "user" {
		err = hermes.SaveUser(mf)
	} else {
		err = hermes.SaveMemory(mf)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"file": name, "chars": mf.Chars(), "limit": limit})
}

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
