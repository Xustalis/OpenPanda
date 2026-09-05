package panel

// The project surface for the console: the same first-class project the CLI
// gained — metadata, a work directory, and the current one — rather than a bare
// list of memory-file names.
//
// The console used to show project *names* only (GET /api/projects returned
// []string from the memory layer), and could create one by seeding its memory
// file. Everything else a project needs — where its files are, which one you are
// working in, renaming it, removing it — existed only in the CLI. That gap is
// what this file closes; every handler here goes through the same
// internal/projects store the CLI uses, so the two surfaces cannot disagree
// about what a project is or which one is active.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/memory"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
)

// projectView is one project as the console sees it: the metadata row plus the
// two things the row does not know — how big its memory is, and whether it is the
// one currently entered.
type projectView struct {
	projectstore.Project
	Active   bool `json:"active"`
	Entries  int  `json:"memory_entries"`
	Chars    int  `json:"memory_chars"`
	Sessions int  `json:"sessions"`
}

// projectStoreOrErr guards the handlers that need the metadata table. A database
// from before the projects migration has no table, so the console degrades to the
// memory-only view rather than erroring on every request.
func (h *handler) projectStoreOrErr(w http.ResponseWriter) (*projectstore.Store, bool) {
	if h.projectStore == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("project store not configured"))
		return nil, false
	}
	return h.projectStore, true
}

// projectMemorySize reads a project's memory counts, or zeroes when it has none.
func (h *handler) projectMemorySize(name string) (entries, chars int) {
	if h.projects == nil {
		return 0, 0
	}
	m, err := h.projects.Load(name)
	if err != nil {
		return 0, 0
	}
	return len(m.Entries), m.Chars()
}

// projectViews assembles the full listing, folding in projects that exist only as
// a memory file. They predate the metadata table, and a listing that showed the
// table alone would report that work the user can still see does not exist.
func (h *handler) projectViews(store *projectstore.Store) ([]projectView, string, error) {
	list, err := store.List()
	if err != nil {
		return nil, "", err
	}
	known := make(map[string]bool, len(list))
	for _, p := range list {
		known[p.Name] = true
	}
	if h.projects != nil {
		if names, lerr := h.projects.List(); lerr == nil {
			for _, n := range names {
				if known[n] {
					continue
				}
				if p, aerr := store.EnsureFromName(n); aerr == nil {
					list = append(list, p)
				}
			}
		}
	}
	active, err := store.Active()
	if err != nil {
		return nil, "", err
	}
	out := make([]projectView, 0, len(list))
	for _, p := range list {
		entries, chars := h.projectMemorySize(p.Name)
		var sessCount int
		if h.sessions != nil {
			if sList, err := h.sessions.ListByProject(p.Name); err == nil {
				sessCount = len(sList)
			}
		}
		out = append(out, projectView{
			Project:  p,
			Active:   p.Name == active,
			Entries:  entries,
			Chars:    chars,
			Sessions: sessCount,
		})
	}
	return out, active, nil
}

// getProject serves GET /api/projects/{name}.
func (h *handler) getProject(w http.ResponseWriter, r *http.Request) {
	store, ok := h.projectStoreOrErr(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	p, err := store.Get(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such project"))
		return
	}
	active, _ := store.Active()
	entries, chars := h.projectMemorySize(name)
	var sessCount int
	if h.sessions != nil {
		if sList, err := h.sessions.ListByProject(name); err == nil {
			sessCount = len(sList)
		}
	}
	writeJSON(w, projectView{
		Project:  p,
		Active:   p.Name == active,
		Entries:  entries,
		Chars:    chars,
		Sessions: sessCount,
	})
}

// patchProjectRequest is the body of PATCH /api/projects/{name}: a rename, a work
// directory, a description, or any combination. Absent fields are left alone;
// an explicit empty work_dir clears it, which is how a project gives up its tree.
type patchProjectRequest struct {
	Name        *string `json:"name,omitempty"`
	WorkDir     *string `json:"work_dir,omitempty"`
	Description *string `json:"description,omitempty"`
}

// patchProject serves PATCH /api/projects/{name} — rename and/or edit metadata.
// A rename moves the memory file and the tasks too, exactly as the CLI's
// `panda project rename` does: tasks carry the project name as data, so renaming
// without them would leave them pointing at a project that no longer exists.
func (h *handler) patchProject(w http.ResponseWriter, r *http.Request) {
	store, ok := h.projectStoreOrErr(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var req patchProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	cur, err := store.Get(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such project"))
		return
	}

	if req.Name != nil {
		newName := strings.TrimSpace(*req.Name)
		if newName != name {
			if verr := projectstore.ValidateName(newName); verr != nil {
				writeErr(w, http.StatusBadRequest, errors.New("invalid project name"))
				return
			}
			if _, rerr := store.Rename(name, newName); rerr != nil {
				writeErr(w, http.StatusConflict, errors.New("rename failed"))
				return
			}
			h.renameProjectMemory(name, newName)
			if h.sessions != nil {
				_, _ = h.sessions.RenameProject(name, newName)
			}
			if h.store != nil {
				if _, terr := h.store.RenameProject(r.Context(), name, newName); terr != nil {
					// The metadata already moved; a task that kept the old name is
					// visible and fixable, so report success with the rename done.
					writeErr(w, http.StatusInternalServerError, errors.New("tasks not renamed"))
					return
				}
			}
			name = newName
			cur, _ = store.Get(name)
		}
	}
	if req.WorkDir != nil || req.Description != nil {
		workDir, desc := cur.WorkDir, cur.Description
		if req.WorkDir != nil {
			workDir = strings.TrimSpace(*req.WorkDir)
		}
		if req.Description != nil {
			desc = strings.TrimSpace(*req.Description)
		}
		if _, uerr := store.Update(name, workDir, desc); uerr != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("update failed"))
			return
		}
	}
	p, err := store.Get(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("reload failed"))
		return
	}
	active, _ := store.Active()
	entries, chars := h.projectMemorySize(name)
	var sessionCount int
	if h.sessions != nil {
		if sList, err := h.sessions.ListByProject(name); err == nil {
			sessionCount = len(sList)
		}
	}
	writeJSON(w, projectView{Project: p, Active: p.Name == active, Entries: entries, Chars: chars, Sessions: sessionCount})
}

// renameProjectMemory moves a project's memory directory. Copy-then-drop rather
// than move, so a failure leaves the old file intact instead of losing the memory.
func (h *handler) renameProjectMemory(oldName, newName string) {
	if h.projects == nil {
		return
	}
	m, err := h.projects.Load(oldName)
	if err != nil {
		return
	}
	if err := h.projects.Save(newName, m); err != nil {
		return
	}
	_ = h.projects.Delete(oldName)
}

// deleteProject serves DELETE /api/projects/{name}. ?keep_memory=1 leaves the
// memory file. The work directory is never touched — removing a project is
// bookkeeping, and deleting the user's tree would be the one irreversible thing
// this endpoint could do.
func (h *handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	store, ok := h.projectStoreOrErr(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if _, err := store.Get(name); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such project"))
		return
	}
	active, _ := store.Active()
	if active == name {
		_ = store.ClearActive()
		h.bindEngineProject(store, "")
	}
	if err := store.Delete(name); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("remove failed"))
		return
	}
	keepMemory := r.URL.Query().Get("keep_memory") == "1"
	if !keepMemory && h.projects != nil {
		_ = h.projects.Delete(name)
	}

	sessionsAction := r.URL.Query().Get("sessions")
	if sessionsAction == "" {
		sessionsAction = "keep"
	}
	sessionsAffected := 0
	if h.sessions != nil {
		if list, err := h.sessions.ListByProject(name); err == nil {
			sessionsAffected = len(list)
			for _, s := range list {
				if sessionsAction == "delete" {
					if h.worktrees != nil {
						_ = h.worktrees.Remove(r.Context(), s.ID)
					}
					_ = h.sessions.Delete(s.ID)
				} else {
					_ = h.sessions.SetProject(s.ID, "")
				}
			}
		}
	}

	writeJSON(w, map[string]any{
		"removed":           name,
		"memory_kept":       keepMemory,
		"sessions_action":   sessionsAction,
		"sessions_affected": sessionsAffected,
	})
}

// enterProject serves POST /api/projects/{name}/enter — makes it the current
// project. It writes the same pointer `panda project enter` writes, so entering a
// project in the console is entering it for the next CLI ask as well.
func (h *handler) enterProject(w http.ResponseWriter, r *http.Request) {
	store, ok := h.projectStoreOrErr(w)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if _, err := store.Get(name); err != nil {
		// A project that exists only as a memory file is adopted rather than
		// refused: the user can see it in the list, so failing to enter it would
		// read as a bug.
		if _, aerr := store.EnsureFromName(name); aerr != nil {
			writeErr(w, http.StatusNotFound, errors.New("no such project"))
			return
		}
	}
	if err := store.SetActive(name); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("enter failed"))
		return
	}
	h.bindEngineProject(store, name)
	p, _ := store.Get(name)
	entries, chars := h.projectMemorySize(name)
	writeJSON(w, projectView{Project: p, Active: true, Entries: entries, Chars: chars})
}

// exitProject serves POST /api/projects/exit.
func (h *handler) exitProject(w http.ResponseWriter, r *http.Request) {
	store, ok := h.projectStoreOrErr(w)
	if !ok {
		return
	}
	was, _ := store.Active()
	if err := store.ClearActive(); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("exit failed"))
		return
	}
	h.bindEngineProject(store, "")
	writeJSON(w, map[string]string{"left": was})
}

// bindEngineProject tells the live engine which project its next ask belongs to,
// so an ask issued from the console lands in the project the console is showing.
func (h *handler) bindEngineProject(store *projectstore.Store, name string) {
	eng := h.currentEngine()
	if eng == nil {
		return
	}
	if name == "" {
		eng.SetProject("", "")
		return
	}
	dir := ""
	if p, err := store.Get(name); err == nil {
		dir = p.WorkDir
	}
	eng.SetProject(name, dir)
}

// seedProjectMemory creates a project's memory file, the memory layer's notion of
// "this project exists".
func (h *handler) seedProjectMemory(name string) error {
	if h.projects == nil {
		return nil
	}
	return h.projects.Save(name, memory.MemFile{Limit: h.projects.Limit()})
}
