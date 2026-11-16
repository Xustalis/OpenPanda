// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// errNoNativeDialog marks the platform/session combinations where no chooser
// exists at all — a Linux box with neither zenity nor kdialog, a headless
// Windows session with no desktop to draw on. The console reads it as "open
// your own browser", not as a failure worth reporting.
var errNoNativeDialog = errors.New("no native directory dialog available")

// chooseDirectoryRequest is the JSON body for POST /api/dialog/choose-directory.
type chooseDirectoryRequest struct {
	// DefaultPath is where the chooser opens. The console sends the pick it
	// already holds, so a second visit starts where the first one ended
	// instead of at whatever folder the OS last remembered for someone else.
	DefaultPath string `json:"default_path"`
	// Title is the dialog's caption. The caller localizes it: the panel has
	// no idea which of the five console languages the browser is set to.
	Title string `json:"title"`
}

// chooseDirectoryResponse is the JSON response for POST /api/dialog/choose-directory.
type chooseDirectoryResponse struct {
	Path      string `json:"path"`
	Cancelled bool   `json:"canceled"`
	// Unsupported says this request cannot be answered by a native chooser,
	// so the console should raise its own in-page browser straight away
	// rather than wait for a window that is never going to appear.
	Unsupported bool   `json:"unsupported,omitempty"`
	Error       string `json:"error,omitempty"`
}

// openDirDialog is the seam a test swaps for a stub: the real implementation
// blocks on a modal OS window, and no test may raise one.
var openDirDialog = openNativeDirectoryDialog

// chooseDirectoryDialog serves POST /api/dialog/choose-directory by raising the
// host's own folder chooser and answering with the folder the user picked.
func (h *handler) chooseDirectoryDialog(w http.ResponseWriter, r *http.Request) {
	var req chooseDirectoryRequest
	if r.Body != nil {
		// Every field is optional, so an empty or unparseable body is simply
		// "no defaults" — not a 400 the console would have to special-case.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	if !callerIsLocal(r) {
		// The window would open on this host's desktop, where the person who
		// clicked is not. Say so instead of raising it.
		writeJSON(w, chooseDirectoryResponse{
			Unsupported: true,
			Error:       "the panel is being viewed from another device",
		})
		return
	}

	path, cancelled, err := openDirDialog(strings.TrimSpace(req.DefaultPath), strings.TrimSpace(req.Title))
	if err != nil {
		writeJSON(w, chooseDirectoryResponse{
			Cancelled:   cancelled,
			Unsupported: errors.Is(err, errNoNativeDialog),
			Error:       err.Error(),
		})
		return
	}
	writeJSON(w, chooseDirectoryResponse{Path: path, Cancelled: cancelled})
}

// callerIsLocal reports whether the request came from this machine. A native
// chooser can only serve a console running on the same desktop as the panel;
// for a remote browser the honest answer is the in-page browser, which is why
// this is checked before any window is raised.
func callerIsLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		// No usable address (in-process call, unix socket): nothing says the
		// caller is remote, so let the dialog try.
		return true
	}
	return ip.IsLoopback()
}

// existingDir normalizes a suggested starting folder, returning "" when it
// does not name a directory that exists — a stale pick from a deleted project
// must not stop the chooser from opening.
func existingDir(initial string) string {
	if initial == "" {
		return ""
	}
	if initial == "~" || strings.HasPrefix(initial, "~/") || strings.HasPrefix(initial, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			initial = filepath.Join(home, initial[1:])
		}
	}
	abs, err := filepath.Abs(initial)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return abs
	}
	return ""
}

// dirEntry represents a subdirectory in listDirectories response.
type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// listDirectoriesResponse is the JSON response for GET /api/fs/directories.
type listDirectoriesResponse struct {
	Current     string     `json:"current"`
	Parent      string     `json:"parent"`
	Separator   string     `json:"separator"`
	Directories []dirEntry `json:"directories"`
}

// listDirectories serves GET /api/fs/directories?path=...
// Provides a fallback visual directory navigator in the web UI.
//
// Unlike /api/fs/read this endpoint is not bounded by allowedReadRoot, and that
// is deliberate: it exists to pick a project work dir, which may live anywhere
// the user keeps code, and a picker that can only see the node's own data tree
// cannot pick one. It returns names and levels only — no file contents — so it
// leaks no more than a directory listing; widening a work dir to "/" is refused
// where it would matter (projects.ValidateWorkDir). The comment above
// allowedReadRoot describes the read boundary, not this picker.
func (h *handler) listDirectories(w http.ResponseWriter, r *http.Request) {
	reqPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if reqPath == "" || reqPath == "~" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			reqPath = home
		} else {
			reqPath, _ = os.Getwd()
		}
	}

	absPath, err := h.resolveFsPath(reqPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid path: "+err.Error()))
		return
	}

	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		// Fallback to parent or home
		if parent := filepath.Dir(absPath); parent != absPath {
			if pinfo, perr := os.Stat(parent); perr == nil && pinfo.IsDir() {
				absPath = parent
			}
		}
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("read directory failed: "+err.Error()))
		return
	}

	var dirs []dirEntry
	for _, e := range entries {
		// Only list directories, skip hidden ones by default
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, dirEntry{
			Name: name,
			Path: filepath.Join(absPath, name),
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})

	parent := filepath.Dir(absPath)
	if parent == absPath {
		parent = "" // at root
	}

	writeJSON(w, listDirectoriesResponse{
		Current:     absPath,
		Parent:      parent,
		Separator:   string(filepath.Separator),
		Directories: dirs,
	})
}
