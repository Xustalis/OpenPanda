package main

// `panda plan` is the plan plane's user-facing surface: start a multi-stage,
// multi-device pipeline from a file, and follow it stage by stage.
//
// It exists because the plane was otherwise unreachable. Every piece of the
// flagship scenario — stage dependencies, artifacts travelling between machines,
// a stage routed by the VRAM it declares — was implemented and tested, and there
// was no command that could start one. This is that command.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

func runPlan(args []string) {
	if len(args) == 0 {
		planUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "run", "start":
		runPlanStart(args[1:])
	case "show", "status":
		runPlanShow(args[1:])
	case "example", "init":
		fmt.Print(plan.ExampleYAML)
	case "help", "-h", "--help":
		planUsage()
	default:
		planUsage()
		os.Exit(2)
	}
}

func planUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda plan <verb>")
	fmt.Fprintln(os.Stderr, "  run <file.yaml>       start a multi-stage plan (use - for stdin)")
	fmt.Fprintln(os.Stderr, "  show <plan-id>        list a plan's stages and their states")
	fmt.Fprintln(os.Stderr, "  example               print a runnable example plan to edit")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "A stage is an ordinary task: it queues, routes across devices, retries,")
	fmt.Fprintln(os.Stderr, "and parks in review when it needs approval. A plan adds the ordering and")
	fmt.Fprintln(os.Stderr, "hands each stage's output to the next.")
}

// runPlanStart parses a plan file, hands it to the scheduler core, and prints the
// plan id plus the stages that were released immediately. The daemon does the
// rest: releasing successors is a completion hook plus a sweep, so the command
// does not need to stay attached for the plan to finish.
func runPlanStart(args []string) {
	fs := flag.NewFlagSet("plan run", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", defaultCardPath(), "path to capabilities.yaml (default: discovered)")
	priority := fs.String("priority", "normal", "priority for every stage: "+cliPriorities)
	dryRun := fs.Bool("dry-run", false, "validate and print the stage order without creating anything")
	fs.Parse(reorderFlags(args, commonValueFlags))

	path := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if path == "" {
		planUsage()
		os.Exit(2)
	}
	prio, ok := parseCLIPriority(*priority)
	if !ok {
		fmt.Fprintf(os.Stderr, "panda: unknown priority %q (want %s)\n", *priority, cliPriorities)
		os.Exit(2)
	}

	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		fatal("read plan", err)
	}
	p, err := plan.Parse(data)
	if err != nil {
		// A plan that does not validate has created nothing, so this is the one
		// failure that costs the user only a re-edit. Say what is wrong and stop.
		fmt.Fprintf(os.Stderr, "panda: %v\n", err)
		os.Exit(1)
	}

	if *dryRun {
		fmt.Printf("goal: %s\n", p.Goal)
		order, oerr := plan.Order(p)
		if oerr != nil {
			fatal("order plan", oerr)
		}
		for i, st := range order {
			fmt.Printf("  %d. %-14s requires=%s needs=%s%s\n", i+1, st.ID,
				orDash(strings.Join(st.Requires, ",")), orDash(strings.Join(st.Needs, ",")),
				resourceSummary(st.Resources))
		}
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{CardPath: *cardPath})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	q := core.DefaultQueueSpec()
	q.Priority = prio
	// Deliberately no WorkDir: a path from this machine is meaningless on the
	// machine that runs the stage, so each executor derives its own.
	q.WorkDir = ""

	planID, err := engine.StartPlan(context.Background(), p, q)
	if err != nil {
		fatal("start plan", err)
	}
	stages, serr := engine.PlanStages(context.Background(), planID)
	if serr != nil {
		fatal("read plan", serr)
	}

	if jsonOutput {
		emitJSON(planToJSON(planID, p.Goal, stages))
		return
	}
	fmt.Printf("plan:   %s\n", planID)
	fmt.Printf("goal:   %s\n", p.Goal)
	fmt.Printf("stages: %d\n", len(stages))
	printPlanStages(stages)
	fmt.Printf("\nfollow: panda plan show %s\n", planID)
}

// runPlanShow lists one plan's stages. It reads the store directly rather than
// building an engine: following a plan must work while the daemon owns it.
func runPlanShow(args []string) {
	fs := flag.NewFlagSet("plan show", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		planUsage()
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	stages, err := store.PlanStages(context.Background(), id)
	if err != nil {
		fatal("read plan", err)
	}
	if len(stages) == 0 {
		fmt.Fprintf(os.Stderr, "panda: no such plan: %s\n", id)
		os.Exit(1)
	}
	if jsonOutput {
		emitJSON(planToJSON(id, "", stages))
		return
	}
	fmt.Printf("plan:   %s\n", id)
	fmt.Printf("stages: %d\n", len(stages))
	printPlanStages(stages)
}

// printPlanStages renders the board for one plan: what each stage is waiting for,
// where it ran, and whether it handed anything on. The artifact column is the one
// that answers "did the training stage actually get the script?".
func printPlanStages(stages []core.Task) {
	for _, t := range stages {
		fmt.Printf("  %-14s %-12s owner=%-16s needs=%s\n",
			t.StageID, t.State, orDash(t.OwnerNode), orDash(strings.Join(t.Needs, ",")))
		if len(t.Inputs) > 0 {
			for _, in := range t.Inputs {
				fmt.Printf("      in   <- %s %s from %s\n", in.Stage, shortHash(in.Hash), in.Source)
			}
		}
		if t.OutputArtifact != "" {
			fmt.Printf("      out  -> %s\n", shortHash(t.OutputArtifact))
		}
	}
}

type planStageJSON struct {
	Stage  string   `json:"stage"`
	TaskID string   `json:"task_id"`
	State  string   `json:"state"`
	Owner  string   `json:"owner,omitempty"`
	Needs  []string `json:"needs,omitempty"`
	Output string   `json:"output_artifact,omitempty"`
}

func planToJSON(planID, goal string, stages []core.Task) map[string]any {
	out := make([]planStageJSON, 0, len(stages))
	for _, t := range stages {
		out = append(out, planStageJSON{
			Stage:  t.StageID,
			TaskID: t.TaskID,
			State:  t.State,
			Owner:  t.OwnerNode,
			Needs:  t.Needs,
			Output: t.OutputArtifact,
		})
	}
	m := map[string]any{"plan_id": planID, "stages": out}
	if goal != "" {
		m["goal"] = goal
	}
	return m
}

// resourceSummary renders only the fields a stage actually asked for, so a
// dry-run listing shows "gpu_vram_gb=8" on the training stage and nothing on the
// others instead of four zeroes on every line.
func resourceSummary(r entry.ResourceProfile) string {
	var parts []string
	if r.CPU > 0 {
		parts = append(parts, fmt.Sprintf("cpu=%d", r.CPU))
	}
	if r.RAMGB > 0 {
		parts = append(parts, fmt.Sprintf("ram_gb=%g", r.RAMGB))
	}
	if r.GPUVRAMGB > 0 {
		parts = append(parts, fmt.Sprintf("gpu_vram_gb=%g", r.GPUVRAMGB))
	}
	if r.DurationHint != "" {
		parts = append(parts, "duration="+r.DurationHint)
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
