package panel

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// chooseDirectoryResponse is the JSON response for POST /api/dialog/choose-directory.
type chooseDirectoryResponse struct {
	Path      string `json:"path"`
	Cancelled bool   `json:"cancelled"`
	Error     string `json:"error,omitempty"`
}

// chooseDirectoryDialog invokes the native OS directory chooser (macOS/Windows/Linux)
// and returns the selected directory path.
func (h *handler) chooseDirectoryDialog(w http.ResponseWriter, r *http.Request) {
	path, cancelled, err := openNativeDirectoryDialog()
	if err != nil {
		writeJSON(w, chooseDirectoryResponse{
			Cancelled: cancelled,
			Error:     err.Error(),
		})
		return
	}
	writeJSON(w, chooseDirectoryResponse{
		Path:      path,
		Cancelled: cancelled,
	})
}

// openNativeDirectoryDialog opens the OS native file dialog to choose a folder.
func openNativeDirectoryDialog() (string, bool, error) {
	switch runtime.GOOS {
	case "darwin":
		// macOS: AppleScript choose folder
		cmd := exec.Command("osascript", "-e", `POSIX path of (choose folder with prompt "Select Project Folder")`)
		out, err := cmd.Output()
		if err != nil {
			// User clicked Cancel in osascript
			if strings.Contains(err.Error(), "exit status 1") || strings.Contains(string(out), "User canceled") {
				return "", true, nil
			}
			return "", false, err
		}
		path := strings.TrimSpace(string(out))
		path = strings.TrimRight(path, "/")
		return path, false, nil

	case "windows":
		// Windows: PowerShell FolderBrowserDialog
		psScript := `Add-Type -AssemblyName System.Windows.Forms; $f = New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description = 'Select Project Folder'; if ($f.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $f.SelectedPath }`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
		out, err := cmd.Output()
		if err != nil {
			return "", false, err
		}
		path := strings.TrimSpace(string(out))
		if path == "" {
			return "", true, nil
		}
		return path, false, nil

	case "linux":
		// Linux: try zenity then kdialog
		if _, err := exec.LookPath("zenity"); err == nil {
			cmd := exec.Command("zenity", "--file-selection", "--directory", "--title=Select Project Folder")
			out, err := cmd.Output()
			if err != nil {
				return "", true, nil // zenity returns exit 1 on cancel
			}
			path := strings.TrimSpace(string(out))
			return strings.TrimRight(path, "/"), false, nil
		}
		if _, err := exec.LookPath("kdialog"); err == nil {
			cmd := exec.Command("kdialog", "--getexistingdirectory", ".")
			out, err := cmd.Output()
			if err != nil {
				return "", true, nil
			}
			path := strings.TrimSpace(string(out))
			return strings.TrimRight(path, "/"), false, nil
		}
		return "", false, errors.New("no native file dialog available on this Linux system (zenity/kdialog missing)")

	default:
		return "", false, errors.New("native directory dialog not supported on " + runtime.GOOS)
	}
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

	absPath, err := filepath.Abs(reqPath)
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
