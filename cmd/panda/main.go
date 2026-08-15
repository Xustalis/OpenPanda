// Command panda is the PANDA core daemon. It registers this node's
// capabilities and, once peers connect, delegates/executes tasks over
// WebSocket. Subcommands: ask (unified entry model), status/queue/task/
// cancel/logs (CLI panel), version. With no subcommand it runs the daemon.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/core"
	"github.com/xenith/panda/internal/ledger"
	"github.com/xenith/panda/internal/log"
	"github.com/xenith/panda/internal/memory"
	"github.com/xenith/panda/internal/panel"
	"github.com/xenith/panda/internal/push"
	"github.com/xenith/panda/internal/skills"
	"github.com/xenith/panda/internal/storage"
)

var version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "ask":
			runAsk(os.Args[2:])
			return
		case "status":
			runStatus(os.Args[2:])
			return
		case "queue":
			runQueue(os.Args[2:])
			return
		case "task":
			runTask(os.Args[2:])
			return
		case "cancel":
			runCancel(os.Args[2:])
			return
		case "logs":
			runLogs(os.Args[2:])
			return
		case "skill":
			runSkill(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Printf("panda %s\n", version)
			return
		}
	}
	runDaemon()
}

func runDaemon() {
	var (
		configPath = flag.String("config", "", "path to config.yaml (default /etc/panda/config.yaml)")
		cardPath   = flag.String("card", "", "path to capabilities.yaml")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}

	log.Setup(cfg.Log.Level, nil)
	logger := log.From(context.Background())

	if err := os.MkdirAll(dirOf(cfg.Storage.DBPath), 0o755); err != nil {
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
	coreNode.SetSharedSecret(cfg.Network.SharedSecret)

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

	// Web Push (design P3-26): when enabled, a task entering review fires a push
	// to every subscribed device so the operator can approve/reject from a phone.
	// Disabled by default; the endpoints and sender are only wired when on.
	var pushSvc *push.Service
	if cfg.Push.Enabled {
		keys, err := push.LoadOrCreateVAPIDKeys(cfg.Push.VAPIDKeyPath, cfg.Push.VAPIDSubject)
		if err != nil {
			fatal("load vapid keys", err)
		}
		pushSvc = push.NewService(keys, push.NewStore(db), logger)
		coreNode.TaskStore().SetOnReview(func(t core.Task) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := pushSvc.Notify(ctx, push.Notification{
				Title: "PANDA · 任务需要审批",
				Body:  t.Title,
				ID:    t.TaskID,
				Icon:  "/icons/icon-192.png",
				Badge: "/icons/badge-72.png",
			}); err != nil {
				logger.Warn("notify review", "task", t.TaskID, "err", err)
			}
		})
	}

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

	// PWA control panel (design §11 / P3-25): an HTTP server serving the static
	// web app plus the task queue/approval API. Optional; empty panel_addr
	// disables it. A panel without a token is refused (not silently left open):
	// the /api/* approval routes must never be reachable unauthenticated.
	if cfg.Network.PanelAddr != "" {
		if cfg.Network.PanelToken == "" {
			logger.Warn("panel disabled: network.panel_token is not set (refusing to serve /api/* unauthenticated)")
		} else {
			panelSrv := &http.Server{
				Addr:    cfg.Network.PanelAddr,
				Handler: panel.New(coreNode.TaskStore(), "web/pwa", pushSvc, cfg.Network.PanelToken),
			}
			go func() {
				logger.Info("panel listening", "addr", cfg.Network.PanelAddr)
				if err := panelSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					logger.Warn("panel server", "err", err)
				}
			}()
		}
	}

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

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "panda: %s: %v\n", step, err)
	os.Exit(1)
}
