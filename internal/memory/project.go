// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// Projects manages per-project memory files (design §17.2). Each project's
// memory lives at projects/{name}/MEMORY.md in the same §-separated format as
// Hermes, and is injected only into that project's agent context — never into
// Hermes.
type Projects struct {
	root   string
	limits Limits     // configured cap; zero falls back to ProjectCharLimit
	mu     sync.Mutex // serializes whole-file writes so concurrent saves cannot interleave (P1-21)
}

// NewProjects wraps a projects/ root directory with the historical
// compile-time cap. Configured deployments use NewProjectsWithLimits instead.
func NewProjects(root string) *Projects {
	return NewProjectsWithLimits(root, Limits{})
}

// NewProjectsWithLimits wraps a projects/ root directory with a configurable
// character cap (config memory.limits.project); a non-positive value falls
// back to ProjectCharLimit.
func NewProjectsWithLimits(root string, limits Limits) *Projects {
	return &Projects{root: root, limits: limits}
}

// Limit returns the effective per-project character cap.
func (p *Projects) Limit() int { return p.limits.project() }

// ValidateName rejects project names that are empty or could escape the
// projects root via a path separator. A valid name is a single path segment.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("memory: project name must not be empty")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("memory: invalid project name %q", name)
	}
	return nil
}

// Path returns the MEMORY.md path for a project, validating the name first.
func (p *Projects) Path(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(p.root, name, "MEMORY.md"), nil
}

// List returns the names of the projects that exist under the root (i.e. have
// a directory), sorted. A missing root yields an empty list — no projects yet.
func (p *Projects) List() ([]string, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: list projects: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Load reads a project's memory as a MemFile with the project limit applied. A
// missing file yields an empty MemFile, so a project with no memory simply
// contributes nothing.
func (p *Projects) Load(name string) (MemFile, error) {
	path, err := p.Path(name)
	if err != nil {
		return MemFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return MemFile{Limit: p.limits.project()}, nil
		}
		return MemFile{}, fmt.Errorf("memory: read project memory %q: %w", name, err)
	}
	m := ParseMem(data)
	m.Limit = p.limits.project()
	return m, nil
}

// Root returns the on-disk project-memory root used by this store.
func (p *Projects) Root() string { return p.root }

// Save writes a project's memory, creating the project directory as needed. It
// enforces the project character cap and refuses an over-limit file rather than
// silently truncating, mirroring Hermes.save.
func (p *Projects) Save(name string, m MemFile) error {
	path, err := p.Path(name)
	if err != nil {
		return err
	}
	release, err := lockRoot(p.root)
	if err != nil {
		return fmt.Errorf("memory: lock project memory %q: %w", name, err)
	}
	defer release()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveLocked(name, path, m)
}

// Update runs fn against the project's current memory and writes the result
// back as one critical section — the read-modify-write form Save cannot give a
// caller, because the state fn has to reason about is the state on disk.
func (p *Projects) Update(name string, fn func(*MemFile) error) error {
	path, err := p.Path(name)
	if err != nil {
		return err
	}
	release, err := lockRoot(p.root)
	if err != nil {
		return fmt.Errorf("memory: lock project memory %q: %w", name, err)
	}
	defer release()
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.Load(name)
	if err != nil {
		return err
	}
	before := m.Bytes()
	if err := fn(&m); err != nil {
		return err
	}
	if bytes.Equal(before, m.Bytes()) {
		return nil // nothing changed: no rewrite, no mtime churn
	}
	return p.saveLocked(name, path, m)
}

// saveLocked writes the file; the caller holds the project mutex and the
// cross-process lock.
func (p *Projects) saveLocked(name, path string, m MemFile) error {
	limit := m.Limit
	if limit <= 0 {
		limit = p.limits.project()
	}
	if m.Chars() > limit {
		return fmt.Errorf("%w: at %d/%d chars", ErrOverLimit, m.Chars(), limit)
	}
	for _, e := range m.Entries {
		if strings.ContainsRune(e, '§') {
			return ErrSeparator
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("memory: create project dir: %w", err)
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	if err := util.WriteFileAtomic(path, m.Bytes(), 0o600); err != nil {
		return fmt.Errorf("memory: write project memory %q: %w", name, err)
	}
	return nil
}

// Rename moves all project memory, including daily history and scoped skills.
// Both names share a parent filesystem, so a single directory rename keeps the
// source intact on failure and avoids copying only MEMORY.md before deleting
// its sibling history. The store lock serializes memory writers.
func (p *Projects) Rename(oldName, newName string) error {
	return p.RenameWith(oldName, newName, nil)
}

// RenameWith carries project files and applies an associated metadata rename
// while writers are locked out. If apply fails, the files move back. apply must
// leave its own state unchanged on failure and must not call this memory store.
func (p *Projects) RenameWith(oldName, newName string, apply func() error) error {
	if err := ValidateName(oldName); err != nil {
		return err
	}
	if err := ValidateName(newName); err != nil {
		return err
	}
	if oldName == newName {
		if apply != nil {
			return apply()
		}
		return nil
	}
	path, err := p.Path(newName)
	if err != nil {
		return err
	}
	release, err := lockRoot(p.root)
	if err != nil {
		return fmt.Errorf("memory: lock project memory %q: %w", oldName, err)
	}
	defer release()
	p.mu.Lock()
	defer p.mu.Unlock()
	// Refuse an existing target, including an empty directory or a symlink.
	newDir := filepath.Join(p.root, newName)
	if _, statErr := os.Lstat(newDir); statErr == nil {
		return fmt.Errorf("memory: project %q already exists", newName)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("memory: check project %q: %w", newName, statErr)
	}
	oldDir := filepath.Join(p.root, oldName)
	info, err := os.Lstat(oldDir)
	if os.IsNotExist(err) {
		// Preserve the historical behavior for projects without any memory.
		if err := p.saveLocked(newName, path, MemFile{}); err != nil {
			return err
		}
		if apply != nil {
			if err := apply(); err != nil {
				return errors.Join(err, os.Remove(path), os.Remove(newDir))
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("memory: project %q is not a directory", oldName)
	}
	// Projects.Daily takes the same outer root lock, so no daily file handle
	// is open during this rename (also required by Windows file sharing).
	if err := os.Rename(oldDir, newDir); err != nil {
		return fmt.Errorf("memory: rename project %q: %w", oldName, err)
	}
	if apply != nil {
		if err := apply(); err != nil {
			if rollbackErr := os.Rename(newDir, oldDir); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("memory: restore project %q: %w", oldName, rollbackErr))
			}
			return err
		}
	}
	return nil
}

// Delete removes a project's directory — its MEMORY.md and any project-scoped
// skills alongside it. This is the only destructive call in the package, so the
// containment argument is worth stating: ValidateName has already rejected path
// separators, "." and "..", so the join can only name a direct child of the root,
// and nothing outside p.root is reachable from any input.
//
// A project that has no directory is already deleted, so that is not an error.
func (p *Projects) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	// Cross-process first: an update of the same project's memory may be in
	// flight from the CLI or the console, and deleting the directory under it
	// would lose that writer's change after it reported success.
	release, err := lockRoot(p.root)
	if err != nil {
		return fmt.Errorf("memory: lock project memory %q: %w", name, err)
	}
	defer release()
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := filepath.Join(p.root, name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("memory: remove project %q: %w", name, err)
	}
	return nil
}

// Daily keeps operational project history outside the personal warm layer.
func (p *Projects) Daily(name string) (*Daily, error) {
	path, err := p.Path(name)
	if err != nil {
		return nil, err
	}
	daily := NewDaily(filepath.Join(filepath.Dir(path), "daily"))
	daily.projectRoot = p.root
	return daily, nil
}
