// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The smaller parity endpoints that round the console out to the TUI's
// surface: /api/doctor (self-check), /api/fs/read + /api/fs/files (the
// `/read` command and @file completion), /api/context (the `/context`
// snapshot of what the next ask will run with), and /api/cost (the `/cost`
// ledger rollup over delegation_metrics).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/doctor"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/projects"
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
// workspace root, the memory tree, the per-project memory tree, or any
// registered project's work dir. The TUI's /read is unbounded because it runs
// in the user's own shell; the panel is a network surface, so reads stay
// inside the directories the node owns.
//
// The project work dirs are the ones that matter in practice. A thread's files
// live in its project — the composer's file browser is meant to offer "what
// this thread's agent can act on" — so leaving them out of the allow-list left
// the browser able to list nothing but the node's own openpanda-data tree, and
// made @file fail on every path the user actually works on.
func (h *handler) allowedReadRoot(abs string) bool {
	if h.cfg == nil {
		return false
	}
	var roots []string
	h.readCfg(func(c *config.Config) {
		roots = []string{c.Storage.WorkPath, c.Storage.MemoryPath, c.Storage.ProjectsPath}
	})
	if h.projectStore != nil {
		if list, err := h.projectStore.List(); err == nil {
			for _, p := range list {
				if safeReadRoot(p.WorkDir) {
					roots = append(roots, p.WorkDir)
				}
			}
		}
	}
	for _, root := range roots {
		if underRoot(root, abs) {
			return true
		}
	}
	return false
}

// safeReadRoot admits a project's work dir to the read allow-list only while it
// still passes the breadth rule that projects.Create/Update apply on write.
//
// The write gate is not enough on its own. A row written by an older binary —
// before the rule existed — keeps its work dir across the upgrade untouched, and
// a work dir of "/" or the home directory would make /api/fs/read a whole-disk
// reader for every client of the panel. The read side therefore re-checks each
// row it did not just validate itself; a rejected row simply contributes no
// root, so the project's files stay unreadable through the panel until its work
// dir is fixed.
func safeReadRoot(workDir string) bool {
	return workDir != "" && projects.ValidateWorkDir(workDir) == nil
}

// underRoot reports whether abs sits inside root. A root that is itself a
// symlink must resolve the same way the request path does, or the Rel check
// compares a resolved child against an unresolved parent and wrongly rejects.
//
// Both sides are resolved for the same reason. On Windows the OS temp tree is
// commonly reached through an 8.3 short name (`C:\Users\RUNNER~1\…`), and
// EvalSymlinks expands the root to its long form while the request path keeps
// the short one it was built from — every genuine temp file then compares as
// outside and the preview 403s. A path that does not exist cannot be resolved,
// so its parent is: that keeps "missing file" a 404 instead of turning it into
// a boundary refusal.
func underRoot(root, abs string) bool {
	if root == "" {
		return false
	}
	rabs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(rabs); err == nil {
		rabs = real
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	} else if real, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(real, filepath.Base(abs))
	}
	rel, err := filepath.Rel(rabs, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// allowedImageRoot is allowedReadRoot widened by the OS temp tree.
//
// Agents park screenshots in a scratch directory — a verification run writes
// `%TEMP%/panda_verify/t2-eating.png` and then `Read`s it — and those are
// exactly the images a transcript wants to show. The extra root is scoped to
// this endpoint (images only, extension-gated, size-capped): /api/fs/read
// keeps its stricter list.
func (h *handler) allowedImageRoot(abs string) bool {
	if h.allowedReadRoot(abs) {
		return true
	}
	return underRoot(os.TempDir(), abs)
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
	if !filepath.IsAbs(reqPath) {
		h.readCfg(func(c *config.Config) {
			if c.Storage.WorkPath != "" {
				reqPath = filepath.Join(c.Storage.WorkPath, reqPath)
			}
		})
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
		h.readCfg(func(c *config.Config) { reqPath = c.Storage.WorkPath })
		if reqPath == "" {
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

// maxImageBytes caps /api/fs/image — a transcript preview, not a file server.
// 16 MiB holds a full-resolution screenshot; a frame grab that big is already
// beyond what an inline thumbnail can usefully show.
const maxImageBytes = 16 << 20

// imageTypes is the extension → wire-type table the preview endpoint serves.
// SVG is deliberately absent: it is a script-bearing document, and this
// endpoint answers with the file's own bytes under the same content type.
var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".avif": "image/avif",
}

// getImage serves GET /api/fs/image?path= — the raw bytes behind an inline
// transcript preview. The timeline shows a tool row's picture instead of its
// path; the browser cannot put the Bearer token on an <img src>, so the
// console fetches this and hands the blob to the <img>.
//
// The type is taken from the extension and the bytes are sniffed only to
// catch a mislabelled file; either way the response is served with
// X-Content-Type-Options: nosniff, so a text file renamed .png cannot be
// talked into executing as HTML.
func (h *handler) getImage(w http.ResponseWriter, r *http.Request) {
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
	// Resolve symlinks before the root check so a link cannot point outside.
	if real, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = real
	}
	ctype, ok := imageTypes[strings.ToLower(filepath.Ext(absPath))]
	if !ok {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("not a previewable image type"))
		return
	}
	if !h.allowedImageRoot(absPath) {
		writeErr(w, http.StatusForbidden, errors.New("path is outside the workspace, memory and temp roots"))
		return
	}
	st, err := os.Stat(absPath)
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, errors.New("no such file"))
		return
	}
	if st.Size() > maxImageBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, errors.New("image is too large to preview"))
		return
	}
	f, err := os.Open(absPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("read failed"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=60")
	// ServeContent brings range/HEAD along for free and seeks the file itself.
	http.ServeContent(w, r, filepath.Base(absPath), st.ModTime(), f)
}

// openInFileManager launches the desktop's file browser at dir. It is
// deliberately fire-and-forget: Explorer returns a non-zero status even when
// it succeeds, so waiting on the child would report a false failure. A var,
// not a func, so tests can observe the call without opening a window.
var openInFileManager = func(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}

// revealPath serves POST /api/fs/reveal — "open this folder" from the
// history's directory menu. The allow-list is the same one /api/fs/read
// enforces: the panel is a network surface, and handing an arbitrary path to
// the desktop shell is a bigger favour than reading a file. A missing
// directory is a 404 rather than a silent no-op, because the console shows
// the failure instead of a menu that appears to work.
func (h *handler) revealPath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	reqPath := strings.TrimSpace(req.Path)
	if reqPath == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	absPath, err := h.resolveFsPath(reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid path"))
		return
	}
	// Resolve before the root check so a symlink inside the workspace cannot
	// point the file manager outside it.
	if real, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = real
	}
	if !h.allowedReadRoot(absPath) {
		writeErr(w, http.StatusForbidden, errors.New("path is outside the workspace and memory roots"))
		return
	}
	st, err := os.Stat(absPath)
	if err != nil || !st.IsDir() {
		writeErr(w, http.StatusNotFound, errors.New("no such directory"))
		return
	}
	if err := openInFileManager(absPath); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("could not open the file manager"))
		return
	}
	writeJSON(w, map[string]any{"opened": absPath})
}

// getContext serves GET /api/context — the `/context` snapshot: which model,
// work dir, memory manifest, active project and card state the next ask runs
// with. Authorization stays client-side (the composer checkbox is per-ask),
// so it is deliberately absent here.
func (h *handler) getContext(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	var workDir, nodeName, memPath string
	h.readCfg(func(c *config.Config) {
		workDir = c.Storage.WorkPath
		nodeName = c.Node.Name
		memPath = c.Storage.MemoryPath
	})
	if h.cfg != nil {
		out["work_dir"] = workDir
		out["node_name"] = nodeName
	}
	mc := h.engineModel()
	out["model"] = modelEntryOf(mc, mc)
	if memPath != "" {
		hermes := memory.NewHermesWithLimits(memPath, memory.Limits{})
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
// breakdown so the console can show "entry:kimi-k3" beside "claude_code".
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
