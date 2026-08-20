package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// memoryHermes builds the Hermes store backing the memory console with the
// configured caps (config memory.limits), so the API enforces exactly the
// limits the daemon enforces — the console counter and the store can never
// disagree.
func (h *handler) memoryHermes() *memory.Hermes {
	return memory.NewHermesWithLimits(h.cfg.Storage.MemoryPath, memory.Limits{
		User:    h.cfg.Memory.Limits.User,
		Memory:  h.cfg.Memory.Limits.Memory,
		Project: h.cfg.Memory.Limits.Project,
	})
}

// topicJSON is the wire form of one topics/<name>.md file in GET /api/memory.
type topicJSON struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// listDaily reads the warm-layer diary files (daily/*.md), newest first,
// for read-only browsing in the memory console. At most maxDailyView files
// ride along so a long history cannot bloat the response.
const maxDailyView = 31

func listDaily(dir string) []topicJSON {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []topicJSON{}
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if stem, ok := strings.CutSuffix(e.Name(), ".md"); ok {
			names = append(names, stem)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > maxDailyView {
		names = names[:maxDailyView]
	}
	out := make([]topicJSON, 0, len(names))
	for _, name := range names {
		out = append(out, topicJSON{Name: name, Content: readFileCapped(filepath.Join(dir, name+".md"))})
	}
	return out
}

// getMemory serves GET /api/memory — a read-only peek at every Hermes memory
// file (USER.md / MEMORY.md / topics/*.md / DREAMS.md) so the console can
// show what the agent remembers and what it dreamed (A3: all memory files are
// user-managed). Each file is capped at 64 KiB; a missing file is reported as
// an empty string, not an error (fresh nodes start without memory). The edit
// caps ride along so the console can render a live character counter; they
// are the configured values (config memory.limits), not compile-time constants.
func (h *handler) getMemory(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	root := h.cfg.Storage.MemoryPath
	hermes := h.memoryHermes()
	topics := []topicJSON{}
	if names, err := hermes.ListTopics(); err == nil {
		for _, name := range names {
			topics = append(topics, topicJSON{
				Name:    name,
				Content: readFileCapped(filepath.Join(hermes.TopicsDir(), name+".md")),
			})
		}
	}
	resp := map[string]any{
		"user":          readFileCapped(filepath.Join(root, "USER.md")),
		"memory":        readFileCapped(filepath.Join(root, "MEMORY.md")),
		"dreams":        readFileCapped(filepath.Join(root, "DREAMS.md")),
		"time":          time.Now().Format(time.RFC3339),
		"user_limit":    h.cfg.Memory.Limits.User,
		"mem_limit":     h.cfg.Memory.Limits.Memory,
		"project_limit": h.cfg.Memory.Limits.Project,
		"topics":        topics,
		"daily":         listDaily(hermes.WarmDir()),
	}
	writeJSON(w, resp)
}

var errNoMemoryConfig = &staticError{msg: "memory path not configured"}

// memoryFileLimit maps the wire name of an editable §-entry memory file to
// its configured character cap. DREAMS.md is handled separately by
// putMemory (free-form diary, byte cap).
func (h *handler) memoryFileLimit(name string) (limit int, ok bool) {
	switch name {
	case "user":
		return h.cfg.Memory.Limits.User, true
	case "memory":
		return h.cfg.Memory.Limits.Memory, true
	}
	return 0, false
}

// decodeMemoryBody parses the shared {"content": "..."} edit body.
func decodeMemoryBody(r *http.Request) (string, error) {
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", errors.New("invalid JSON body")
	}
	return req.Content, nil
}

// putMemory serves PUT /api/memory/{file} — the editable memory files of the
// console (P1: memory 页产品化, C2: dreams opened up). USER.md and MEMORY.md
// are §-entry text the user may rewrite wholesale; the write goes through the
// Hermes store so the configured character caps are enforced and the file
// lands atomically. DREAMS.md is the Dreamer's diary — editable now too, but
// kept as free-form text (no § parsing) under a generous byte cap, and the
// console asks for confirmation before opening the editor. Topic files have
// their own route (PUT /api/memory/topics/{name}).
func (h *handler) putMemory(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	name := r.PathValue("file")
	content, err := decodeMemoryBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if name == "dreams" {
		if err := h.saveDreams(content); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"file": "dreams", "chars": len([]rune(content)), "limit": maxMemoryView})
		return
	}

	limit, ok := h.memoryFileLimit(name)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown memory file (editable: user, memory, dreams)"))
		return
	}

	// Route through the Hermes store: it re-parses (§-separated entries,
	// whitespace-tolerant), enforces the cap with a clear error, and writes
	// atomically — a console edit can never tear the file the agent reads.
	mf := memory.ParseMem([]byte(content))
	mf.Limit = limit
	hermes := h.memoryHermes()
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

// saveDreams writes DREAMS.md verbatim (the diary is prose, not §-entries),
// capped at maxMemoryView bytes and written atomically like every other
// memory file.
func (h *handler) saveDreams(content string) error {
	if len(content) > maxMemoryView {
		return errors.New("dreams diary exceeds the 64 KiB edit cap")
	}
	path := filepath.Join(h.cfg.Storage.MemoryPath, "DREAMS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return util.WriteFileAtomic(path, []byte(content), 0o644)
}

// putMemoryTopic serves PUT /api/memory/topics/{name} — create-or-rewrite one
// topic file (A3 multi-file memory). The {name} mux wildcard matches a single
// path segment and ValidateName rejects separators/dots again, so the name
// cannot escape the topics directory (directory traversal). Topic files share
// the §-entry format and the memory.limits.memory cap.
func (h *handler) putMemoryTopic(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	name := r.PathValue("name")
	if err := memory.ValidateName(name); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid topic name"))
		return
	}
	content, err := decodeMemoryBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	limit := h.cfg.Memory.Limits.Memory
	mf := memory.ParseMem([]byte(content))
	mf.Limit = limit
	hermes := h.memoryHermes()
	path, err := hermes.TopicPath(name)
	if err != nil || !withinDir(hermes.TopicsDir(), path) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid topic name"))
		return
	}
	if err := hermes.SaveTopic(name, mf); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"topic": name, "chars": mf.Chars(), "limit": limit})
}

// deleteMemoryTopic serves DELETE /api/memory/topics/{name} — removes one
// topic file. A missing topic is a 404 so the console can tell a typo from a
// removal.
func (h *handler) deleteMemoryTopic(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	name := r.PathValue("name")
	hermes := h.memoryHermes()
	path, err := hermes.TopicPath(name)
	if err != nil || !withinDir(hermes.TopicsDir(), path) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid topic name"))
		return
	}
	if _, err := os.Stat(path); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such topic"))
		return
	}
	if err := hermes.DeleteTopic(name); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"topic": name, "status": "deleted"})
}

// putProjectMemory serves PUT /api/projects/{name}/memory — the user edits a
// project's MEMORY.md wholesale (A3 web integration). Reuses the Projects
// store so the configured project cap, name validation (no traversal) and
// atomic write all apply exactly as on the daemon side.
func (h *handler) putProjectMemory(w http.ResponseWriter, r *http.Request) {
	if h.projects == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("projects not configured"))
		return
	}
	name := r.PathValue("name")
	if err := memory.ValidateName(name); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid project name"))
		return
	}
	content, err := decodeMemoryBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	mf := memory.ParseMem([]byte(content))
	mf.Limit = h.projects.Limit()
	if err := h.projects.Save(name, mf); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"project": name, "chars": mf.Chars(), "limit": mf.Limit})
}

// withinDir reports whether path stays inside dir after cleaning —
// defense in depth on top of ValidateName and the single-segment mux wildcard
// against directory traversal in topic names.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
