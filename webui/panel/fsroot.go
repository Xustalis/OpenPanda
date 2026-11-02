// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// Rooted file access for the read-only file endpoints.
//
// /api/fs/read, /api/fs/file, /api/fs/image and /api/fs/files used to resolve
// symlinks, compare the result against the allow-list, and then open the path
// again. Between that check and the open, a process that can write the
// workspace (an agent, a build script, a peer task) could swap the last
// component for a symlink and make the viewer read a file outside every root.
//
// os.Root closes that window: the directory is opened once, and every lookup
// from it refuses to leave the tree, even through a symlink that is created
// after the check. The allow-list still runs first for its error messages and
// its breadth rule; the Root is the enforcement.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// errOutsideReadRoots is returned when no configured root contains the path.
// The HTTP layer maps it to 403; a missing file inside a root is a 404.
var errOutsideReadRoots = errors.New("path is outside the workspace and memory roots")

// readRoots is the allow-list in directory form: the node's own trees, every
// project work dir that still passes the breadth rule, and any caller-supplied
// extra roots (a session worktree, the OS temp tree for images).
func (h *handler) readRoots(extra ...string) []string {
	var roots []string
	if h.cfg != nil {
		h.readCfg(func(c *config.Config) {
			roots = append(roots, c.Storage.WorkPath, c.Storage.MemoryPath, c.Storage.ProjectsPath)
		})
	}
	if h.projectStore != nil {
		if list, err := h.projectStore.List(); err == nil {
			for _, p := range list {
				if safeReadRoot(p.WorkDir) {
					roots = append(roots, p.WorkDir)
				}
			}
		}
	}
	for _, root := range extra {
		if root != "" {
			roots = append(roots, root)
		}
	}
	return roots
}

// openReadPath opens abs for reading inside one of the configured roots. It
// returns the open file and its stat; the caller owns the file. An absolute
// symlink whose target is inside the tree still works because the caller
// resolves it first; a symlink that escapes is refused by Root even if it is
// swapped in after that resolution.
func (h *handler) openReadPath(abs string, extra ...string) (*os.File, os.FileInfo, error) {
	var lastErr error
	for _, root := range h.readRoots(extra...) {
		for _, spelling := range rootSpellings(root) {
			if !underRoot(spelling, abs) {
				continue
			}
			rel, err := filepath.Rel(spelling, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			dir, err := os.OpenRoot(spelling)
			if err != nil {
				continue
			}
			f, err := dir.Open(rel)
			_ = dir.Close()
			if err != nil {
				if os.IsNotExist(err) {
					lastErr = err
				}
				continue
			}
			st, err := f.Stat()
			if err != nil {
				_ = f.Close()
				continue
			}
			return f, st, nil
		}
	}
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, errOutsideReadRoots
}

// openReadDir is openReadPath with the directory check the listing endpoint
// needs. The caller reads entries from the open handle, so the listing cannot
// be redirected by a symlink swapped after the check either.
func (h *handler) openReadDir(abs string, extra ...string) (*os.File, os.FileInfo, error) {
	f, st, err := h.openReadPath(abs, extra...)
	if err != nil {
		return nil, nil, err
	}
	if !st.IsDir() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s: not a directory", abs)
	}
	return f, st, nil
}

// rootSpellings returns the configured root and its symlink-resolved form.
// On macOS /var is a symlink to /private/var, so a request that walks the
// resolved path must still match the root the config named.
func rootSpellings(root string) []string {
	root = filepath.Clean(root)
	if root == "" || root == "." {
		return nil
	}
	out := []string{root}
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		out = append(out, real)
	}
	return out
}

// writeReadError maps a rooted open failure to the viewer's HTTP answer.
// Outside the allow-list is a 403; a missing file inside it is a 404.
func writeReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errOutsideReadRoots):
		writeErr(w, http.StatusForbidden, errors.New("path is outside the workspace and memory roots"))
	case os.IsNotExist(err):
		writeErr(w, http.StatusNotFound, errors.New("no such file"))
	default:
		writeErr(w, http.StatusInternalServerError, errors.New("read failed"))
	}
}
