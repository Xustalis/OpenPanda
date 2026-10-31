// SPDX-License-Identifier: AGPL-3.0-or-later

// Package scout builds the compact workspace report an agent's prompt carries
// before it starts work: repository identity, size, layout, key files and the
// project's own build/test entry points.
//
// The report is deliberately deterministic — no model call — so a large
// repository is described the same way every execution round, the prompt cost
// is bounded, and a wrong-scope run is visible before the agent spends its
// first turn wandering the tree. It is orientation, not a substitute for
// reading files: the map names where things are and which commands the
// project itself uses to prove a change.
package scout

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Scan limits. A monorepo can carry a million files; the report is useful at
// a few thousand and the last hundred thousand change nothing an agent can
// use. The deadline bounds a walk over a cold network mount, where the count
// limit alone could still block the execution path for minutes.
const (
	maxFiles   = 20000
	maxDirs    = 4000
	maxScanDur = 2 * time.Second
)

// maxRender caps the rendered block. It is prompt space, not storage: the
// agent needs the shape of the tree, not an inventory of it.
const maxRender = 2400

// skipDirs are generated or vendored trees. Counting them would drown the
// report in dependencies nobody edits, and walking them is what makes a
// large repository slow to describe.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "third_party": true,
	"dist": true, "build": true, "target": true, "out": true,
	".next": true, ".nuxt": true, ".cache": true, ".turbo": true,
	".venv": true, "venv": true, "env": true, "__pycache__": true,
	".pytest_cache": true, ".mypy_cache": true, ".tox": true,
	"coverage": true, ".idea": true, ".gradle": true, ".terraform": true,
	"bin": true, "obj": true, "Pods": true, "DerivedData": true,
}

// langByExt maps source extensions to the language name shown in the report.
// Unknown extensions are counted only in the file total.
var langByExt = map[string]string{
	".go": "go", ".py": "python", ".rs": "rust",
	".ts": "typescript", ".tsx": "typescript", ".js": "javascript", ".jsx": "javascript",
	".mjs": "javascript", ".cjs": "javascript", ".vue": "vue", ".svelte": "svelte",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp", ".m": "objc", ".mm": "objc",
	".java": "java", ".kt": "kotlin", ".kts": "kotlin", ".cs": "csharp", ".rb": "ruby", ".php": "php",
	".swift": "swift", ".sh": "shell", ".bash": "shell", ".ps1": "powershell", ".zsh": "shell",
	".sql": "sql", ".css": "css", ".scss": "css", ".html": "html", ".md": "markdown",
	".yaml": "yaml", ".yml": "yaml", ".json": "json", ".toml": "toml",
}

// keyNames are root files that tell an agent how the project is built and how
// it expects contributors to work. Matched case-insensitively.
var keyNames = []string{
	"README.md", "README.zh-CN.md", "AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md",
	"Makefile", "go.mod", "Cargo.toml", "package.json", "pyproject.toml",
	"setup.py", "requirements.txt", "CMakeLists.txt", "pom.xml", "build.gradle",
	"build.gradle.kts", "docker-compose.yml", "docker-compose.yaml", "Dockerfile",
}

// Build/test targets recognised in a Makefile. The list is intentionally the
// verification vocabulary projects actually use; an arbitrary target is not a
// build step and listing it would invite the agent to run the wrong thing.
var makeTargets = []string{"build", "test", "lint", "check", "verify", "gate", "vet"}

// Command is one project-declared build or verification entry.
type Command struct {
	Run    string
	Source string
}

// DirCount is one top-level directory and how many files it holds.
type DirCount struct {
	Name  string
	Files int
}

// Report is the workspace description rendered into an agent prompt.
type Report struct {
	Root      string
	RepoRoot  string // git top level when Root is a subdirectory of it
	Branch    string
	Commit    string
	Dirty     bool
	GitKnown  bool
	Files     int
	Dirs      int
	Truncated bool
	TopDirs   []DirCount
	Langs     []DirCount
	KeyFiles  []string
	Build     []Command
	Test      []Command
}

// Empty reports whether there is nothing worth telling the agent. An empty
// workspace still gets no map: the execution frame already names the
// directory, and a zero-file report is noise on a fresh task.
func (r Report) Empty() bool {
	return r.Files == 0 && len(r.KeyFiles) == 0 && !r.GitKnown
}

// Render renders the report as one compact prompt block ("" when Empty).
// Labels stay English: the map is consumed by the agent, and its values are
// paths, language names and commands that are not translated.
func (r Report) Render() string {
	if r.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("[workspace map — orient yourself before acting]\n")
	b.WriteString("root: " + r.Root + "\n")
	if r.RepoRoot != "" && r.RepoRoot != r.Root {
		b.WriteString("git repository: " + r.RepoRoot + " (your workspace is a subdirectory)\n")
	}
	if r.GitKnown {
		state := "clean"
		if r.Dirty {
			state = "has uncommitted changes"
		}
		line := "git: " + r.Branch
		if r.Commit != "" {
			line += " @ " + r.Commit
		}
		b.WriteString(line + ", " + state + "\n")
	}
	size := "size: " + strconv.Itoa(r.Files) + " files in " + strconv.Itoa(r.Dirs) + " dirs"
	if r.Truncated {
		size += " (scan capped; the tree is larger)"
	}
	b.WriteString(size + "\n")
	if dirs := renderCounts(r.TopDirs, true); dirs != "" {
		b.WriteString("top dirs: " + dirs + "\n")
	}
	if langs := renderCounts(r.Langs, false); langs != "" {
		b.WriteString("languages: " + langs + "\n")
	}
	if len(r.KeyFiles) > 0 {
		b.WriteString("key files: " + strings.Join(r.KeyFiles, ", ") + "\n")
	}
	for _, c := range r.Build {
		b.WriteString("build: " + c.Run + "\n")
	}
	for _, c := range r.Test {
		b.WriteString("verify: " + c.Run + "\n")
	}
	b.WriteString("Use this map to choose what to read; open the specific files you need instead of re-scanning the tree.")
	out := b.String()
	if len(out) > maxRender {
		// Cut on a line boundary so the block never ends mid-path. The cap is
		// prompt space, and every line after the truncation point is detail an
		// agent can recover by reading the tree.
		out = out[:maxRender]
		// A single directory or file name can be long enough to fill the cap
		// on its own; a byte cut there would split a multi-byte UTF-8 sequence
		// and feed the agent mojibake. Trim to a rune boundary first.
		for len(out) > 0 {
			if r, size := utf8.DecodeLastRuneInString(out); r != utf8.RuneError || size > 1 {
				break
			}
			out = out[:len(out)-1]
		}
		if i := strings.LastIndexByte(out, '\n'); i > 0 {
			out = out[:i]
		}
		out += "\n(workspace map truncated)"
	}
	return out
}

func renderCounts(counts []DirCount, dir bool) string {
	if len(counts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		name := c.Name
		if dir {
			if name == "." {
				name = "(root)"
			} else {
				name += "/"
			}
		}
		parts = append(parts, name+" "+strconv.Itoa(c.Files))
	}
	return strings.Join(parts, ", ")
}

// Scan inspects root and returns its report. It never fails: an unreadable
// directory yields an empty report, and a walk that hits its time or count
// budget returns what it has with Truncated set. Nothing here mutates the
// tree; the only subprocess is read-only git plumbing.
func Scan(root string) Report {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return Report{}
	}
	r := Report{Root: abs}
	branch, commit, top, dirty, ok := gitInfo(abs)
	if ok {
		r.Branch, r.Commit, r.Dirty, r.GitKnown = branch, commit, dirty, true
		if top != "" && !samePath(top, abs) {
			r.RepoRoot = top
		}
	}

	langs := map[string]int{}
	dirs := map[string]int{}
	start := time.Now()
	rootDepth := strings.Count(abs, string(filepath.Separator))
	stop := false
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if stop {
			return fs.SkipAll
		}
		if err != nil {
			if d != nil && d.IsDir() && p != abs {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != abs && skipDir(d.Name()) {
				return fs.SkipDir
			}
			if p != abs && pathDepth(p, rootDepth) > 6 {
				// Six levels is past the point a map helps; deeper trees are
				// still reachable through the files named in key files.
				return fs.SkipDir
			}
			if p != abs {
				r.Dirs++
			}
			if r.Dirs >= maxDirs {
				r.Truncated, stop = true, true
				return fs.SkipAll
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(abs, p)
		if rerr != nil {
			return nil
		}
		r.Files++
		seg := firstSegment(rel)
		dirs[seg]++
		if lang, ok := langByExt[strings.ToLower(filepath.Ext(d.Name()))]; ok {
			langs[lang]++
		}
		if r.Files >= maxFiles || time.Since(start) > maxScanDur {
			r.Truncated, stop = true, true
			return fs.SkipAll
		}
		return nil
	})
	if stop {
		r.Truncated = true
	}
	r.TopDirs = topCounts(dirs, 8)
	r.Langs = topCounts(langs, 6)
	r.KeyFiles = keyFiles(abs)
	r.Build, r.Test = commandEntries(abs)
	return r
}

// Cached is Scan with a short process-local cache. Prompt assembly runs once
// per supervision round and a large-tree walk is the one part of that path
// whose cost grows with the repository; the cache keeps rounds cheap without
// making the map stale for the duration of a normal run.
func Cached(root string) Report {
	if root == "" {
		return Report{}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	now := time.Now()
	cacheMu.Lock()
	if e, ok := cache[abs]; ok && now.Before(e.expires) {
		r := e.report
		cacheMu.Unlock()
		return r
	}
	cacheMu.Unlock()

	r := Scan(abs)
	cacheMu.Lock()
	if len(cache) >= 64 {
		for k, e := range cache {
			if !now.Before(e.expires) {
				delete(cache, k)
			}
		}
		if len(cache) >= 64 {
			for k := range cache {
				delete(cache, k)
				break
			}
		}
	}
	cache[abs] = cachedReport{report: r, expires: now.Add(2 * time.Minute)}
	cacheMu.Unlock()
	return r
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cachedReport{}
)

type cachedReport struct {
	report  Report
	expires time.Time
}

func skipDir(name string) bool {
	if skipDirs[name] {
		return true
	}
	// Dot directories are caches, editor state or VCS internals. .github is
	// the one every project relies on for CI, but its files are not source.
	return strings.HasPrefix(name, ".")
}

func firstSegment(rel string) string {
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		return rel[:i]
	}
	return "."
}

func pathDepth(p string, base int) int {
	return strings.Count(p, string(filepath.Separator)) - base
}

func topCounts(m map[string]int, n int) []DirCount {
	out := make([]DirCount, 0, len(m))
	for name, files := range m {
		out = append(out, DirCount{Name: name, Files: files})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func keyFiles(root string) []string {
	var out []string
	seen := map[string]bool{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	present := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			present[strings.ToLower(e.Name())] = e.Name()
		}
	}
	for _, name := range keyNames {
		lower := strings.ToLower(name)
		if actual, ok := present[lower]; ok && !seen[lower] {
			seen[lower] = true
			out = append(out, actual)
		}
	}
	if fi, err := os.Stat(filepath.Join(root, ".github", "workflows")); err == nil && fi.IsDir() {
		out = append(out, ".github/workflows/")
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// commandEntries detects the project's own build and verification commands.
// The order matters: a declared Makefile/package.json target is the project's
// convention and wins over a generic manifest command, because running the
// convention is what CI and the maintainers actually expect.
func commandEntries(root string) (build, test []Command) {
	// A Makefile build target is the project's own convention, so it leads.
	for _, c := range makeCommands(root) {
		if c.Run == "make build" {
			build = append(build, c)
		} else {
			test = append(test, c)
		}
	}
	if c := packageCommand(root, "build"); c != nil {
		build = append(build, *c)
	}
	test = append(test, packageVerifyCommands(root)...)

	if exists(root, "go.mod") {
		build = append(build, Command{Run: "go build ./...", Source: "go.mod"})
		test = append(test, Command{Run: "go test ./...", Source: "go.mod"})
	}
	if exists(root, "Cargo.toml") {
		build = append(build, Command{Run: "cargo build", Source: "Cargo.toml"})
		test = append(test, Command{Run: "cargo test", Source: "Cargo.toml"})
	}
	if cmd := pythonTestCommand(root); cmd != "" {
		test = append(test, Command{Run: cmd, Source: "python"})
	}
	if exists(root, "CMakeLists.txt") {
		build = append(build, Command{Run: "cmake -S . -B build && cmake --build build", Source: "CMakeLists.txt"})
	}
	return dedupe(build), dedupe(test)
}

func makeCommands(root string) []Command {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return nil
	}
	if len(data) > 256*1024 {
		data = data[:256*1024]
	}
	declared := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ".") {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		target := strings.TrimSpace(line[:i])
		if strings.ContainsAny(target, " \t=$") {
			continue
		}
		declared[target] = true
	}
	var out []Command
	for _, t := range makeTargets {
		if !declared[t] {
			continue
		}
		// `make fmt`/`make vet` are verification helpers, but the report has
		// two slots; a project without build/test still benefits from them.
		out = append(out, Command{Run: "make " + t, Source: "Makefile"})
	}
	return out
}

// packageCommand returns the declared Node build script, or nil. A
// package.json without one is not evidence of a build command: nx/esbuild/vite
// invocations are common enough that naming one heuristically would be
// guessing, and the map's job is to report what the project declared.
func packageCommand(root, role string) *Command {
	scripts, err := readPackageScripts(root)
	if err != nil {
		return nil
	}
	switch role {
	case "build":
		if _, ok := scripts["build"]; ok {
			return &Command{Run: "npm run build", Source: "package.json"}
		}
	}
	return nil
}

// packageVerifyCommands lists the declared verification scripts. test comes
// first because it is the contract an agent has to satisfy; lint/typecheck are
// the project's own gates and follow in that order.
func packageVerifyCommands(root string) []Command {
	scripts, err := readPackageScripts(root)
	if err != nil {
		return nil
	}
	var out []Command
	for _, name := range []string{"test", "lint", "typecheck", "check"} {
		if _, ok := scripts[name]; ok {
			out = append(out, Command{Run: "npm run " + name, Source: "package.json"})
		}
	}
	return out
}

// pythonTestCommand names the Python test entry only when a manifest actually
// configures one. A bare tests/ directory is not evidence: this repository's
// own tests/ holds adapter contract scripts run by `make adapter-test`, and
// reporting `pytest` there would send an agent to a runner the project never
// installed.
func pythonTestCommand(root string) string {
	if exists(root, "pytest.ini") {
		return "python3 -m pytest -q"
	}
	if exists(root, "tox.ini") {
		if containsText(filepath.Join(root, "tox.ini"), "[testenv") {
			return "tox"
		}
	}
	if exists(root, "pyproject.toml") && containsText(filepath.Join(root, "pyproject.toml"), "[tool.pytest") {
		return "python3 -m pytest -q"
	}
	if exists(root, "setup.cfg") && containsText(filepath.Join(root, "setup.cfg"), "[tool:pytest]") {
		return "python3 -m pytest -q"
	}
	return ""
}

// containsText reports whether a small manifest file carries marker. Manifests
// are configuration, not source; the read is bounded like the Makefile one.
func containsText(path, marker string) bool {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 256*1024 {
		return false
	}
	return strings.Contains(string(data), marker)
}

func readPackageScripts(root string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, os.ErrInvalid
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, err
	}
	return pkg.Scripts, nil
}

func dedupe(cmds []Command) []Command {
	seen := map[string]bool{}
	out := cmds[:0]
	for _, c := range cmds {
		if c.Run == "" || seen[c.Run] {
			continue
		}
		seen[c.Run] = true
		out = append(out, c)
	}
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

func exists(root, name string) bool {
	fi, err := os.Stat(filepath.Join(root, name))
	return err == nil && !fi.IsDir()
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return aa == bb
}

// gitInfo reads repository identity with read-only plumbing. A missing git,
// a directory outside any repository or a timed-out command all yield ok
// false; the map simply omits the git line.
func gitInfo(root string) (branch, commit, top string, dirty, ok bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", "", "", false, false
	}
	top = gitOut(root, "rev-parse", "--show-toplevel")
	if top == "" {
		return "", "", "", false, false
	}
	branch = gitOut(root, "rev-parse", "--abbrev-ref", "HEAD")
	commit = gitOut(root, "rev-parse", "--short", "HEAD")
	// --quiet exits 1 when the index differs from HEAD. That is the only bit
	// the map needs; counting changed paths on a huge tree costs much more.
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "--no-optional-locks", "diff-index", "--quiet", "HEAD", "--")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		if ee, isExit := err.(*exec.ExitError); isExit && ee.ExitCode() == 1 {
			dirty = true
		}
	}
	return branch, commit, top, dirty, branch != "" || commit != ""
}

func gitOut(root string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	full := append([]string{"-C", root, "--no-optional-locks"}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
