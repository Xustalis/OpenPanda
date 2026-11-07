// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// memoryHermes builds the Hermes store backing the memory console with the
// configured caps (config memory.limits), so the API enforces exactly the
// limits the daemon enforces — the console counter and the store can never
// disagree.
func (h *handler) memoryHermes() *memory.Hermes {
	var memPath string
	var limits memory.Limits
	h.readCfg(func(c *config.Config) {
		memPath = c.Storage.MemoryPath
		limits = memory.Limits{
			User:    c.Memory.Limits.User,
			Memory:  c.Memory.Limits.Memory,
			Project: c.Memory.Limits.Project,
		}
	})
	return memory.NewHermesWithLimits(memPath, limits)
}

// topicJSON is the wire form of one topics/<name>.md file in GET /api/memory.
type topicJSON struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Version string `json:"version,omitempty"`
}

// listDaily reads the warm-layer diary files (daily/*.md), newest first,
// for read-only browsing in the memory console. At most maxDailyView files
// ride along so a long history cannot bloat the response.
const maxDailyView = 31

func listDaily(dir string) ([]topicJSON, error) {
	entries, err := memory.ReadDirOrAbsent(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		info, err := e.Info()
		if os.IsNotExist(err) { // concurrent retention sweep
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		stem, ok := strings.CutSuffix(e.Name(), ".md")
		// Only real YYYY-MM-DD logs belong to the warm layer listing — a
		// stray README.md dropped into daily/ is not a diary entry.
		if !ok {
			continue
		}
		if _, err := time.Parse("2006-01-02", stem); err != nil {
			continue
		}
		names = append(names, stem)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > maxDailyView {
		names = names[:maxDailyView]
	}
	out := make([]topicJSON, 0, len(names))
	for _, name := range names {
		content, err := readDailyCapped(filepath.Join(dir, name+".md"))
		if os.IsNotExist(err) { // concurrent delete/archive
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, topicJSON{Name: name, Content: content})
	}
	return out, nil
}

// getMemory serves GET /api/memory — a read-only peek at every Hermes memory
// file (USER.md / MEMORY.md / topics/*.md / DREAMS.md) so the console can
// show what the agent remembers and what it dreamed (A3: all memory files are
// user-managed). Editable files are returned in full, even when an operator
// raises their cap above 64 KiB; only read-only daily previews are capped.
// A missing file is reported as an empty string. The edit
// caps ride along so the console can render a live character counter; they
// are the configured values (config memory.limits), not compile-time constants.
func (h *handler) getMemory(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	var root string
	var limits config.MemoryLimitsConfig
	h.readCfg(func(c *config.Config) {
		root = c.Storage.MemoryPath
		limits = c.Memory.Limits
	})
	hermes := h.memoryHermes()
	topics := []topicJSON{}
	if names, err := hermes.ListTopics(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	} else {
		for _, name := range names {
			content, version, err := readEditableMemory(filepath.Join(hermes.TopicsDir(), name+".md"))
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			topics = append(topics, topicJSON{Name: name, Content: content, Version: version})
		}
	}
	user, userVersion, err := readEditableMemory(filepath.Join(root, "USER.md"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	mem, memVersion, err := readEditableMemory(filepath.Join(root, "MEMORY.md"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	dreams := readDreams(filepath.Join(root, "DREAMS.md"))
	daily, err := listDaily(hermes.WarmDir())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp := map[string]any{
		"user":             user,
		"memory":           mem,
		"dreams":           dreams.Content,
		"dreams_truncated": dreams.Truncated,
		"versions": map[string]string{
			"user":   userVersion,
			"memory": memVersion,
			"dreams": dreams.Version,
		},
		"time":          time.Now().Format(time.RFC3339),
		"user_limit":    limits.User,
		"mem_limit":     limits.Memory,
		"project_limit": limits.Project,
		"topics":        topics,
		"daily":         daily,
	}
	writeJSON(w, resp)
}

var errNoMemoryConfig = &staticError{msg: "memory path not configured"}

// memoryFileLimit maps the wire name of an editable §-entry memory file to
// its configured character cap. DREAMS.md is handled separately by
// putMemory (free-form diary, byte cap).
func (h *handler) memoryFileLimit(name string) (limit int, ok bool) {
	h.readCfg(func(c *config.Config) {
		switch name {
		case "user":
			limit, ok = c.Memory.Limits.User, true
		case "memory":
			limit, ok = c.Memory.Limits.Memory, true
		}
	})
	return limit, ok
}

// decodeMemoryBody parses the shared {"content": "..."} edit body.
func decodeMemoryBody(r *http.Request) (content, version string, err error) {
	var req struct {
		Content string `json:"content"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", "", errors.New("invalid JSON body")
	}
	return req.Content, req.Version, nil
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
	content, version, err := decodeMemoryBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if name == "dreams" {
		if err := h.saveDreams(content, version); err != nil {
			writeMemoryError(w, err)
			return
		}
		writeJSON(w, map[string]any{"file": "dreams", "chars": len([]rune(content)), "limit": maxDreamsEdit})
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
	update := func(current *memory.MemFile) error {
		if err := checkMemoryVersion(current, version); err != nil {
			return err
		}
		*current = mf
		return nil
	}
	if name == "user" {
		err = hermes.UpdateUser(update)
	} else {
		err = hermes.UpdateMemory(update)
	}
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, map[string]any{"file": name, "chars": mf.Chars(), "limit": limit})
}

// saveDreams writes DREAMS.md verbatim (the diary is prose, not §-entries),
// capped at maxMemoryView bytes and written atomically like every other
// memory file — same 0700/0600 tightening too: the diary's promoted entries
// are memory content, so it is not world-readable either.
func (h *handler) saveDreams(content, version string) error {
	if len(content) > maxDreamsEdit {
		return errors.New("dreams diary exceeds the 2 MiB edit cap")
	}
	var root string
	h.readCfg(func(c *config.Config) { root = c.Storage.MemoryPath })
	return memory.NewDreamDiary(filepath.Join(root, "DREAMS.md")).Replace([]byte(content), version)
}

// checkMemoryVersion treats a missing version as create-only. Older callers
// can still create a fresh file, but cannot silently replace existing facts
// after the daemon or another editor has written them.
func checkMemoryVersion(current *memory.MemFile, version string) error {
	if version == "" {
		if len(current.Bytes()) != 0 {
			return memory.ErrConflict
		}
		return nil
	}
	if memory.Version(current.Bytes()) != version {
		return memory.ErrConflict
	}
	return nil
}

func writeMemoryError(w http.ResponseWriter, err error) {
	if errors.Is(err, memory.ErrConflict) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeErr(w, http.StatusBadRequest, err)
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
	content, version, err := decodeMemoryBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var limit int
	h.readCfg(func(c *config.Config) { limit = c.Memory.Limits.Memory })
	mf := memory.ParseMem([]byte(content))
	mf.Limit = limit
	hermes := h.memoryHermes()
	path, err := hermes.TopicPath(name)
	if err != nil || !withinDir(hermes.TopicsDir(), path) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid topic name"))
		return
	}
	if err := hermes.UpdateTopic(name, func(current *memory.MemFile) error {
		if err := checkMemoryVersion(current, version); err != nil {
			return err
		}
		*current = mf
		return nil
	}); err != nil {
		writeMemoryError(w, err)
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

// deleteMemoryDaily serves DELETE /api/memory/daily/{name} — removes one
// warm-layer diary file (daily/<name>.md). The name must be a single path
// segment (ValidateName + withinDir, same traversal guard as topics) and the
// target must be a regular file: the archive/ directory can never be deleted
// through this route. A missing file is a 404 so the console can tell a stale
// list from a removal.
func (h *handler) deleteMemoryDaily(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	name := r.PathValue("name")
	if err := memory.ValidateName(name); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid daily name"))
		return
	}
	dir := h.memoryHermes().WarmDir()
	path := filepath.Join(dir, name+".md")
	if !withinDir(dir, path) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid daily name"))
		return
	}
	if date, err := time.Parse("2006-01-02", name); err != nil || date.Format("2006-01-02") != name {
		writeErr(w, http.StatusNotFound, errors.New("no such daily file"))
		return
	}
	if err := memory.NewDaily(dir).Delete(name); err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, errors.New("no such daily file"))
		} else {
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, map[string]any{"daily": name, "status": "deleted"})
}

// pruneMemoryDaily serves POST /api/memory/daily/prune — applies the diary
// retention windows (30d archive, 90d delete, archive swept too) immediately
// instead of waiting for the Dreaming scheduler's next sweep.
func (h *handler) pruneMemoryDaily(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoMemoryConfig)
		return
	}
	d := memory.NewDaily(h.memoryHermes().WarmDir())
	if err := d.Prune(time.Now()); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"status": "pruned"})
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
	content, version, err := decodeMemoryBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	mf := memory.ParseMem([]byte(content))
	mf.Limit = h.projects.Limit()
	if err := h.projects.Update(name, func(current *memory.MemFile) error {
		if err := checkMemoryVersion(current, version); err != nil {
			return err
		}
		*current = mf
		return nil
	}); err != nil {
		writeMemoryError(w, err)
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

// readEditableMemory uses the same parsed snapshot as the store's conditional
// update, while returning all source text to the editor. A 64 KiB graph/daily
// preview must never be reused as a whole-file edit draft.
func readEditableMemory(path string) (content, version string, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", memory.Version(nil), nil
	}
	if err != nil {
		return "", "", err
	}
	return string(data), memory.Version(memory.ParseMem(data).Bytes()), nil
}

// readFileCapped returns the file's content capped at maxMemoryView bytes
// (with a truncation marker), or "" when unreadable.
const maxMemoryView = 64 << 10
const maxDreamsEdit = 2 << 20

type dreamsView struct {
	Content   string
	Version   string
	Truncated bool
}

func readDreams(path string) dreamsView {
	f, err := os.Open(path)
	if err != nil {
		return dreamsView{Version: memory.Version(nil)}
	}
	defer f.Close()
	// Keep only the editable prefix in memory, but hash the entire file so
	// even an append beyond the 2 MiB preview invalidates a stale editor.
	data, err := io.ReadAll(io.LimitReader(f, maxDreamsEdit+1))
	if err != nil {
		return dreamsView{Version: memory.Version(nil)}
	}
	h := sha256.New()
	_, _ = h.Write(data)
	if _, err := io.Copy(h, f); err != nil {
		return dreamsView{Version: memory.Version(nil)}
	}
	out := dreamsView{Version: hex.EncodeToString(h.Sum(nil))}
	if len(data) > maxDreamsEdit {
		out.Truncated = true
		data = data[:maxDreamsEdit]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	out.Content = string(data)
	return out
}

func readFileCapped(path string) string {
	content, _ := readDailyCapped(path)
	return content
}

func readDailyCapped(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxMemoryView+1))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}
	if len(data) > maxMemoryView {
		// Cut on a rune boundary: a mid-rune split would hand the JSON
		// encoder invalid UTF-8 and render as replacement characters.
		cut := maxMemoryView
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
		return string(data[:cut]) + "\n…（已截断）", nil
	}
	return string(data), nil
}
