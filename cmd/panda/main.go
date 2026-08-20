// Command panda is the PANDA core daemon. It registers this node's
// capabilities and, once peers connect, delegates/executes tasks over
// WebSocket. Subcommands: ask (unified entry model), repl (interactive shell
// over the same store, with /web to boot the embedded console), status/queue/
// task/approve/reject/cancel/logs (one-shot panel), version. With no
// subcommand it runs the daemon (headless kernel — the web panel is a
// separate webui/ sidecar).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/log"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
)

var version = versionpkg.Version

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("panda %s\n", version)
		return
	}
	args := stripJSONFlag(os.Args[1:])
	sub, args := parseSubcommand(args)
	if sub != "" {
		switch sub {
		case "ask":
			runAsk(args)
			return
		case "repl":
			runRepl(args)
			return
		case "web":
			runWeb(args)
			return
		case "install":
			runInstall(args)
			return
		case "uninstall":
			runUninstall(args)
			return
		case "doctor":
			runDoctor(args)
			return
		case "status":
			runStatus(args)
			return
		case "queue":
			runQueue(args)
			return
		case "task":
			runTask(args)
			return
		case "cancel":
			runCancel(args)
			return
		case "approve":
			runApprove(args)
			return
		case "reject":
			runReject(args)
			return
		case "logs":
			runLogs(args)
			return
		case "skill":
			runSkill(args)
			return
		case "reminder":
			runReminder(args)
			return
		case "detect":
			runDetect(args)
			return
		case "init":
			runInit(args)
			return
		case "metrics":
			runMetrics(args)
			return
		case "audit":
			runAudit(args)
			return
		case "session", "sessions":
			runSession(args)
			return
		case "memory":
			runMemory(args)
			return
		case "config":
			runConfig(args)
			return
		case "agents":
			runAgents(args)
			return
		case "project":
			runProject(args)
			return
		case "version":
			fmt.Printf("panda %s\n", version)
			return
		case "help", "-h", "--help":
			printUsage(os.Stdout)
			return
		default:
			// A bare unknown word must not fall through to runDaemon (P1-25):
			// "panda statsu" (a typo) would otherwise start a resident daemon.
			fmt.Fprintf(os.Stderr, "panda: unknown subcommand %q\n", sub)
			printUsage(os.Stderr)
			os.Exit(2)
		}
	}
	runDaemon()
}

// stripJSONFlag removes every --json occurrence from args (it may sit before
// or after the subcommand) and sets jsonOutput so panel-style commands emit
// their JSON wire form.
func stripJSONFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// parseSubcommand scans args, skips leading global flags and their values,
// and returns the first non-flag argument (the subcommand) plus everything
// after it. Global flags like --config may appear before or after the
// subcommand; this lets users write `panda --config x.yaml status` as well
// as `panda status --config x.yaml`.
func parseSubcommand(args []string) (string, []string) {
	valueFlags := map[string]bool{"--config": true, "--card": true, "--mcp": true}
	var global []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if valueFlags[a] && i+1 < len(args) {
				global = append(global, a, args[i+1])
				i++ // skip the flag's value
			}
			continue
		}
		return a, append(global, args[i+1:]...)
	}
	return "", nil
}

func runDaemon() {
	var (
		configPath = flag.String("config", "", "path to config.yaml (default /etc/openpanda/config.yaml)")
		cardPath   = flag.String("card", "", "path to capabilities.yaml")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}

	log.Setup(cfg.Log.Level, nil)
	logger := log.From(context.Background())

	if err := os.MkdirAll(filepath.Dir(cfg.Storage.DBPath), 0o755); err != nil {
		fatal("create data dir", err)
	}

	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()

	if err := storage.Migrate(db); err != nil {
		fatal("migrate database", err)
	}

	// Load the capability card if configured. A node without a card is
	// still a valid participant for heartbeat testing, but Phase 0
	// requires one to route work.
	var card ledger.Card
	if *cardPath != "" {
		card, err = ledger.LoadCard(*cardPath)
		if err != nil {
			fatal("load capabilities", err)
		}
	}

	coreNode := core.NewCore(db, core.NodeID(cfg.Node.Name), card, schedulerTier(cfg.Node.ResourceClass), logger, cfg.Model)
	coreNode.SetRouterPolicy(cfg.Injection, cfg.Routing)
	coreNode.SetWorkDir(cfg.Storage.WorkPath)
	coreNode.SetHostStatePaths(hostStatePaths(cfg))
	coreNode.SetSharedSecret(cfg.Network.SharedSecret)
	coreNode.SetLimits(cfg.Network.MaxConnections, cfg.Network.MaxConnectionsPerIP)

	// Attach the memory layer (design §17/§8): daily logging that feeds the
	// Dreaming engine, and skill progressive loading. Project memory is no
	// longer injected into agent prompts (A1); the injector instead supplies
	// the A3 memory-file manifest for selective loading. Character caps come
	// from config memory.limits. Load failures degrade gracefully and are
	// logged by the core, not fatal here.
	limits := memoryLimits(cfg)
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, limits)
	projects := memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, limits)
	daily := memory.NewDaily(hermes.WarmDir())
	coreNode.SetMemoryStores(
		memory.NewInjector(hermes, projects),
		daily,
		skills.NewStore(cfg.Storage.SkillsPath),
	)

	// Web Push lives in the webui sidecar (webui/cmd/panel), not the kernel;
	// the kernel stays headless. See webui/README.md.

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Queue scheduler (panel queue redesign): adopt queued-and-scheduled tasks
	// in policy order (drag seq → priority → FIFO) when resources allow. Runs
	// alongside the daemon so enqueued tasks execute even without the panel.
	coreNode.StartQueueScheduler(ctx)

	// Dreaming (design §17.3): consolidate the daily logs into long-term memory
	// in the background — only while the node is idle, at most once per day.
	// The same tick also enforces the daily-log retention windows (A4: the
	// production wiring of daily.Prune, once per day, independently of the
	// dream cadence). Promotions land in the audit log (EvMemoryPromotion) so
	// the Web console can show — and correct or delete — what was memorized.
	dreamer := memory.NewDreamer(hermes)
	audit := security.NewAudit(db)
	dreamer.OnPromotion = func(entry string, viaWhitelist bool) {
		channel := "threshold"
		if viaWhitelist {
			channel = "whitelist"
		}
		if err := audit.Record(ctx, security.Entry{
			Who:    cfg.Node.Name,
			What:   core.EvMemoryPromotion,
			Target: "MEMORY.md",
			Result: "ok",
			Detail: channel + ": " + entry,
		}); err != nil {
			logger.Warn("record memory promotion", "err", err)
		}
	}
	dreamSched := memory.NewScheduler(
		dreamer,
		memory.NewDreamDiary(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md")),
		func() bool { return coreNode.Idle(ctx) },
		5*time.Minute,
	).WithDaily(daily)
	dreamSched.OnError = func(err error) { logger.Warn("dreaming sweep", "err", err) }
	go dreamSched.Run(ctx)

	// Reminders (design P1-28): claim due reminders in the background and
	// log them. The web panel runs its own scanner with Web Push + SSE
	// delivery; ClaimDue's atomic claim keeps the two from double-firing.
	reminderScan := reminders.NewScanner(
		reminders.NewStore(db),
		15*time.Second,
		func(r reminders.Reminder) {
			logger.Info("reminder due", "id", r.ID, "message", r.Message,
				"due", time.Unix(r.DueAt, 0).Format(time.RFC3339))
		},
		logger,
	)
	go reminderScan.Run(ctx)

	if err := coreNode.Register(ctx); err != nil {
		fatal("register node", err)
	}
	go coreNode.RunHeartbeat(ctx)
	go coreNode.RunMonitor(ctx)

	if n, err := coreNode.Recover(ctx); err != nil {
		logger.Warn("task recovery failed", "err", err)
	} else if n > 0 {
		logger.Info("recovered tasks from previous run", "count", n)
	}

	for _, peer := range cfg.Network.Peers {
		go func(p string) {
			backoff := 1 * time.Second
			for {
				err := coreNode.MaintainPeer(ctx, p)
				if err != nil {
					// Dial or hello failed; back off exponentially so we do
					// not hot-loop a permanently offline peer.
					logger.Warn("peer dial failed", "peer", p, "err", err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
					backoff = min(backoff*2, 30*time.Second)
					continue
				}
				// The connection was established and later dropped; reset the
				// backoff and reconnect promptly.
				backoff = 1 * time.Second
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
			}
		}(peer)
	}

	logger.Info("panda core started",
		"version", version,
		"node", cfg.Node.Name,
		"resource_class", cfg.Node.ResourceClass,
		"card", *cardPath,
		"listen", cfg.Network.ListenAddr,
		"db", cfg.Storage.DBPath,
	)

	// The kernel runs headless: the legacy PWA panel is an optional sidecar
	// (webui/cmd/panel), never mounted here. See webui/README.md.

	serveErr := make(chan error, 1)
	// Fail-closed transport auth (design §16 / P0-1): without a shared secret no
	// peer can authenticate, so the WebSocket listener is not started at all —
	// the node runs local-only rather than accepting unauthenticated peers.
	if cfg.Network.SharedSecret == "" {
		logger.Warn("websocket disabled: network.shared_secret is not set (refusing to accept unauthenticated peers)")
	} else {
		go func() { serveErr <- coreNode.Listen(ctx, cfg.Network.ListenAddr) }()
	}

	select {
	case <-ctx.Done():
		logger.Info("panda core shutting down")
		coreNode.Shutdown(ctx)
	case err := <-serveErr:
		if err != nil {
			fatal("websocket server", err)
		}
	}
}

// schedulerTier maps a resource class to the DCPS-style scheduler tier used
// in priority scoring. Root scheduler = 10, sub-scheduler = 5, worker = 1.
func schedulerTier(resourceClass string) int {
	switch resourceClass {
	case "Full":
		return 10
	case "Standard":
		return 5
	default:
		return 1
	}
}

// hostStatePaths returns the node's own bookkeeping paths — its SQLite/memory
// trees and the agent CLI's own config dir — so scope-drift detection ignores
// the host's side-effect writes rather than flagging them as agent drift.
func hostStatePaths(cfg *config.Config) []string {
	return []string{
		filepath.Dir(cfg.Storage.DBPath), // data/: openpanda.db + -wal/-shm + context/
		cfg.Storage.MemoryPath,
		cfg.Storage.ProjectsPath,
		cfg.Storage.SkillsPath,
		filepath.Join(cfg.Storage.WorkPath, ".claude"), // the agent CLI's own project config
	}
}

// memoryLimits maps the configured memory caps (config memory.limits) into
// the memory package's Limits, so the daemon, the REPL and `panda web` all
// enforce the same values; zero fields fall back inside the memory package.
func memoryLimits(cfg *config.Config) memory.Limits {
	return memory.Limits{
		User:    cfg.Memory.Limits.User,
		Memory:  cfg.Memory.Limits.Memory,
		Project: cfg.Memory.Limits.Project,
	}
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "panda: %s: %v\n", step, err)
	os.Exit(1)
}

// printUsage lists the subcommands as a grouped command tree — `panda help`
// should orient a first-time user, not just enumerate words.
func printUsage(w *os.File) {
	fmt.Fprintln(w, "panda — personal task orchestration across your devices")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "runtime:")
	fmt.Fprintln(w, "  (no subcommand)        run the node daemon (registers, listens, delegates)")
	fmt.Fprintln(w, "  ask <text>             unified entry: classify → answer or execute a task")
	fmt.Fprintln(w, "                         (--output-format json|stream-json for headless use)")
	fmt.Fprintln(w, "  repl                   interactive shell (banner, /help pager, Tab completion)")
	fmt.Fprintln(w, "  web                    start the web console (browser opens, auto-login)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "sessions:")
	fmt.Fprintln(w, "  session list|new|show|rm|ask|diff|merge   chat sessions over git worktrees")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "tasks:")
	fmt.Fprintln(w, "  queue [--state s] [--project p]           the task board")
	fmt.Fprintln(w, "  task <id>                                 show one task + timeline")
	fmt.Fprintln(w, "  task add --title T [--prompt P] [--priority low|medium|normal|high|critical]")
	fmt.Fprintln(w, "           [--project p] [--authorize]      enqueue a task (needs --card)")
	fmt.Fprintln(w, "  task priority <id> <level>                change a task's priority")
	fmt.Fprintln(w, "  task move <id> <seq>                      reorder the drag-sort queue")
	fmt.Fprintln(w, "  cancel|approve|reject|logs <id>           one-shot task actions (also")
	fmt.Fprintln(w, "                                            usable as `panda task <verb>`)")
	fmt.Fprintln(w, "  project list|create                       project memories")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "memory:")
	fmt.Fprintln(w, "  memory list|get|set|rm [name]             user/memory/dreams/topic:<n>/")
	fmt.Fprintln(w, "                                            project:<n>/daily:<date> files")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "settings:")
	fmt.Fprintln(w, "  config model|mcp|limits|routing|injection|approval get|set|test")
	fmt.Fprintln(w, "                                            view/edit config.yaml (comments kept)")
	fmt.Fprintln(w, "  agents [test <name>]                      probe installed agent CLIs")
	fmt.Fprintln(w, "  reminder list|add|rm                      scheduled reminders")
	fmt.Fprintln(w, "  skill list|approve|reject                 agent skill management")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "observability:")
	fmt.Fprintln(w, "  status                                    node identity + capability directory")
	fmt.Fprintln(w, "  metrics [--csv]                           delegation metrics")
	fmt.Fprintln(w, "  audit verify [--task id]                  verify the hash chain")
	fmt.Fprintln(w, "  audit entries [--task id]                 print audit trail rows")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "setup:")
	fmt.Fprintln(w, "  install|uninstall                         put panda on PATH / remove it")
	fmt.Fprintln(w, "  init                                      interactive first-run setup")
	fmt.Fprintln(w, "  doctor                                    post-install self-check")
	fmt.Fprintln(w, "  detect                                    scan hardware → capabilities.yaml draft")
	fmt.Fprintln(w, "  version|help                              version / this help")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "global flags: --config <path>, --card <path>, --mcp <cmd>, --json")
	fmt.Fprintln(w, "              (before or after the subcommand; --json = JSON output)")
}
