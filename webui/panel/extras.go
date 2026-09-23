package panel

// The smaller parity endpoints that round the console out to the TUI's
// surface: /api/doctor (self-check), /api/fs/read + /api/fs/files (the
// `/read` command and @file completion), /api/context (the `/context`
// snapshot of what the next ask will run with), and /api/cost (the `/cost`
// ledger rollup over delegation_metrics).

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/doctor"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// getDoctor serves GET /api/doctor — the same self-check suite `panda
// doctor` runs, returned as structured checks for the console to render.
func (h *handler) getDoctor(w http.ResponseWriter, r *http.Request) {
	checks := doctor.Run(h.configPath)
	writeJSON(w, map[string]any{
		"checks":   checks,
		"problems": doctor.Problems(checks),
	})
}

// allowedReadRoot reports whether abs may be read through /api/fs/read: the
// workspace root, the memory tree, or the per-project memory tree. The TUI's
// /read is unbounded because it runs in the user's own shell; the panel is a
// network surface, so reads stay inside the directories the node owns.
func (h *handler) allowedReadRoot(abs string) bool {
	if h.cfg == nil {
		return false
	}
	roots := []string{
		h.cfg.Storage.WorkPath,
		h.cfg.Storage.MemoryPath,
		h.cfg.Storage.ProjectsPath,
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		rabs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		// A root that is itself a symlink must resolve the same way the
		// request path does, or the Rel check below compares a resolved
		// child against an unresolved parent and wrongly rejects.
		if real, err := filepath.EvalSymlinks(rabs); err == nil {
			rabs = real
		}
		rel, err := filepath.Rel(rabs, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveFsPath normalizes a request path for the fs endpoints: ~ expands,
// and a relative path anchors at work_path — the composer's @token is typed
// relative to the workspace, and the panel's own cwd is wherever the daemon
// happened to start.
func (h *handler) resolveFsPath(reqPath string) (string, error) {
	if strings.HasPrefix(reqPath, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			reqPath = filepath.Join(home, strings.TrimPrefix(reqPath, "~"))
		}
	}
	if !filepath.IsAbs(reqPath) && h.cfg != nil && h.cfg.Storage.WorkPath != "" {
		reqPath = filepath.Join(h.cfg.Storage.WorkPath, reqPath)
	}
	return filepath.Abs(reqPath)
}

// fsFileEntry is one row in a /api/fs/files listing.
type fsFileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size,omitempty"`
	ModTS int64  `json:"mod_ts,omitempty"`
}

// listFiles serves GET /api/fs/files?path= — directories AND files, the
// browsing half of the composer's @file completion. Unlike
// /api/fs/directories (which only lists dirs for the project-workdir
// picker), this includes regular files and is rooted at work_path when no
// path is given.
func (h *handler) listFiles(w http.ResponseWriter, r *http.Request) {
	reqPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if reqPath == "" {
		if h.cfg != nil && h.cfg.Storage.WorkPath != "" {
			reqPath = h.cfg.Storage.WorkPath
		} else {
			reqPath, _ = os.Getwd()
		}
	}
	absPath, err := h.resolveFsPath(reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid path"))
		return
	}
	// A file path lists its own parent with the file's name as the filter —
	// the @ completion sends the partially-typed token's dirname.
	filter := ""
	if st, err := os.Stat(absPath); err == nil && !st.IsDir() {
		filter = filepath.Base(absPath)
		absPath = filepath.Dir(absPath)
	} else if err != nil {
		// Nonexistent path: treat the last segment as a prefix filter.
		filter = filepath.Base(absPath)
		absPath = filepath.Dir(absPath)
	}
	// Listing follows the same root boundary as reading: the menu exists to
	// pick files /api/fs/read can actually attach, so browsing outside the
	// owned roots only advertises paths that will fail on send.
	if real, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = real
	}
	if !h.allowedReadRoot(absPath) {
		writeErr(w, http.StatusForbidden, errors.New("path is outside the workspace and memory roots"))
		return
	}
	entries, err := os.ReadDir(absPath)
	if err != nil {
		writeJSON(w, map[string]any{"current": absPath, "entries": []fsFileEntry{}})
		return
	}
	out := make([]fsFileEntry, 0, len(entries))
	lowerFilter := strings.ToLower(filter)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(filter, ".") {
			continue // hidden files only surface when the filter starts with a dot
		}
		if lowerFilter != "" && !strings.HasPrefix(strings.ToLower(name), lowerFilter) {
			continue
		}
		fe := fsFileEntry{Name: name, Path: filepath.Join(absPath, name), Dir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			fe.Size = info.Size()
			fe.ModTS = info.ModTime().Unix()
		}
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir // directories first
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	const maxEntries = 200
	if len(out) > maxEntries {
		out = out[:maxEntries]
	}
	writeJSON(w, map[string]any{"current": absPath, "entries": out})
}

// maxReadBytes caps /api/fs/read — same budget the REPL's @file attach uses
// (a prompt is a context window, not a pipe).
const maxReadBytes = 32 * 1024

// readFile serves GET /api/fs/read?path= — the web /read: file content under
// an allowed root, capped at maxReadBytes with a truncation flag.
func (h *handler) readFile(w http.ResponseWriter, r *http.Request) {
	reqPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if reqPath == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	absPath, err := h.resolveFsPath(reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid path"))
		return
	}
	// Resolve symlinks before the root check so a link inside the workspace
	// cannot point outside it.
	if real, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = real
	}
	if !h.allowedReadRoot(absPath) {
		writeErr(w, http.StatusForbidden, errors.New("path is outside the workspace and memory roots"))
		return
	}
	st, err := os.Stat(absPath)
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, errors.New("no such file"))
		return
	}
	f, err := os.Open(absPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("read failed"))
		return
	}
	defer f.Close()
	// One byte past the cap is the truncation signal — a multi-GB file must
	// not land in memory just to be cut down to 32 KiB.
	data, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("read failed"))
		return
	}
	truncated := false
	if len(data) > maxReadBytes {
		data, truncated = data[:maxReadBytes], true
	}
	writeJSON(w, map[string]any{
		"path":      absPath,
		"content":   string(data),
		"size":      st.Size(),
		"truncated": truncated,
	})
}

// getContext serves GET /api/context — the `/context` snapshot: which model,
// work dir, memory manifest, active project and card state the next ask runs
// with. Authorization stays client-side (the composer checkbox is per-ask),
// so it is deliberately absent here.
func (h *handler) getContext(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	if h.cfg != nil {
		out["work_dir"] = h.cfg.Storage.WorkPath
		out["node_name"] = h.cfg.Node.Name
	}
	mc := h.engineModel()
	out["model"] = modelEntryOf(mc, mc)
	if h.cfg != nil && h.cfg.Storage.MemoryPath != "" {
		hermes := memory.NewHermesWithLimits(h.cfg.Storage.MemoryPath, memory.Limits{})
		if files, err := hermes.Files(); err == nil {
			out["memory_files"] = len(files)
		}
	}
	if h.projectStore != nil {
		if name, err := h.projectStore.Active(); err == nil && name != "" {
			out["project"] = name
			if p, err := h.projectStore.Get(name); err == nil && p.WorkDir != "" {
				out["project_work_dir"] = p.WorkDir
			}
		}
	}
	out["has_card"] = h.cardPath() != ""
	writeJSON(w, out)
}

// costJSON is the wire form of GET /api/cost — the `/cost` rollup over the
// delegation_metrics ledger, which both the commander model's own usage and
// adapter delegations write into.
type costJSON struct {
	TotalTokens  int64              `json:"total_tokens"`
	TotalCostUSD float64            `json:"total_cost_usd"`
	Calls        int                `json:"calls"`
	SuccessRate  float64            `json:"success_rate"`
	ByExecutor   []costExecutorJSON `json:"by_executor"`
	Since        int64              `json:"since"` // earliest row ts, unix seconds
	Unavailable  bool               `json:"unavailable,omitempty"`
}

type costExecutorJSON struct {
	Executor  string  `json:"executor"`
	Calls     int     `json:"calls"`
	Tokens    int64   `json:"tokens"`
	CostUSD   float64 `json:"cost_usd"`
	Successes int     `json:"successes"`
}

// getCost serves GET /api/cost — aggregated token spend over the
// delegation_metrics table: totals, success rate, and a per-executor
// breakdown so the console can show "entry:kimi-latest" beside "claude_code".
func (h *handler) getCost(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("store not configured"))
		return
	}
	metrics, err := h.store.ListDelegationMetrics(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("load metrics failed"))
		return
	}
	type agg struct {
		calls     int
		tokens    int64
		cost      float64
		successes int
	}
	byExec := map[string]*agg{}
	res := costJSON{Since: time.Now().Unix()}
	for _, m := range metrics {
		res.Calls++
		if m.Tokens.Valid {
			res.TotalTokens += m.Tokens.Int64
		}
		if m.Cost.Valid {
			res.TotalCostUSD += m.Cost.Float64
		}
		if m.Success {
			res.SuccessRate++
		}
		if m.CreatedAt > 0 && m.CreatedAt < res.Since {
			res.Since = m.CreatedAt
		}
		a := byExec[m.Executor]
		if a == nil {
			a = &agg{}
			byExec[m.Executor] = a
		}
		a.calls++
		if m.Tokens.Valid {
			a.tokens += m.Tokens.Int64
		}
		if m.Cost.Valid {
			a.cost += m.Cost.Float64
		}
		if m.Success {
			a.successes++
		}
	}
	if res.Calls > 0 {
		res.SuccessRate = res.SuccessRate / float64(res.Calls)
	} else {
		res.Since = 0
	}
	for execID, a := range byExec {
		res.ByExecutor = append(res.ByExecutor, costExecutorJSON{
			Executor: execID, Calls: a.calls, Tokens: a.tokens, CostUSD: a.cost, Successes: a.successes,
		})
	}
	sort.Slice(res.ByExecutor, func(i, j int) bool { return res.ByExecutor[i].Tokens > res.ByExecutor[j].Tokens })
	writeJSON(w, res)
}
