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
	"time"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/core"
	"github.com/xenith/panda/internal/entry"
	"github.com/xenith/panda/internal/ledger"
	"github.com/xenith/panda/internal/mcp"
	"github.com/xenith/panda/internal/memory"
	"github.com/xenith/panda/internal/skills"
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
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated), e.g. \"npx -y @modelcontextprotocol/server-filesystem /tmp\"")
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
		fmt.Fprintln(os.Stderr, "usage: panda ask [--config PATH] [--card PATH] [--authorize] \"<question>\"")
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}

	// The memory injector supplies Hermes memory to the entry model's system
	// prompt and project memory to agent execution context (design §17.2); the
	// tool executor carries out memory tool_calls the model emits.
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	projects := memory.NewProjects(cfg.Storage.ProjectsPath)
	injector := memory.NewInjector(hermes, projects)
	registry := buildToolRegistry(hermes, projects)

	// Optional MCP server: spawn it, import its tool surface into the registry,
	// and tear it down when the ask ends. --mcp is a space-separated command (no
	// quoting), matching the stdio servers PANDA targets (filesystem/git/fetch).
	if *mcpCmd != "" {
		parts := splitCommand(*mcpCmd)
		mcpClient, err := mcp.NewStdioClient(context.Background(), parts[0], parts[1:]...)
		if err != nil {
			fatal("start mcp server", err)
		}
		defer mcpClient.Close()
		if err := registerMCPTools(context.Background(), registry, mcpClient); err != nil {
			fatal("register mcp tools", err)
		}
	}
	daily := memory.NewDaily(hermes.WarmDir())
	skillStore := skills.NewStore(cfg.Storage.SkillsPath)

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

	// 提前建立到 peers 的 P2P 连接并交换能力卡，让入口模型在分类时就能
	// 看到远程节点的能力（否则首次分类看不到远程能力，跨设备路由失败）。
	// 只有提供能力卡（可能执行任务）时才连接；answer/tool_call 不产生副作用。
	var sched *core.Core
	var schedCtx context.Context
	if *cardPath != "" {
		card, err := ledger.LoadCard(*cardPath)
		if err != nil {
			fatal("load capabilities", err)
		}
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
		// ask is a short-lived scheduler: use an ephemeral node id so it never
		// collides with a concurrently running daemon on the same node.
		sched = core.NewCore(db, core.EphemeralNodeID(cfg.Node.Name), card, schedulerTier(cfg.Node.ResourceClass), logger, cfg.Model)
		sched.SetMemoryStores(injector, daily, skillStore)
		sched.SetWorkDir(cfg.Storage.WorkPath)
		sched.SetSharedSecret(cfg.Network.SharedSecret)
		ctx, cancel := context.WithCancel(context.Background())
		schedCtx = ctx
		defer cancel()
		for _, peer := range cfg.Network.Peers {
			if err := sched.DialPeer(ctx, peer); err != nil {
				logger.Warn("peer dial failed", "peer", peer, "err", err)
			}
		}
		if len(cfg.Network.Peers) > 0 {
			waitForPeers(ctx, db, 2*time.Second)
		}
	}

	var devices []ledger.Node
	if devices, err = ledger.Query(db, "online", ""); err != nil {
		devices = nil
	}

	// Hermes memory for the conversation prompt, retrieved by relevance to the
	// user's prompt; a load failure is non-fatal — classification proceeds
	// without memory rather than blocking the ask.
	conversationMemory, merr := injector.Conversation(prompt)
	if merr != nil {
		fmt.Fprintf(os.Stderr, "panda: load memory: %v\n", merr)
	}

	client := entry.NewClient(cfg.Model)
	ctx := context.Background()

	// Multi-turn loop: a tool_call is executed and its result fed back so the
	// model can, e.g., read memory then merge/retry (the Hermes consolidation
	// workflow) before producing a final answer or task.
	turns := []entry.Turn{{Role: "user", Content: prompt}}
	const maxRounds = 6
	for round := 0; round < maxRounds; round++ {
		out, err := entry.ClassifyTurnsWithTools(ctx, client, devices, conversationMemory, turns, registry)
		if err != nil {
			fmt.Fprintln(os.Stderr, "panda: "+err.Error())
			os.Exit(1)
		}

		switch out.Kind {
		case entry.KindAnswer:
			fmt.Println(out.Answer)
			return
		case entry.KindTask:
			if sched == nil {
				fmt.Fprintln(os.Stderr, "panda: task output requires --card (capabilities.yaml)")
				os.Exit(1)
			}
			runAskTask(sched, schedCtx, out.Task, *authorize)
			return
		case entry.KindToolCall:
			result := executeTool(ctx, registry, out.Tool)
			turns = appendToolTurns(turns, out.Tool, result)
		default:
			fmt.Println(out.Answer)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "panda: 达到最大交互轮数（%d）仍未收敛\n", maxRounds)
	os.Exit(1)
}

// runAskTask executes a classified task on the already-connected scheduler
// core and prints the outcome. runAsk establishes the P2P connections before
// classification so the entry model sees remote capabilities; here we only
// route (locally or forward) and await the result.
func runAskTask(c *core.Core, ctx context.Context, spec *entry.TaskSpec, authorized bool) {
	in := toTaskInput(spec)
	in.Authorized = authorized
	task, result, err := c.Submit(ctx, in)
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

// waitForPeers polls until at least one peer has registered online (via its
// hello capability card) or the deadline passes. It lets a slow dial over a
// real network settle before the routing decision runs.
func waitForPeers(ctx context.Context, db *sql.DB, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if nodes, err := ledger.Query(db, "online", ""); err == nil && len(nodes) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
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
		Title:         spec.Title,
		Project:       spec.Project,
		ContextType:   spec.ContextType,
		Intent:        intent.String(),
		SpecJSON:      string(specJSON),
		Requires:      spec.Requires.Abilities,
		PreferredNode: spec.Spec.Scope,
		Complexity:    spec.Complexity,
		Risk:          spec.Risk,
		ResourceJSON:  string(resourceJSON),
	}
}

func readStdin() ([]byte, error) {
	return os.ReadFile("/dev/stdin")
}
