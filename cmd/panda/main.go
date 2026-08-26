// Command panda is the OpenPanda CLI. With no subcommand it drops into the
// interactive REPL (the operator's seat); `panda daemon` runs the headless
// kernel that registers this node's capabilities and delegates/executes
// tasks over WebSocket. Other subcommands: ask (unified entry model), web
// (embedded console), status/queue/task/approve/reject/cancel/logs (one-shot
// panel), version.
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

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/log"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/updater"
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
		case "daemon", "serve":
			runDaemon(args)
			return
		case "ask":
			runAsk(args)
			return
		case "repl", "chat":
			runRepl(args)
			return
		case "web":
			runWeb(args)
			return
		case "voice":
			runVoice(args)
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
		case "nodes":
			runStatus(args)
			return
		case "queue":
			runQueue(args)
			return
		case "task":
			runTask(args)
			return
		case "plan":
			runPlan(args)
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
		case "card":
			runCard(args)
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
			// A bare unknown word must not silently fall through (P1-25):
			// "panda statsu" (a typo) should neither start the REPL nor a
			// resident daemon — name the fix instead. When the word is one
			// typo away from a real subcommand, say which: that is the whole
			// difference between a dead end and a correction.
			p := palFor(os.Stderr)
			fmt.Fprintf(os.Stderr, "panda: %s %s\n", i18n.T(i18n.Detect(), "cli.unknownSub"), p.Command(sub))
			if s := suggest(sub, subcommandNames()); s != "" {
				fmt.Fprintf(os.Stderr, "  %s\n\n", i18n.Tf(i18n.Detect(), "repl.didyoumean", "cmd", p.Command(s)))
			}
			printUsage(os.Stderr)
			os.Exit(2)
		}
	}
	// No subcommand: the interactive REPL is the product's front door;
	// the kernel is an explicit `panda daemon` away.
	runRepl(args)
}

// subcommandNames lists every accepted subcommand for typo suggestions. It is
// the switch in main() written out once more, deliberately: the switch is the
// dispatcher and must stay a switch (aliases share a case), while this is the
// vocabulary, and a name missing here costs a suggestion, not a command.
func subcommandNames() []string {
	return []string{
		"daemon", "serve", "ask", "repl", "chat", "web", "voice",
		"install", "uninstall", "doctor", "status", "nodes", "queue",
		"task", "plan", "cancel", "approve", "reject", "logs", "skill",
		"reminder", "detect", "card", "init", "metrics", "audit", "session",
		"sessions", "memory", "config", "agents", "project", "version", "help",
	}
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
	// No subcommand: args is flags-only (any bare word would have returned
	// above), so pass them through untouched — the default target (the REPL)
	// parses --config/--card/--mcp itself.
	return "", args
}

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml (default /etc/openpanda/config.yaml)")
	cardPath := fs.String("card", defaultCardPath(), "path to capabilities.yaml (default: discovered — ./capabilities.yaml, next to the resolved config, or /etc/openpanda/capabilities.yaml)")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	effectiveIdentity := cfg.Node.EffectiveIdentity()
	identityLock, err := nodeidentity.Acquire(cfg.Node.Kind, effectiveIdentity)
	if err != nil {
		fatal("start node", err)
	}
	defer identityLock.Release()

	log.Setup(cfg.Log.Level, nil)
	logger := log.From(context.Background())

	if err := os.MkdirAll(filepath.Dir(cfg.Storage.DBPath), 0o755); err != nil {
		fatal("create data dir", err)
	}
	// Storage roots the runtime writes into must exist before any task
	// executes: the sandbox sets a child's cwd to work_path, and a missing
	// dir surfaces as a misleading fork/exec ENOENT blaming the command
	// binary instead of the absent working directory.
	for _, dir := range []string{
		cfg.Storage.ContextPath,
		cfg.Storage.MemoryPath,
		cfg.Storage.ProjectsPath,
		cfg.Storage.SkillsPath,
		cfg.Storage.WorkPath,
		cfg.Storage.ArtifactPath,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal("create storage dir", err)
		}
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
		// A card copied from another machine can declare commands this host
		// does not have (the shipped examples are POSIX-only). Advertising them
		// would win the native plan and fail at exec, so they go before the
		// card is ever registered or sent in a hello.
		if dropped := card.PruneUnavailableNative(); len(dropped) > 0 {
			logger.Warn("native abilities dropped: command not found on this host",
				"ids", strings.Join(dropped, ","))
		}
	}
	card.NodeKind = cfg.Node.Kind
	card.NodeIdentity = effectiveIdentity

	runtimeNodeID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, effectiveIdentity)
	coreNode := core.NewCore(db, runtimeNodeID, card, schedulerTier(cfg.Node.ResourceClass), logger, cfg.Model)
	coreNode.SetRouterPolicy(cfg.Injection, cfg.Routing)
	// Supervision (上级完成度判定): judge agent results against the task's
	// success criteria and re-delegate work that isn't complete. A model-less
	// node skips this — agent tasks finish in one shot as before.
	coreNode.AttachSupervisor(cfg.Model)
	// The work dir travels to adapter subprocesses as their cwd (via the
	// sandbox and the adapter request's CWD field), so it must be absolute —
	// a relative path would resolve against the TASK dir inside the adapter
	// and make python's subprocess.run fail instantly.
	if absWork, err := filepath.Abs(cfg.Storage.WorkPath); err == nil {
		coreNode.SetWorkDir(absWork)
	} else {
		coreNode.SetWorkDir(cfg.Storage.WorkPath)
	}
	coreNode.SetHostStatePaths(hostStatePaths(cfg))
	coreNode.SetSharedSecret(cfg.Network.SharedSecret)
	// The artifact pool is the data plane: a stage's packed output, named by its
	// hash, that a later stage on another node pulls over the bus. Without it a
	// delegated task can only carry a path, which means nothing on the node that
	// receives it.
	coreNode.SetArtifactStore(artifact.NewStore(cfg.Storage.ArtifactPath))
	coreNode.SetLimits(cfg.Network.MaxConnections, cfg.Network.MaxConnectionsPerIP)
	// Execution timeouts (timeouts.*): the agent budget and the task lease. A
	// deep-learning stage runs far longer than a code edit, so both are operator
	// knobs; SetTimeouts also keeps the lease above the agent's hard limit.
	coreNode.SetTimeouts(cfg.Timeouts)

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

	// Self-update notices (v0.0.4 follow-up #5): the headless daemon
	// cannot apply updates itself — there is no console to consent
	// from — but it still checks periodically and logs a notice, so an
	// operator reading the daemon log learns a release is waiting
	// instead of discovering it on the next web visit.
	updateNotice := updater.New(updater.Options{
		Current: version,
		Logger:  logger,
		Idle:    func(ctx context.Context) bool { return coreNode.Idle(ctx) },
		OnAvailable: func(v string) {
			logger.Info("update available",
				"version", v,
				"hint", "open the web console (System → Updates) to review the changelog and apply")
		},
	})
	updateNotice.StartAutoCheck(ctx, 6*time.Hour)

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
		"node_kind", cfg.Node.Kind,
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
// should orient a first-time user, not just enumerate words. Every line goes
// through styleHelpLine, which tints headings and the typeable part of each
// entry (see ui.go); on a non-colour stream it is the same plain text as before.
func printUsage(w *os.File) {
	p := palFor(w)
	line := func(s string) { fmt.Fprintln(w, styleHelpLine(p, s)) }
	line("panda — personal task orchestration across your devices")
	line("")
	line("runtime:")
	line("  (no subcommand)        interactive REPL — the operator's seat (same as `panda repl`)")
	line("  daemon                 run the node kernel headless (registers, listens, delegates)")
	line("  nodes                  show current and known nodes (same data as status)")
	line("  ask <text>             unified entry: classify → answer or execute a task")
	line("                         (--output-format json|stream-json for headless use)")
	line("  repl                   interactive shell (banner, /help pager, Tab completion)")
	line("  web                    start the web console (browser opens, auto-login)")
	line("  voice [--once] [--mute] hands-free entry: wake word → ask → spoken reply")
	line("                         (needs extensions/voice sidecars)")
	line("")
	line("sessions:")
	line("  session list|new|show|rm|ask|diff|merge   chat sessions over git worktrees")
	line("")
	line("tasks:")
	line("  queue [--state s] [--project p] [--watch] the task board (--watch: live view)")
	line("  task <id>                                 show one task + timeline")
	line("  task add --title T [--prompt P] [--priority low|medium|normal|high|critical]")
	line("           [--project p] [--authorize]      enqueue a task (needs --card)")
	line("  task priority <id> <level>                change a task's priority")
	line("  task move <id> <seq>                      reorder the drag-sort queue")
	line("  cancel|approve|reject|logs <id>           one-shot task actions (also")
	line("                                            usable as `panda task <verb>`)")
	line("  plan run <file.yaml> [--dry-run]          start a multi-stage, multi-device")
	line("                                            pipeline (`plan example` to start)")
	line("  plan show <plan-id>                       stage states + artifact wiring")
	line("  project list|create                       project memories")
	line("")
	line("memory:")
	line("  memory list|get|set|rm [name]             user/memory/dreams/topic:<n>/")
	line("                                            project:<n>/daily:<date> files")
	line("")
	line("settings:")
	line("  config model|mcp|limits|routing|injection|approval get|set|test")
	line("                                            view/edit config.yaml (comments kept)")
	line("  agents [test <name>]                      probe installed agent CLIs")
	line("  reminder list|add|rm                      scheduled reminders")
	line("  skill list|approve|reject                 agent skill management")
	line("")
	line("observability:")
	line("  status                                    node identity + capability directory")
	line("  metrics [--csv]                           delegation metrics")
	line("  audit verify [--task id]                  verify the hash chain")
	line("  audit entries [--task id]                 print audit trail rows")
	line("")
	line("setup:")
	line("  install|uninstall                         put panda on PATH / remove it")
	line("  init [--defaults|--non-interactive]      first-run setup (one question; flags = zero prompts)")
	line("  doctor                                    post-install self-check")
	line("  detect                                    scan hardware → capabilities.yaml draft")
	line("  card show|rescan|edit|set                  this node's capability card: read it,")
	line("                                            re-scan hardware + agent CLIs (--write),")
	line("                                            edit it in $EDITOR, or set one field")
	line("  version|help                              version / this help")
	line("")
	line("global flags: --config <path>, --card <path>, --mcp <cmd>, --json")
	line("              (before or after the subcommand; --json = JSON output)")
}
