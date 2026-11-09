// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

// Read-only workspace tools for the side channel.
//
// The side channel exists to answer questions beside the main thread. Its
// observation tools cover live node state, but the most common question — "what
// does this function do", "where is this flag read", "what changed in this
// package" — is about the workspace itself. Without a way to read the tree the
// channel can only guess at code or tell the user to go ask the main thread,
// which is exactly the dead end it was opened to avoid.
//
// Everything here is confined to the ask's work dir: a path is resolved through
// symlinks and then checked to sit under the resolved root, so neither ".." nor
// a link can reach outside it. The tools only read — no writes, no execution —
// which is why the side ask's allow-list keeps them while it drops every
// mutation and every dispatch path.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/projects"
)

const (
	// wsReadBytes caps one workspace_read result. A prompt is a context
	// window, not a pipe: a generated lock file must not crowd out the
	// conversation it was opened beside.
	wsReadBytes = 64 << 10
	// wsReadLines caps a read even when the byte budget allows more lines —
	// hundreds of one-character lines are still hundreds of messages.
	// The default used to be 200, which split common project files (README,
	// CLAUDE.md) across two or three round trips: the model kept re-reading
	// the same file and burned the whole tool-round budget before answering.
	// A wider default still fits the per-result context bound in toolbudget.go.
	wsReadLines        = 600
	wsReadDefaultLines = 300
	// wsListEntries caps one directory listing. A node_modules tree is not a
	// report.
	wsListEntries = 200
	// wsSearch* bound a search: hits and files both. Without the file cap a
	// regex over a monorepo is a minute of I/O behind one question.
	wsSearchHits      = 40
	wsSearchFileBytes = 2 << 20
	wsSearchFiles     = 4000
	// wsSearchTotalBytes bounds the bytes actually read. The per-file and
	// per-file-count caps together still allow gigabytes across a monorepo;
	// 64 MiB of text is already ~10 seconds of grep on a slow disk.
	wsSearchTotalBytes = 64 << 20
	wsSearchLineRunes  = 240
)

// wsSkipDirs are trees a code search never wants: version control, installed
// dependencies, build output and editor state. They are skipped by name at any
// depth, the same way every developer's own ripgrep config does.
var wsSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "bower_components": true,
	"vendor": true, ".venv": true, "venv": true, "__pycache__": true,
	"dist": true, "build": true, "bin": true, "target": true,
	".next": true, ".nuxt": true, ".cache": true, "coverage": true,
	".idea": true, ".vscode": true, ".openpanda": true,
}

// registerWorkspaceTools adds the read-only workspace family to one ask's
// registry, closed over that ask's work dir. An empty or too-broad work dir
// registers nothing: a channel pinned to $HOME or / is a whole-disk reader,
// which is wider than any project.
func registerWorkspaceTools(reg *entry.Registry, workDir string) {
	root, ok := workspaceRoot(workDir)
	if !ok || reg == nil {
		return
	}
	reg.Register(entry.Tool{
		Name: "workspace_read",
		Description: "Read a text file inside this conversation's workspace. Returns numbered lines. " +
			"Prefer workspace_search when you only need specific content; read a file once and answer from what you have, " +
			"paging with start_line/max_lines only when a specific missing section is essential. / " +
			"读取当前工作区内的文本文件，返回带行号的内容。只需要特定内容时优先用 workspace_search；" +
			"读一次即可据此作答，仅在确实缺少关键段落时才用 start_line/max_lines 继续翻页。",
		Tier: defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "File path, relative to the workspace / 相对工作区的文件路径"},
				"start_line": map[string]any{"type": "integer", "description": "First line to return, 1-based (default 1) / 起始行（从 1 开始）"},
				"max_lines":  map[string]any{"type": "integer", "description": "Maximum lines to return (default 300, cap 600) / 最多返回行数（默认 300，上限 600）"},
			},
			"required": []string{"path"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return wsRead(root, args)
		},
	})

	reg.Register(entry.Tool{
		Name: "workspace_list",
		Description: "List a directory inside this conversation's workspace: entries, sizes, and which are directories. " +
			"Use it to find the file to read or search. / " +
			"列出当前工作区内的目录内容（名称、大小、是否为目录），用来定位要读或搜索的文件。",
		Tier: defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory path relative to the workspace (default .) / 相对工作区的目录路径"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return wsList(root, args)
		},
	})

	reg.Register(entry.Tool{
		Name: "workspace_search",
		Description: "Search file contents inside this conversation's workspace with a regular expression. " +
			"Returns file:line: text, bounded to the first matches. Skips .git, dependencies and build output. / " +
			"用正则搜索当前工作区内的文件内容，返回 file:line: 文本，结果有上限；自动跳过 .git、依赖和构建产物。",
		Tier: defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string", "description": "Regular expression / 正则表达式"},
				"path":        map[string]any{"type": "string", "description": "Directory or file to search, relative to the workspace (default .) / 搜索范围（相对工作区）"},
				"ignore_case": map[string]any{"type": "boolean", "description": "Case-insensitive match / 忽略大小写"},
			},
			"required": []string{"pattern"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return wsSearch(ctx, root, args)
		},
	})
}

// workspaceRoot resolves the ask's work dir to a real path and refuses one the
// projects store would refuse to create: the filesystem root, the home
// directory, or anything above it wide enough to be a whole-disk reader.
func workspaceRoot(workDir string) (string, bool) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return "", false
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", false
	}
	if projects.ValidateWorkDir(abs) != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return abs, true
}

// workspaceResolve turns a model-supplied path into an absolute one inside
// root. The symlink resolution happens before the containment check, so a link
// planted inside the workspace cannot point the read at /etc or ~/.ssh. A
// missing path resolves through its parent: "no such file" must stay a tool
// result, not a boundary refusal.
func workspaceResolve(root, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path is required")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		parent, perr := filepath.EvalSymlinks(filepath.Dir(abs))
		if perr != nil {
			return "", errors.New("no such path")
		}
		real = filepath.Join(parent, filepath.Base(abs))
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the workspace")
	}
	return real, nil
}

// wsRead implements workspace_read.
func wsRead(root string, args map[string]any) (string, error) {
	rel := stringArg(args, "path")
	abs, err := workspaceResolve(root, rel)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("no such file: %s", rel)
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory — use workspace_list", rel)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", rel)
	}
	start := intArg(args, "start_line")
	if start <= 0 {
		start = 1
	}
	maxLines := intArg(args, "max_lines")
	if maxLines <= 0 {
		maxLines = wsReadDefaultLines
	}
	if maxLines > wsReadLines {
		maxLines = wsReadLines
	}

	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	defer f.Close()

	// A NUL in the first block marks this as binary. Reading it as text would
	// hand the model a megabyte of mojibake for a question it cannot answer.
	prefix := make([]byte, 4096)
	n, _ := f.Read(prefix)
	if bytes.IndexByte(prefix[:n], 0) >= 0 {
		return fmt.Sprintf("%s is a binary file (%s); it cannot be shown as text.", rel, wsSize(st.Size())), nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var b strings.Builder
	lineNo, emitted, used := 0, 0, 0
	truncated := false
	for sc.Scan() {
		lineNo++
		if lineNo < start {
			continue
		}
		if emitted >= maxLines || used >= wsReadBytes {
			truncated = true
			break
		}
		text := sc.Text()
		if utf8.RuneCountInString(text) > 2000 {
			text = string([]rune(text)[:2000]) + " …(line truncated)"
			truncated = true
		}
		fmt.Fprintf(&b, "%6d\t%s\n", lineNo, text)
		used += len(text) + 8
		emitted++
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	if emitted == 0 {
		return fmt.Sprintf("No lines at or after line %d; the file has %d lines.", start, lineNo), nil
	}
	if truncated {
		// The old trailer ("continue with start_line=N") read as an
		// instruction to keep paging. On an "analyse this project" ask the
		// model obeyed it literally and spent every tool round re-reading
		// files instead of producing the answer. State the fact, point at
		// the targeted tool, and leave paging as an explicit last resort.
		fmt.Fprintf(&b, "\n[showing lines %d-%d; more content exists. Use workspace_search for specific text; "+
			"start_line=%d only if a missing section is essential]", start, start+emitted-1, start+emitted)
	}
	return b.String(), nil
}

// wsList implements workspace_list.
func wsList(root string, args map[string]any) (string, error) {
	rel := stringArg(args, "path")
	if rel == "" {
		rel = "."
	}
	abs, err := workspaceResolve(root, rel)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("no such directory: %s", rel)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is a file — use workspace_read", rel)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("list %s: %w", rel, err)
	}
	sortedEntries(entries)
	visible := 0
	for _, e := range entries {
		if !wsSkipDirs[e.Name()] {
			visible++
		}
	}
	var b strings.Builder
	shown := 0
	for _, e := range entries {
		if wsSkipDirs[e.Name()] {
			continue
		}
		if shown >= wsListEntries {
			break
		}
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
		} else if info, ierr := e.Info(); ierr == nil {
			fmt.Fprintf(&b, "%s  (%s)\n", e.Name(), wsSize(info.Size()))
		} else {
			fmt.Fprintf(&b, "%s\n", e.Name())
		}
		shown++
	}
	if shown == 0 {
		return fmt.Sprintf("%s is empty.", rel), nil
	}
	if shown < visible {
		fmt.Fprintf(&b, "[showing %d of %d entries]", shown, visible)
	}
	return b.String(), nil
}

// wsSearch implements workspace_search.
func wsSearch(ctx context.Context, root string, args map[string]any) (string, error) {
	pattern := stringArg(args, "pattern")
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	if boolArg(args, "ignore_case") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regular expression: %v", err)
	}
	rel := stringArg(args, "path")
	if rel == "" {
		rel = "."
	}
	start, err := workspaceResolve(root, rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(start); err != nil {
		return "", fmt.Errorf("no such path: %s", rel)
	}

	var b strings.Builder
	hits, files := 0, 0
	var scanned int64
	truncated := false
	walk := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path != start && wsSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if files >= wsSearchFiles {
			truncated = true
			return filepath.SkipAll
		}
		files++
		info, ierr := d.Info()
		if ierr != nil || info.Size() > wsSearchFileBytes {
			return nil
		}
		if scanned+info.Size() > wsSearchTotalBytes {
			truncated = true
			return filepath.SkipAll
		}
		scanned += info.Size()
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil
		}
		defer f.Close()
		prefix := make([]byte, 2048)
		n, _ := f.Read(prefix)
		if bytes.IndexByte(prefix[:n], 0) >= 0 {
			return nil
		}
		if _, serr := f.Seek(0, io.SeekStart); serr != nil {
			return nil
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if !re.MatchString(line) {
				continue
			}
			relPath, rerr := filepath.Rel(root, path)
			if rerr != nil {
				relPath = path
			}
			text := strings.TrimSpace(line)
			if utf8.RuneCountInString(text) > wsSearchLineRunes {
				text = string([]rune(text)[:wsSearchLineRunes]) + "…"
			}
			fmt.Fprintf(&b, "%s:%d: %s\n", filepath.ToSlash(relPath), lineNo, text)
			hits++
			if hits >= wsSearchHits {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	}
	// WalkDir handles a single file as a one-entry walk, so the same callback
	// serves "search this subtree" and "search this file".
	werr := filepath.WalkDir(start, walk)
	if werr != nil && !errors.Is(werr, filepath.SkipAll) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("search %s: %w", rel, werr)
	}
	if hits == 0 {
		return fmt.Sprintf("No matches for %q under %s.", pattern, rel), nil
	}
	out := b.String()
	if truncated {
		out += fmt.Sprintf("[stopped at %d matches after %d files; narrow the pattern or path]", hits, files)
	}
	return out, nil
}

// wsSize renders a byte count for a tool result.
func wsSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// stringArg reads a string tool argument.
func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// boolArg reads a boolean tool argument.
func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

// sortedEntries orders a directory listing the way a reader scans it: dirs
// first, then names, case-insensitively.
func sortedEntries(entries []os.DirEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
}
