//go:build !windows

package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PATH persistence on unix is a marked block appended to the user's shell
// startup files. Markers make the change idempotent (re-running install
// never duplicates the line) and fully reversible (uninstall strips exactly
// the block, leaving any user-authored PATH edits untouched).
const (
	markerBegin = "# >>> openpanda path >>>"
	markerEnd   = "# <<< openpanda path <<<"
)

// rcCandidates lists shell startup files in priority order. zsh's rc lives
// under $ZDOTDIR when set; the rest are conventional home-dotfiles.
func rcCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	if z := os.Getenv("ZDOTDIR"); z != "" {
		out = append(out, filepath.Join(z, ".zshrc"))
	} else {
		out = append(out, filepath.Join(home, ".zshrc"))
	}
	out = append(out,
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
	)
	return dedupe(out)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// AddToPATH appends the marked export block to every existing rc file (or
// creates ~/.profile when none exist — rare, but a fresh minimal box should
// still work). Returns the files it touched; an empty slice with nil error
// means the marker is already present (idempotent re-install).
func AddToPATH(dir string) ([]string, error) {
	block := fmt.Sprintf("%s\nexport PATH=%q:$PATH\n%s\n", markerBegin, dir, markerEnd)
	var candidates []string
	already := false
	for _, rc := range rcCandidates() {
		data, err := os.ReadFile(rc)
		if err != nil {
			if _, statErr := os.Stat(rc); statErr == nil {
				continue // exists but unreadable; another rc may still cover us
			}
			continue
		}
		if strings.Contains(string(data), markerBegin) {
			already = true // registered in this file already
			continue
		}
		candidates = append(candidates, rc)
	}
	if len(candidates) == 0 {
		if already {
			return nil, nil
		}
		// No rc file at all: create ~/.profile so a login shell picks it up.
		if home, err := os.UserHomeDir(); err == nil {
			if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("\n"+block), 0o644); err == nil {
				return []string{filepath.Join(home, ".profile")}, nil
			}
		}
		return nil, fmt.Errorf("install: no shell startup file could be updated; add %s to PATH manually", dir)
	}
	var written []string
	for _, rc := range candidates {
		f, err := os.OpenFile(rc, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			continue
		}
		_, err = f.WriteString("\n" + block)
		f.Close()
		if err == nil {
			written = append(written, rc)
		}
	}
	if len(written) == 0 && !already {
		return nil, fmt.Errorf("install: no shell startup file could be updated; add %s to PATH manually", dir)
	}
	return written, nil
}

// RemovePATHPersistence strips our marked blocks from every rc file. The
// dir argument is unused on unix (markers identify our lines); it exists so
// both platforms share one signature. Returns the files that changed;
// user-authored lines are never touched.
func RemovePATHPersistence(_ string) ([]string, error) {
	var changed []string
	for _, rc := range rcCandidates() {
		data, err := os.ReadFile(rc)
		if err != nil || !strings.Contains(string(data), markerBegin) {
			continue
		}
		if err := os.WriteFile(rc, []byte(stripMarkers(string(data))), 0o644); err == nil {
			changed = append(changed, rc)
		}
	}
	return changed, nil
}

// stripMarkers removes every marked block (begin..end inclusive). Nested or
// unterminated blocks shed their begin line so a later re-install stays
// idempotent.
func stripMarkers(s string) string {
	var out []string
	skipping := false
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.TrimSpace(line) == markerBegin:
			skipping = true
		case strings.TrimSpace(line) == markerEnd:
			skipping = false
		case !skipping:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// PathPersistedAt reports which rc files currently carry the marker —
// doctor shows this as the "will survive reboot" state.
func PathPersistedAt(dir string) []string {
	var found []string
	for _, rc := range rcCandidates() {
		data, err := os.ReadFile(rc)
		if err == nil && strings.Contains(string(data), markerBegin) {
			found = append(found, rc)
		}
	}
	return found
}
