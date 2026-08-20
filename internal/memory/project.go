package memory

import (
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

// Save writes a project's memory, creating the project directory as needed. It
// enforces the project character cap and refuses an over-limit file rather than
// silently truncating, mirroring Hermes.save.
func (p *Projects) Save(name string, m MemFile) error {
	path, err := p.Path(name)
	if err != nil {
		return err
	}
	limit := m.Limit
	if limit <= 0 {
		limit = p.limits.project()
	}
	if m.Chars() > limit {
		return fmt.Errorf("%w: at %d/%d chars", ErrOverLimit, m.Chars(), limit)
	}
	// Serialize the write so concurrent saves of the same project cannot
	// interleave into a torn file (P1-21); the write itself is atomic (P1-20).
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("memory: create project dir: %w", err)
	}
	if err := util.WriteFileAtomic(path, m.Bytes(), 0o644); err != nil {
		return fmt.Errorf("memory: write project memory %q: %w", name, err)
	}
	return nil
}
