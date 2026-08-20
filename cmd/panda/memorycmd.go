package main

// `panda memory` — user management of the node's memory files (A3/A4): the
// personal files (USER.md / MEMORY.md / DREAMS.md), topic files, project
// files, and the read-only daily logs. set validates through the Hermes
// format (entries split on §, character caps) and writes atomically like the
// web console's memory editor.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

// memoryTarget is one addressable memory file.
type memoryTarget struct {
	name     string // display name, e.g. "topic:coffee"
	path     string // absolute path on disk
	writable bool
	load     func() (memory.MemFile, error)
	save     func(memory.MemFile) error
	remove   func() error
}

// resolveMemoryTarget maps the CLI name vocabulary onto a concrete file:
// user | memory | dreams | topic:<name> | project:<name> | daily:<date>.
func resolveMemoryTarget(cfg *config.Config, name string) (memoryTarget, error) {
	loc := i18n.Detect()
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memoryLimits(cfg))
	projects := memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg))
	name = strings.TrimSpace(name)

	switch {
	case name == "user" || name == "USER.md":
		return memoryTarget{
			name: "user", path: memory.UserPath(cfg.Storage.MemoryPath), writable: true,
			load: hermes.LoadUser, save: hermes.SaveUser,
		}, nil
	case name == "memory" || name == "MEMORY.md":
		return memoryTarget{
			name: "memory", path: memory.MemoryPath(cfg.Storage.MemoryPath), writable: true,
			load: hermes.LoadMemory, save: hermes.SaveMemory,
		}, nil
	case name == "dreams" || name == "DREAMS.md":
		return memoryTarget{
			name: "dreams", path: filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"), writable: false,
			load: func() (memory.MemFile, error) {
				return loadMemFile(hermes, filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"))
			},
		}, nil
	case strings.HasPrefix(name, "topic:"):
		topic := strings.TrimPrefix(name, "topic:")
		if err := memory.ValidateName(topic); err != nil {
			return memoryTarget{}, fmt.Errorf("%s: %w", i18n.T(loc, "cli.memory.badName"), err)
		}
		return memoryTarget{
			name: "topic:" + topic, writable: true,
			load:   func() (memory.MemFile, error) { return hermes.LoadTopic(topic) },
			save:   func(m memory.MemFile) error { return hermes.SaveTopic(topic, m) },
			remove: func() error { return hermes.DeleteTopic(topic) },
			path:   func() string { p, _ := hermes.TopicPath(topic); return p }(),
		}, nil
	case strings.HasPrefix(name, "project:"):
		project := strings.TrimPrefix(name, "project:")
		if err := memory.ValidateName(project); err != nil {
			return memoryTarget{}, fmt.Errorf("%s: %w", i18n.T(loc, "cli.memory.badName"), err)
		}
		return memoryTarget{
			name: "project:" + project, writable: true,
			load: func() (memory.MemFile, error) { return projects.Load(project) },
			save: func(m memory.MemFile) error { return projects.Save(project, m) },
			path: func() string { p, _ := projects.Path(project); return p }(),
		}, nil
	case strings.HasPrefix(name, "daily:"):
		date := strings.TrimPrefix(name, "daily:")
		var day time.Time
		switch strings.ToLower(date) {
		case "today":
			day = time.Now()
		case "yesterday":
			day = time.Now().AddDate(0, 0, -1)
		default:
			parsed, err := time.Parse("2006-01-02", date)
			if err != nil {
				return memoryTarget{}, fmt.Errorf("%s", i18n.Tf(loc, "cli.memory.badDaily", "date", date))
			}
			day = parsed
		}
		daily := memory.NewDaily(hermes.WarmDir())
		return memoryTarget{
			name: "daily:" + day.Format("2006-01-02"), path: daily.PathFor(day), writable: false,
		}, nil
	}
	return memoryTarget{}, fmt.Errorf("%s", i18n.Tf(loc, "cli.memory.unknownName", "name", name))
}

// loadMemFile reads an arbitrary § file under the hermes root (DREAMS.md).
func loadMemFile(h *memory.Hermes, path string) (memory.MemFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return memory.MemFile{}, nil
		}
		return memory.MemFile{}, err
	}
	return memory.ParseMem(data), nil
}

func runMemory(args []string) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "ls":
		runMemoryList(rest)
	case "get", "show", "cat":
		runMemoryGet(rest)
	case "set":
		runMemorySet(rest)
	case "rm", "delete":
		runMemoryRm(rest)
	case "help", "-h", "--help":
		memoryUsage()
	default:
		fmt.Fprintf(os.Stderr, "panda: unknown memory verb %q\n", verb)
		memoryUsage()
		os.Exit(2)
	}
}

func memoryUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda memory <verb>")
	fmt.Fprintln(os.Stderr, "  list                    index of every memory file (selective-load manifest)")
	fmt.Fprintln(os.Stderr, "  get <name>              print a file's raw content")
	fmt.Fprintln(os.Stderr, "  set <name> [--file F]   replace a file (stdin by default; § separates entries)")
	fmt.Fprintln(os.Stderr, "  rm topic:<name>         delete a topic file")
	fmt.Fprintln(os.Stderr, "names: user | memory | dreams | topic:<n> | project:<n> | daily:<date>")
	fmt.Fprintln(os.Stderr, "       (dreams and daily:<date> are read-only)")
}

func runMemoryList(args []string) {
	fs := flag.NewFlagSet("memory list", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memoryLimits(cfg))
	projects := memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg))

	files, err := hermes.Files()
	if err != nil {
		fatal("index memory", err)
	}

	type row struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Entries int    `json:"entries"`
		Chars   int    `json:"chars"`
		Summary string `json:"summary,omitempty"`
	}
	rows := make([]row, 0, len(files)+4)
	for _, f := range files {
		name := f.Name
		switch name {
		case "USER.md":
			name = "user"
		case "MEMORY.md":
			name = "memory"
		default:
			name = "topic:" + strings.TrimSuffix(strings.TrimPrefix(name, "topics/"), ".md")
		}
		rows = append(rows, row{Name: name, Path: f.Path, Entries: f.Entries, Chars: f.Chars, Summary: f.Summary})
	}
	// DREAMS.md (dream consolidation output), when present.
	if m, err := loadMemFile(hermes, filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md")); err == nil && len(m.Entries) > 0 {
		rows = append(rows, row{Name: "dreams", Path: filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md"), Entries: len(m.Entries), Chars: m.Chars(), Summary: summarizeFirst(m.Entries)})
	}
	// Project memories.
	if names, err := projects.List(); err == nil {
		sort.Strings(names)
		for _, n := range names {
			if m, err := projects.Load(n); err == nil && len(m.Entries) > 0 {
				path, _ := projects.Path(n)
				rows = append(rows, row{Name: "project:" + n, Path: path, Entries: len(m.Entries), Chars: m.Chars(), Summary: summarizeFirst(m.Entries)})
			}
		}
	}
	// Daily logs (read-only).
	if entries, err := os.ReadDir(hermes.WarmDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			rows = append(rows, row{Name: "daily:" + strings.TrimSuffix(e.Name(), ".md"), Path: filepath.Join(hermes.WarmDir(), e.Name())})
		}
	}

	if jsonOutput {
		emitJSON(rows)
		return
	}
	if len(rows) == 0 {
		fmt.Println(i18n.T(loc, "cli.memory.none"))
		return
	}
	fmt.Println(i18n.T(loc, "cli.memory.head"))
	for _, r := range rows {
		if r.Entries > 0 {
			fmt.Printf("  %-22s entries=%-4d chars=%-6d %s\n", r.Name, r.Entries, r.Chars, r.Summary)
		} else {
			fmt.Printf("  %-22s (read-only)\n", r.Name)
		}
	}
}

func summarizeFirst(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	s := strings.TrimSpace(entries[0])
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

func runMemoryGet(args []string) {
	fs := flag.NewFlagSet("memory get", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	if fs.Arg(0) == "" {
		fmt.Fprintln(os.Stderr, "usage: panda memory get <name>")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	target, err := resolveMemoryTarget(cfg, fs.Arg(0))
	if err != nil {
		fatal("memory", err)
	}
	data, err := os.ReadFile(target.path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "panda: %s: empty or missing\n", target.name)
			return
		}
		fatal("read memory", err)
	}
	if jsonOutput {
		emitJSON(map[string]string{"name": target.name, "path": target.path, "content": string(data)})
		return
	}
	os.Stdout.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		fmt.Println()
	}
}

func runMemorySet(args []string) {
	fs := flag.NewFlagSet("memory set", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	filePath := fs.String("file", "", "read the new content from this file (default: stdin)")
	fs.Parse(args)
	if fs.Arg(0) == "" {
		fmt.Fprintln(os.Stderr, "usage: panda memory set <name> [--file F]  (content from stdin otherwise)")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()
	target, err := resolveMemoryTarget(cfg, fs.Arg(0))
	if err != nil {
		fatal("memory", err)
	}
	if !target.writable || target.save == nil {
		fmt.Fprintln(os.Stderr, i18n.Tf(loc, "cli.memory.readonly", "name", target.name))
		os.Exit(1)
	}

	var data []byte
	if *filePath != "" {
		data, err = os.ReadFile(*filePath)
	} else {
		data, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fatal("read input", err)
	}

	// Validate through the Hermes format: entries split on §, caps enforced
	// by the store's Save — same contract as the web editor.
	m := memory.ParseMem(data)
	if err := target.save(m); err != nil {
		fatal("save memory", err)
	}
	if jsonOutput {
		emitJSON(map[string]any{"name": target.name, "entries": len(m.Entries), "chars": m.Chars()})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.memory.saved", "name", target.name, "entries", fmt.Sprint(len(m.Entries)), "chars", fmt.Sprint(m.Chars())))
}

func runMemoryRm(args []string) {
	fs := flag.NewFlagSet("memory rm", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	if fs.Arg(0) == "" {
		fmt.Fprintln(os.Stderr, "usage: panda memory rm topic:<name>")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()
	target, err := resolveMemoryTarget(cfg, fs.Arg(0))
	if err != nil {
		fatal("memory", err)
	}
	if target.remove == nil {
		fmt.Fprintln(os.Stderr, i18n.T(loc, "cli.memory.rmTopicOnly"))
		os.Exit(1)
	}
	if err := target.remove(); err != nil {
		fatal("delete memory", err)
	}
	fmt.Println(i18n.Tf(loc, "cli.memory.removed", "name", target.name))
}
