package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HubSkill is one entry in the Skills Hub catalog.
type HubSkill struct {
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description" yaml:"description"`
	Scope       Scope    `json:"scope,omitempty" yaml:"scope,omitempty"`
	Author      string   `json:"author,omitempty" yaml:"author,omitempty"`
	Version     string   `json:"version,omitempty" yaml:"version,omitempty"`
	Tags        []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	URL         string   `json:"url,omitempty" yaml:"url,omitempty"`
	Content     string   `json:"content,omitempty" yaml:"content,omitempty"`
	DocURL      string   `json:"doc_url,omitempty" yaml:"doc_url,omitempty"`
	Recommended bool     `json:"recommended,omitempty" yaml:"recommended,omitempty"`
	Alias       string   `json:"alias,omitempty" yaml:"alias,omitempty"`
}

// CuratedSkills contains built-in, production-grade procedural workflows available offline or by default.
var CuratedSkills = []HubSkill{
	{
		Name:        "git-workflow",
		Alias:       "git",
		Description: "Standard Git branching, commit conventions, rebase workflow and pull request verification",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.1.0",
		Tags:        []string{"git", "workflow", "vcs", "dev"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: git-workflow",
			"description: Standard Git branching, commit conventions, rebase workflow and pull request verification",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Standardized procedural workflow for code changes, commits, rebasing, and branch hygiene.",
			"",
			"## Steps",
			"1. Create a feature branch with descriptive name:",
			"   `git checkout -b feat/<topic>` or `git checkout -b fix/<topic>`",
			"2. Keep commits atomic and write conventional commit messages:",
			"   - `feat: add feature X`",
			"   - `fix: resolve bug Y`",
			"   - `docs: update documentation for Z`",
			"3. Before submitting or merging, rebase onto origin/main:",
			"   `git fetch origin && git rebase origin/main`",
			"4. Run full test gate before pushing:",
			"   `make gate` or `go test ./...`",
			"5. Push branch and track upstream:",
			"   `git push -u origin HEAD`",
			"",
		}, "\n"),
	},
	{
		Name:        "code-review",
		Alias:       "review",
		Description: "Comprehensive code quality, security check, error handling, and test coverage audit",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"review", "audit", "security", "qa"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: code-review",
			"description: Comprehensive code quality, security check, error handling, and test coverage audit",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Systematic code review checklist to detect anti-patterns, security flaws, and race conditions.",
			"",
			"## Review Checklist",
			"1. **Safety & Security**:",
			"   - Validate user inputs against path traversal ('..', absolute paths, slashes).",
			"   - Ensure secrets, tokens, and keys are not committed or logged in plaintext.",
			"   - Verify proper timeout and context cancellation on network calls and subprocesses.",
			"2. **Concurrency & Resource Management**:",
			"   - Check mutex locking/unlocking ordering and defer statements.",
			"   - Ensure goroutines terminate cleanly and channels do not deadlock.",
			"   - Run tests with `-race` enabled.",
			"3. **Error Handling & Logging**:",
			"   - Check that errors are wrapped with meaningful context (`fmt.Errorf(\"...: %w\", err)`).",
			"   - Ensure nil pointers and missing map keys are checked before dereferencing.",
			"4. **Testing & Verification**:",
			"   - Unit tests cover happy path and error/edge cases.",
			"   - Assertions provide clear failure diffs.",
			"",
		}, "\n"),
	},
	{
		Name:        "docker-compose",
		Alias:       "docker",
		Description: "Manage multi-container application stacks, health checks, environment configs and volume persistence",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"docker", "containers", "devops", "deploy"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: docker-compose",
			"description: Manage multi-container application stacks, health checks, environment configs and volume persistence",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Workflow for building, starting, validating, and tearing down containerized local environments.",
			"",
			"## Steps",
			"1. Verify Docker daemon is running:",
			"   `docker info > /dev/null 2>&1`",
			"2. Build or pull images with latest changes:",
			"   `docker compose build --pull`",
			"3. Start stack in detached mode:",
			"   `docker compose up -d`",
			"4. Check service status and healthchecks:",
			"   `docker compose ps`",
			"5. Inspect service logs on failure:",
			"   `docker compose logs -f --tail=100 <service>`",
			"6. Clean up resources when done:",
			"   `docker compose down -v`",
			"",
		}, "\n"),
	},
	{
		Name:        "unit-testing",
		Alias:       "test",
		Description: "Guidelines for writing robust table-driven unit tests, race detector verification, and benchmarks in Go",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"testing", "golang", "quality", "bench"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: unit-testing",
			"description: Guidelines for writing robust table-driven unit tests, race detector verification, and benchmarks in Go",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Best practices for writing self-contained, repeatable, and concurrency-safe unit tests in Go.",
			"",
			"## Steps",
			"1. Structure test cases as table-driven structs with `name`, input, and expected output.",
			"2. Use `t.Run(tc.name, func(t *testing.T) { ... })` for clear isolation.",
			"3. Clean up temporary files with `t.TempDir()` rather than manual removals.",
			"4. Execute tests with race detection:",
			"   `go test -race -v ./...`",
			"5. Check test coverage:",
			"   `go test -cover ./...`",
			"",
		}, "\n"),
	},
	{
		Name:        "python-env",
		Alias:       "python",
		Description: "Python virtual environments, dependencies (pip/uv), linting (ruff), and pytest execution",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"python", "venv", "pip", "pytest"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: python-env",
			"description: Python virtual environments, dependencies (pip/uv), linting (ruff), and pytest execution",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Procedures for creating isolated Python environments, installing dependencies, and running tests.",
			"",
			"## Steps",
			"1. Create and activate a virtual environment:",
			"   `python3 -m venv .venv && source .venv/bin/activate`",
			"2. Upgrade installer and install dependencies:",
			"   `pip install -U pip && pip install -r requirements.txt`",
			"3. Check code formatting and lint rules:",
			"   `ruff check .` or `flake8`",
			"4. Execute the test suite:",
			"   `pytest -v`",
			"",
		}, "\n"),
	},
	{
		Name:        "node-workflow",
		Alias:       "node",
		Description: "Node.js & npm/pnpm project setup, package management, build pipeline, and testing",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"node", "npm", "pnpm", "frontend"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: node-workflow",
			"description: Node.js & npm/pnpm project setup, package management, build pipeline, and testing",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Standard workflow for managing JavaScript/TypeScript packages and running build scripts.",
			"",
			"## Steps",
			"1. Install project dependencies:",
			"   `pnpm install` or `npm install`",
			"2. Run typechecking and static lint:",
			"   `npm run typecheck && npm run lint`",
			"3. Build production bundle:",
			"   `npm run build`",
			"4. Execute automated test suite:",
			"   `npm test`",
			"",
		}, "\n"),
	},
	{
		Name:        "api-debug",
		Alias:       "api",
		Description: "RESTful API testing, curl request inspection, headers and JSON payload verification",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"api", "curl", "http", "debug"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: api-debug",
			"description: RESTful API testing, curl request inspection, headers and JSON payload verification",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Procedures for rapidly querying, diagnosing, and inspecting HTTP API endpoints.",
			"",
			"## Steps",
			"1. Send test request with verbose headers:",
			"   `curl -iv -X GET <url>`",
			"2. Send JSON payload with Bearer authentication:",
			"   `curl -s -X POST -H 'Content-Type: application/json' -H 'Authorization: Bearer <token>' -d '{\"key\":\"val\"}' <url> | jq .`",
			"3. Verify HTTP response code and latency:",
			"   `curl -o /dev/null -s -w 'code: %{http_code} time: %{time_total}s\\n' <url>`",
			"",
		}, "\n"),
	},
	{
		Name:        "system-diagnostics",
		Alias:       "sys",
		Description: "Inspect host CPU, memory, disk usage, network ports, and running process health",
		Scope:       ScopeGlobal,
		Author:      "OpenPanda Team",
		Version:     "1.0.0",
		Tags:        []string{"system", "ops", "diagnostic", "monitor"},
		Recommended: true,
		Content: strings.Join([]string{
			"---",
			"name: system-diagnostics",
			"description: Inspect host CPU, memory, disk usage, network ports, and running process health",
			"scope: global",
			"status: active",
			"use_count: 0",
			"success_count: 0",
			"---",
			"",
			"## Purpose",
			"Routine diagnostics to analyze system performance, port occupancy, and process health.",
			"",
			"## Steps",
			"1. Check disk utilization and inodes:",
			"   `df -h`",
			"2. Check memory consumption:",
			"   `vm_stat` (macOS) or `free -m` (Linux)",
			"3. List top memory and CPU consuming processes:",
			"   `ps aux --sort=-%mem | head -n 10`",
			"4. Check open listening ports:",
			"   `lsof -iTCP -sTCP:LISTEN -n -P`",
			"",
		}, "\n"),
	},
}

// HubIndex is the top-level repository index for a Skills Hub.
type HubIndex struct {
	Version string     `json:"version" yaml:"version"`
	Name    string     `json:"name,omitempty" yaml:"name,omitempty"`
	Skills  []HubSkill `json:"skills" yaml:"skills"`
}

// RecommendedSkills returns the subset of skills designated as recommended presets.
func RecommendedSkills() []HubSkill {
	var list []HubSkill
	for _, s := range CuratedSkills {
		if s.Recommended {
			list = append(list, s)
		}
	}
	return list
}

// ResolveSkillAlias maps common shorthand aliases (e.g. "git", "docker", "test", "python")
// or 1-based numeric indices ("1", "2") to canonical skill names.
func ResolveSkillAlias(name string) string {
	raw := strings.TrimSpace(name)
	lower := strings.ToLower(raw)

	// Check 1-based index (e.g. "1" -> first recommended skill)
	rec := RecommendedSkills()
	if num, err := strconv.Atoi(lower); err == nil && num >= 1 && num <= len(rec) {
		return rec[num-1].Name
	}

	for _, s := range CuratedSkills {
		if strings.EqualFold(s.Name, raw) || (s.Alias != "" && strings.EqualFold(s.Alias, raw)) {
			return s.Name
		}
	}

	switch lower {
	case "git":
		return "git-workflow"
	case "review":
		return "code-review"
	case "docker":
		return "docker-compose"
	case "test", "tests":
		return "unit-testing"
	case "python", "py":
		return "python-env"
	case "node", "npm", "pnpm":
		return "node-workflow"
	case "api", "curl":
		return "api-debug"
	case "system", "sys", "diag":
		return "system-diagnostics"
	}

	return raw
}

// FetchHubIndex retrieves the skill index from hubURL. If hubURL is empty or remote fetch fails,
// it gracefully returns the curated catalog.
func FetchHubIndex(ctx context.Context, hubURL string) (*HubIndex, error) {
	hubURL = strings.TrimSpace(hubURL)
	if hubURL == "" {
		return &HubIndex{
			Version: "1.0",
			Name:    "OpenPanda Curated Skills Hub",
			Skills:  CuratedSkills,
		}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL, nil)
	if err != nil {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}
	req.Header.Set("User-Agent", "OpenPanda-Skills-Hub/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes))
	if err != nil {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}

	var idx HubIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return &HubIndex{Version: "1.0", Name: "OpenPanda Curated Skills (Fallback)", Skills: CuratedSkills}, nil
	}

	// Merge curated skills that are not already present in remote index
	existing := make(map[string]bool)
	for _, s := range idx.Skills {
		existing[s.Name] = true
	}
	for _, s := range CuratedSkills {
		if !existing[s.Name] {
			idx.Skills = append(idx.Skills, s)
		}
	}

	return &idx, nil
}

// SearchHub filters skills in index by a search query (matching name, description, tags, author, alias).
func SearchHub(index *HubIndex, query string) []HubSkill {
	if index == nil {
		return nil
	}
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return index.Skills
	}

	tokens := strings.Fields(query)
	var matched []HubSkill

	for _, sk := range index.Skills {
		text := strings.ToLower(sk.Name + " " + sk.Alias + " " + sk.Description + " " + sk.Author + " " + strings.Join(sk.Tags, " "))
		matchAll := true
		for _, token := range tokens {
			if !strings.Contains(text, token) {
				matchAll = false
				break
			}
		}
		if matchAll {
			matched = append(matched, sk)
		}
	}
	return matched
}

// InstallFromHub finds a skill in the hub (resolving aliases and numeric IDs) and installs it.
func InstallFromHub(ctx context.Context, store *Store, hubURL string, name string, opts ImportOptions) (*Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skills: skill name cannot be empty")
	}

	resolvedName := ResolveSkillAlias(name)

	index, err := FetchHubIndex(ctx, hubURL)
	if err != nil {
		return nil, fmt.Errorf("skills: fetch hub index: %w", err)
	}

	var target *HubSkill
	for i := range index.Skills {
		if strings.EqualFold(index.Skills[i].Name, resolvedName) ||
			(index.Skills[i].Alias != "" && strings.EqualFold(index.Skills[i].Alias, name)) {
			target = &index.Skills[i]
			break
		}
	}

	if target == nil {
		return nil, fmt.Errorf("skills: skill %q not found in hub", name)
	}

	if opts.Scope == "" && target.Scope != "" {
		opts.Scope = target.Scope
	}

	if strings.TrimSpace(target.Content) != "" {
		return store.ImportBytes([]byte(target.Content), opts)
	}

	if strings.TrimSpace(target.URL) != "" {
		skills, err := store.ImportURL(ctx, target.URL, opts)
		if err != nil {
			return nil, err
		}
		if len(skills) == 0 {
			return nil, fmt.Errorf("skills: no skills imported from %s", target.URL)
		}
		return skills[0], nil
	}

	return nil, fmt.Errorf("skills: hub skill %q has neither content nor URL", name)
}

// InstallAllRecommended installs all recommended presets in one batch.
func InstallAllRecommended(ctx context.Context, store *Store, hubURL string, opts ImportOptions) ([]*Skill, error) {
	rec := RecommendedSkills()
	var installed []*Skill
	for _, item := range rec {
		sk, err := InstallFromHub(ctx, store, hubURL, item.Name, opts)
		if err != nil {
			// If already exists and not forced, continue
			if !opts.Force && strings.Contains(err.Error(), "already exists") {
				continue
			}
			return installed, err
		}
		installed = append(installed, sk)
	}
	return installed, nil
}

// IsBuiltinSkill reports whether name (or its alias) matches a standard built-in skill.
func IsBuiltinSkill(name string) bool {
	clean := strings.ToLower(strings.TrimSpace(name))
	for _, c := range CuratedSkills {
		if strings.EqualFold(c.Name, clean) || (c.Alias != "" && strings.EqualFold(c.Alias, clean)) {
			return true
		}
	}
	return false
}

// BuiltinSkills returns all standard built-in skills defined in the system.
func BuiltinSkills() []HubSkill {
	return CuratedSkills
}

// EnsureBuiltins seeds all standard built-in skills into the store with global
// scope and active status if they are not already present on disk.
// If a skill is already present, it is left untouched to respect user modifications.
func (s *Store) EnsureBuiltins() error {
	for _, cur := range CuratedSkills {
		existing, err := s.Load(ScopeGlobal, "", cur.Name)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		sk, err := ParseSkill([]byte(cur.Content))
		if err != nil {
			continue
		}
		sk.Builtin = true
		sk.Status = StatusActive
		if err := s.Save(sk); err != nil {
			return err
		}
	}
	return nil
}

// ResetBuiltin restores a built-in skill (by name or alias) to its original factory definition.
func (s *Store) ResetBuiltin(name string) (*Skill, error) {
	clean := strings.ToLower(strings.TrimSpace(name))
	for _, cur := range CuratedSkills {
		if strings.EqualFold(cur.Name, clean) || (cur.Alias != "" && strings.EqualFold(cur.Alias, clean)) {
			sk, err := ParseSkill([]byte(cur.Content))
			if err != nil {
				return nil, err
			}
			sk.Builtin = true
			sk.Status = StatusActive
			if err := s.Save(sk); err != nil {
				return nil, err
			}
			return sk, nil
		}
	}
	return nil, fmt.Errorf("skills: %q is not a built-in skill", name)
}

// DiscoverAndInstall searches the Skills Hub for skills matching query, picks the top
// match, and installs it if not already present. It returns the skill and a boolean
// indicating whether it was newly installed (true) or was already present (false).
// The caller picks the landing status through opts: user-initiated installs pass
// StatusActive (the explicit click/command is the approval), while autonomous
// installs (model tools, MCP self-tools) must pass StatusPending so the skill
// cannot steer future tasks until a human approves it in the foreground.
func (s *Store) DiscoverAndInstall(ctx context.Context, hubURL string, query string, opts ImportOptions) (*Skill, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, false, fmt.Errorf("skills: search query cannot be empty")
	}

	idx, err := FetchHubIndex(ctx, hubURL)
	if err != nil {
		return nil, false, fmt.Errorf("skills: fetch hub index: %w", err)
	}

	results := SearchHub(idx, query)
	if len(results) == 0 {
		return nil, false, fmt.Errorf("skills: no skills matching %q found in hub", query)
	}

	top := results[0]
	existing, err := s.Load(ScopeGlobal, "", top.Name)
	if err == nil && existing != nil {
		return existing, false, nil
	}

	if opts.Scope == "" {
		opts.Scope = ScopeGlobal
	}
	installed, err := InstallFromHub(ctx, s, hubURL, top.Name, opts)
	if err != nil {
		return nil, false, err
	}
	return installed, true, nil
}
