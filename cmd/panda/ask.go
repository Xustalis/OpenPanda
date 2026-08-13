package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/core"
	"github.com/xenith/panda/internal/entry"
	"github.com/xenith/panda/internal/ledger"
	"github.com/xenith/panda/internal/storage"
)

// runAsk implements `panda ask "..."` — one call through the unified entry
// model. answer/tool_call print the classified output; task additionally
// executes it on this node via core.SubmitLocal (the Phase 1 local closed
// loop).
func runAsk(args []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", "", "path to capabilities.yaml (required to execute tasks)")
	fs.Parse(args)

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		if stat, _ := os.Stdin.Stat(); stat != nil && stat.Mode()&os.ModeCharDevice == 0 {
			// Non-tty stdin: read the whole stream as the prompt.
			b, _ := readStdin()
			prompt = strings.TrimSpace(string(b))
		}
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: panda ask [--config PATH] [--card PATH] \"<question>\"")
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}

	// Open the DB for a device summary and, for task output, persistence. A
	// missing DB is fine for answer/tool_call (empty devices), but a task
	// cannot execute without one.
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		fatal("migrate database", err)
	}

	var devices []ledger.Node
	if devices, err = ledger.Query(db, "online", ""); err != nil {
		devices = nil
	}

	client := entry.NewClient(cfg.Model)
	out, err := entry.Classify(context.Background(), client, devices, "", prompt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		os.Exit(1)
	}

	switch out.Kind {
	case entry.KindAnswer:
		fmt.Println(out.Answer)
	case entry.KindToolCall:
		b, _ := json.MarshalIndent(out.Tool, "", "  ")
		fmt.Printf("tool_call: %s\n", string(b))
	case entry.KindTask:
		runAskTask(cfg, *cardPath, db, out.Task)
	default:
		fmt.Println(out.Answer)
	}
}

// runAskTask executes a classified task locally and prints the outcome.
func runAskTask(cfg *config.Config, cardPath string, db *sql.DB, spec *entry.TaskSpec) {
	if cardPath == "" {
		fmt.Fprintln(os.Stderr, "panda: task output requires --card (capabilities.yaml)")
		os.Exit(1)
	}
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		fatal("load capabilities", err)
	}

	// A quiet logger so the ask CLI stays clean; the daemon (runDaemon) uses
	// structured JSON logging instead.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := core.NewCore(db, core.NodeID(cfg.Node.Name), card, schedulerTier(cfg.Node.ResourceClass), logger, cfg.Model)

	task, result, err := c.SubmitLocal(context.Background(), toTaskInput(spec))
	if err != nil {
		fmt.Fprintf(os.Stderr, "panda: task failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("task %s %s\n", task.TaskID, task.State)
	if result.OK {
		fmt.Print(result.Stdout)
	} else {
		fmt.Fprintf(os.Stderr, "exit %d: %s\n", result.ExitCode, result.Stderr)
		os.Exit(1)
	}
}

// toTaskInput translates the entry model's TaskSpec into the core's local
// TaskInput. Intent is composed from the spec fields so the agent adapter
// receives one actionable natural-language instruction (design doc §7.3: the
// refined intent is the spec from the same call — no separate refinement step
// in MVP).
func toTaskInput(spec *entry.TaskSpec) core.TaskInput {
	var constraints strings.Builder
	for i, c := range spec.Spec.Constraints {
		if i > 0 {
			constraints.WriteString("；")
		}
		constraints.WriteString(c)
	}

	var intent strings.Builder
	intent.WriteString(spec.Title)
	if spec.Spec.Target != "" {
		fmt.Fprintf(&intent, "\n目标：%s", spec.Spec.Target)
	}
	if spec.Spec.Scope != "" {
		fmt.Fprintf(&intent, "\n范围：%s", spec.Spec.Scope)
	}
	if constraints.Len() > 0 {
		fmt.Fprintf(&intent, "\n约束：%s", constraints.String())
	}
	if spec.Spec.SuccessDefinition != "" {
		fmt.Fprintf(&intent, "\n成功标准：%s", spec.Spec.SuccessDefinition)
	}

	specJSON, _ := json.Marshal(spec.Spec)
	resourceJSON, _ := json.Marshal(spec.Resources)

	return core.TaskInput{
		Title:        spec.Title,
		Project:      spec.Project,
		ContextType:  spec.ContextType,
		Intent:       intent.String(),
		SpecJSON:     string(specJSON),
		Requires:     spec.Requires.Abilities,
		Complexity:   spec.Complexity,
		Risk:         spec.Risk,
		ResourceJSON: string(resourceJSON),
	}
}

func readStdin() ([]byte, error) {
	return os.ReadFile("/dev/stdin")
}
