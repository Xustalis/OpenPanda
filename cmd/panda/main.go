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

	"github.com/xenith/openpanda/internal/config"
	"github.com/xenith/openpanda/internal/core"
	"github.com/xenith/openpanda/internal/ledger"
	"github.com/xenith/openpanda/internal/log"
	"github.com/xenith/openpanda/internal/memory"
	"github.com/xenith/openpanda/internal/skills"
	"github.com/xenith/openpanda/internal/storage"
)

var version = "0.0.1"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("panda %s\n", version)
		return
	}
	sub, args := parseSubcommand(os.Args[1:])
	if sub != "" {
		switch sub {
		case "ask":
			runAsk(args)
			return
		case "repl":
			runRepl(args)
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
		case "metrics":
			runMetrics(args)
			return
		case "audit":
			runAudit(args)
			return
		case "version":
			fmt.Printf("panda %s\n", version)
			return
		default:
			// A bare unknown word must not fall through to runDaemon (P1-25):
			// "panda statsu" (a typo) would otherwise start a resident daemon.
			fmt.Fprintf(os.Stderr, "panda: unknown subcommand %q\n", sub)
			fmt.Fprintln(os.Stderr, "usage: panda [ask|repl|status|queue|task|cancel|approve|reject|logs|skill|metrics|audit|version] — or no subcommand to run the daemon")
			os.Exit(2)
		}
	}
	runDaemon()
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
	coreNode.SetWorkDir(cfg.Storage.WorkPath)
	coreNode.SetHostStatePaths(hostStatePaths(cfg))
	coreNode.SetSharedSecret(cfg.Network.SharedSecret)
	coreNode.SetLimits(cfg.Network.MaxConnections, cfg.Network.MaxConnectionsPerIP)

	// Attach the memory layer (design §17/§8): project-memory injection into
	// agent execution context, daily logging that feeds the Dreaming engine, and
	// skill progressive loading. Load failures degrade to no injection and are
	// logged by the core, not fatal here.
	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	projects := memory.NewProjects(cfg.Storage.ProjectsPath)
	coreNode.SetMemoryStores(
		memory.NewInjector(hermes, projects),
		memory.NewDaily(hermes.WarmDir()),
		skills.NewStore(cfg.Storage.SkillsPath),
	)

	// Web Push lives in the webui sidecar (webui/cmd/panel), not the kernel;
	// the kernel stays headless. See webui/README.md.

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Dreaming (design §17.3): consolidate the daily logs into long-term memory
	// in the background — only while the node is idle, at most once per day.
	dreamSched := memory.NewScheduler(
		memory.NewDreamer(hermes),
		memory.NewDreamDiary(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md")),
		func() bool { return coreNode.Idle(ctx) },
		5*time.Minute,
	)
	dreamSched.OnError = func(err error) { logger.Warn("dreaming sweep", "err", err) }
	go dreamSched.Run(ctx)

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

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "panda: %s: %v\n", step, err)
	os.Exit(1)
}
