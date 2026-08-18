// Package skills implements the self-evolving procedural-memory layer (design
// §8): reusable workflows distilled from task history into SKILL.md files.
// Declarative memory (the memory package) stores facts; skills store procedures
// ("how to do X, what to avoid, how to verify success").
//
// The format follows the Harness/agentskills.io standard — YAML frontmatter +
// Markdown body — and the trigger + progressive-loading model follows Hermes
// Agent (should_create_skill, skill index). A skill is scoped to global /
// project / device so project-specific workflows never leak into other
// projects (design §8.3).
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xenith/openpanda/internal/util"
)

// Scope is the visibility tier of a skill (design §8.3).
type Scope string

const (
	ScopeGlobal  Scope = "global"  // reusable everywhere
	ScopeProject Scope = "project" // one project only
	ScopeDevice  Scope = "device"  // one device only
)

// Status is a skill's lifecycle stage.
type Status string

const (
	StatusPending Status = "pending" // generated, awaiting user approval
	StatusActive  Status = "active"
	StatusDormant Status = "dormant" // unused for a while; keep but deprioritize
	StatusExpired Status = "expired" // stale; candidate for pruning
)

// Skill is one procedural memory entry: the YAML frontmatter fields plus a
// Markdown body (the procedure itself). All fields are exported so callers (the
// core, the generator) can construct and mutate skills directly.
type Skill struct {
	Name         string    `yaml:"name"`
	Description  string    `yaml:"description"`
	Scope        Scope     `yaml:"scope"`
	Project      string    `yaml:"project,omitempty"`
	Device       string    `yaml:"device,omitempty"`
	Status       Status    `yaml:"status"`
	UseCount     int       `yaml:"use_count"`
	SuccessCount int       `yaml:"success_count"`
	LastUsed     time.Time `yaml:"last_used,omitempty"`
	Body         string    `yaml:"-"`
}

// ParseSkill decodes SKILL.md bytes (YAML frontmatter + Markdown body).
func ParseSkill(data []byte) (*Skill, error) {
	fm, body, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, err
	}
	s := &Skill{}
	if err := yaml.Unmarshal([]byte(fm), s); err != nil {
		return nil, fmt.Errorf("skills: parse frontmatter: %w", err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("skills: skill name is required")
	}
	if s.Description == "" {
		return nil, fmt.Errorf("skills: skill %q description is required", s.Name)
	}
	if s.Scope == "" {
		s.Scope = ScopeGlobal // an omitted scope defaults to global
	}
	switch s.Scope {
	case ScopeGlobal, ScopeProject, ScopeDevice:
	default:
		return nil, fmt.Errorf("skills: unknown scope %q", s.Scope)
	}
	s.Body = strings.TrimSpace(body)
	return s, nil
}

// Bytes serializes the skill back to the on-disk SKILL.md format.
func (s *Skill) Bytes() ([]byte, error) {
	fm, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("skills: marshal frontmatter: %w", err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(fm)
	b.WriteString("---\n")
	if s.Body != "" {
		b.WriteString("\n")
		b.WriteString(s.Body)
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}

// splitFrontmatter separates the leading "--- ... ---" block from the body.
func splitFrontmatter(src string) (fm, body string, err error) {
	if !strings.HasPrefix(src, "---") {
		return "", "", fmt.Errorf("skills: missing frontmatter delimiter")
	}
	rest := strings.TrimPrefix(src, "---")
	rest = strings.TrimPrefix(rest, "\n")
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", fmt.Errorf("skills: unclosed frontmatter")
	}
	fm = rest[:idx]
	body = strings.TrimPrefix(rest[idx+len("\n---"):], "\n")
	return fm, body, nil
}

// Store manages SKILL.md files under a skills/ root, keyed by scope.
type Store struct {
	root string
}

// NewStore wraps a skills/ root directory.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Path returns the on-disk location for a skill, determined by its scope.
// The directory layout keeps project/device skills segregated (design §8.3).
func (s *Store) Path(sk *Skill) (string, error) {
	dir, err := scopeDir(sk)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, dir, sk.Name, "SKILL.md"), nil
}

// Save writes a skill to its scope directory, creating it as needed.
func (s *Store) Save(sk *Skill) error {
	path, err := s.Path(sk)
	if err != nil {
		return err
	}
	data, err := sk.Bytes()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("skills: create skill dir: %w", err)
	}
	if err := util.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("skills: write %s: %w", sk.Name, err)
	}
	return nil
}

// Load reads and parses one skill by scope and name. A missing skill returns
// (nil, nil). The scope key is validated against path traversal.
func (s *Store) Load(scope Scope, key, name string) (*Skill, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	if err := validateScopeKey(key); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, dirFor(scope, key), name, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("skills: read %s: %w", name, err)
	}
	return ParseSkill(data)
}

// scopeDir returns the directory (relative to root) for a skill, validating the
// name and the scope's required key (so a project/device name cannot escape the
// skills root via a path separator).
func scopeDir(sk *Skill) (string, error) {
	if err := validateName(sk.Name); err != nil {
		return "", err
	}
	if sk.Scope == "" {
		sk.Scope = ScopeGlobal
	}
	key := ""
	switch sk.Scope {
	case ScopeProject:
		key = sk.Project
	case ScopeDevice:
		key = sk.Device
	}
	if err := validateScopeKey(key); err != nil {
		return "", err
	}
	return dirFor(sk.Scope, key), nil
}

// validateScopeKey validates a project/device scope key. Global scope has an
// empty key, which is always valid.
func validateScopeKey(key string) error {
	if key == "" {
		return nil
	}
	return validateName(key)
}

// dirFor builds the scope directory for an index/load lookup.
func dirFor(scope Scope, key string) string {
	switch scope {
	case ScopeProject:
		return filepath.Join("project", key)
	case ScopeDevice:
		return filepath.Join("device", key)
	default:
		return "global"
	}
}

// validateName rejects names that are empty or could escape the skills root.
func validateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("skills: invalid skill name %q", name)
	}
	return nil
}
