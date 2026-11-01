// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The transcript's read-only file viewer.
//
// /api/fs/read exists for @file attachment and is the wrong shape for a viewer:
// it answers 32 KiB of one file as a JSON string, with no line numbers and no
// way to ask for a window. This endpoint is the viewer's: it resolves a path the
// way the thread means it (relative to that session's worktree), can return just
// the lines around a focus line, and caps at a megabyte rather than a prompt
// budget.
//
// It is strictly read-only and shares /api/fs/read's root allow-list. A session
// worktree that is not a registered project still resolves — the agent works
// there, so the transcript must be able to show what it touched — but only
// through the same breadth rule projects are held to on write.

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"net/http"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// maxViewBytes caps /api/fs/file. A viewer that streams a whole source file has
// no business holding more than this in memory; past it the endpoint answers a
// window when a line was asked for, and a refusal when one was not.
const maxViewBytes = 1 << 20

// viewContextLines is how much of the file sits around the requested line when
// no count is given — enough to read the function, not so much that the drawer
// has to scroll on arrival.
const viewContextLines = 40

// maxViewContext bounds the caller's own count so a hostile query cannot make
// the endpoint read a whole gigabyte one "context" at a time.
const maxViewContext = 4000

// binarySniffBytes is how far into the file the NUL check looks. Source files
// never carry a NUL in their first 8 KiB; a compressed or compiled artifact
// almost always does.
const binarySniffBytes = 8000

// fileView is the response body. Start/End are 1-based line numbers of the
// returned window, and TotalLines is -1 when the file was too large to count.
type fileView struct {
	Path       string `json:"path"`
	Rel        string `json:"rel,omitempty"`
	Content    string `json:"content"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	TotalLines int    `json:"total_lines"`
	Line       int    `json:"line,omitempty"`
	Truncated  bool   `json:"truncated"`
	Binary     bool   `json:"binary"`
	Size       int64  `json:"size"`
	Ext        string `json:"ext,omitempty"`
}

// viewFile serves GET /api/fs/file?path=&session=&line=&count=.
func (h *handler) viewFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	reqPath := strings.TrimSpace(q.Get("path"))
	if reqPath == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}

	// The thread's own workspace is the base for a relative path: a path in a
	// chat is the path the agent used, and the agent runs in the session's
	// worktree, not in whatever directory the daemon started in.
	workDir := h.sessionWorkDir(q.Get("session"))
	if workDir != "" {
		// Resolve the base once, up front: the path below is resolved before
		// the root check, and comparing a resolved child against an unresolved
		// parent reports every genuine hit as outside (on macOS a temp dir is
		// reached through /var, a symlink to /private/var).
		if real, err := filepath.EvalSymlinks(workDir); err == nil {
			workDir = real
		}
	}
	abs, err := h.resolveViewPath(reqPath, workDir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid path"))
		return
	}
	// Resolve symlinks before the root check, so a link inside the workspace
	// cannot point outside it.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if !h.allowedViewPath(abs, workDir) {
		writeErr(w, http.StatusForbidden, errors.New("path is outside the workspace and memory roots"))
		return
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, errors.New("no such file"))
		return
	}

	line := atoiOr(q.Get("line"), 0)
	if line < 0 {
		line = 0
	}
	count := atoiOr(q.Get("count"), 0)
	if count <= 0 {
		count = viewContextLines
	}
	count = min(count, maxViewContext)

	view, err := readFileView(abs, st.Size(), line, count)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("read failed"))
		return
	}
	if workDir != "" {
		if rel, err := filepath.Rel(workDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			view.Rel = filepath.ToSlash(rel)
		}
	}
	writeJSON(w, view)
}

// sessionWorkDir returns the workspace a session runs in, or "" when the id is
// unknown or the thread has no worktree of its own (it then runs in the shared
// work path, which resolveViewPath falls back to).
func (h *handler) sessionWorkDir(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || h.sessions == nil {
		return ""
	}
	sess, err := h.sessions.Get(id)
	if err != nil || sess.Worktree == "" {
		return ""
	}
	if !safeReadRoot(sess.Worktree) {
		return ""
	}
	return sess.Worktree
}

// resolveViewPath turns a request path into an absolute one. ~ expands; a
// relative path anchors at the session worktree when there is one, and at the
// node's work path otherwise.
func (h *handler) resolveViewPath(reqPath, workDir string) (string, error) {
	if strings.HasPrefix(reqPath, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			reqPath = filepath.Join(home, strings.TrimPrefix(reqPath, "~"))
		}
	}
	if !filepath.IsAbs(reqPath) {
		base := workDir
		if base == "" {
			h.readCfg(func(c *config.Config) {
				base = c.Storage.WorkPath
			})
		}
		if base != "" {
			reqPath = filepath.Join(base, reqPath)
		}
	}
	return filepath.Abs(reqPath)
}

// allowedViewPath is allowedReadRoot widened by the session's own worktree.
//
// The widening is what makes "the file this thread's agent just touched" open
// at all: a worktree lives inside the repository, and a repository the user
// runs a session in is not necessarily one they registered as a project. The
// worktree still has to pass the same breadth rule a project work dir does
// (safeReadRoot), so a corrupted or hand-edited session cannot turn this into a
// whole-disk reader.
func (h *handler) allowedViewPath(abs, workDir string) bool {
	if h.allowedReadRoot(abs) {
		return true
	}
	return workDir != "" && underRoot(workDir, abs)
}

// readFileView reads the requested window. A file that fits the cap is read
// whole — that is what lets the response carry real line numbers and a total —
// while a larger one is streamed just far enough to fill the window.
func readFileView(abs string, size int64, line, count int) (*fileView, error) {
	view := &fileView{
		Path:       abs,
		Line:       line,
		Start:      1,
		TotalLines: -1,
		Size:       size,
		Ext:        strings.ToLower(strings.TrimPrefix(filepath.Ext(abs), ".")),
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if binarySniff(f) {
		view.Binary = true
		return view, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	if size <= maxViewBytes {
		data, err := io.ReadAll(io.LimitReader(f, maxViewBytes+1))
		if err != nil {
			return nil, err
		}
		text := string(data)
		lines := splitLines(text)
		view.TotalLines = len(lines)
		start, end := viewWindow(len(lines), line, count)
		// A window wider than the file (or any window over an empty one) ends
		// where the file does; the slice below must never assume otherwise.
		end = min(end, len(lines))
		start = min(start, end+1)
		if start > end {
			start = end + 1
		}
		view.Start, view.End = start, end
		view.Content = strings.Join(lines[start-1:end], "\n")
		return view, nil
	}

	// Larger than the cap: stream to the window and stop. Line counting would
	// mean reading the whole file, so the total stays unknown.
	start, end := viewWindow(0, line, count)
	if line <= 0 {
		// No focus line and no room to return the file: hand back the head and
		// let the console say the file is too large to show whole.
		view.Truncated = true
		start, end = 1, count
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var kept []string
	n := 0
	for sc.Scan() {
		n++
		if n < start {
			continue
		}
		if n > end {
			break
		}
		kept = append(kept, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	view.Start = start
	view.End = start + len(kept) - 1
	if len(kept) == 0 {
		view.End = start - 1
	}
	view.Content = strings.Join(kept, "\n")
	view.Truncated = line <= 0 || size > maxViewBytes
	return view, nil
}

// viewWindow is the 1-based inclusive line window for a request: the file
// around `line` when one was given, the head of the file otherwise. total is 0
// when the length is unknown, in which case the window is not clamped to it.
func viewWindow(total, line, count int) (start, end int) {
	if line <= 0 {
		return 1, count
	}
	start = max(1, line-count)
	end = line + count
	if total > 0 {
		end = min(end, total)
		start = min(start, max(1, end))
	}
	return start, end
}

// splitLines splits on newlines without inventing a trailing empty line for a
// file that ends with one.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.TrimSuffix(text, "\n")
	return strings.Split(text, "\n")
}

// binarySniff reports whether the head of the file looks binary. It restores
// nothing — callers seek back themselves.
func binarySniff(f *os.File) bool {
	buf := make([]byte, binarySniffBytes)
	n, err := f.Read(buf)
	if n == 0 && err != nil {
		return false
	}
	return strings.IndexByte(string(buf[:n]), 0) >= 0
}

// atoiOr parses s, returning def when it is absent or malformed. A viewer is
// not the place to fail a request over a stray query parameter.
func atoiOr(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
