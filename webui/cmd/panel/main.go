// Command panel runs the web control panel as a sidecar, reading the same
// SQLite store the kernel daemon writes (see webui/README.md). Besides the
// read-only queue it serves the panel's write paths: POST /api/ask (the
// unified entry, shared with `panda ask`), project create, cancel, and node
// listing. --card wires the ask engine's task execution (same flag as
// `panda ask`); without it /api/ask answers questions but refuses tasks.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xenith/openpanda/internal/askengine"
	"github.com/xenith/openpanda/internal/config"
	"github.com/xenith/openpanda/internal/core"
	"github.com/xenith/openpanda/internal/log"
	"github.com/xenith/openpanda/internal/memory"
	"github.com/xenith/openpanda/internal/reminders"
	"github.com/xenith/openpanda/internal/storage"
	"github.com/xenith/openpanda/webui/panel"
	"github.com/xenith/openpanda/webui/push"
)

func main() {
	var (
		configPath = flag.String("config", "", "path to config.yaml (default /etc/openpanda/config.yaml)")
		staticDir  = flag.String("static", "", "serve a directory instead of the embedded web app (dev override)")
		cardPath   = flag.String("card", "", "path to capabilities.yaml (enables task execution in /api/ask)")
		mcpCommand = flag.String("mcp", "", "space-separated stdio MCP server command whose tools /api/ask may call")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	// Zero-config convenience: default to loopback, and generate an
	// ephemeral token there when none is configured (printed below). A
	// non-loopback bind still fails closed — an unauthenticated panel on
	// the network is never acceptable.
	if cfg.Network.PanelAddr == "" {
		cfg.Network.PanelAddr = "127.0.0.1:7840"
	}
	if cfg.Network.PanelToken == "" {
		if !panel.IsLoopbackAddr(cfg.Network.PanelAddr) {
			fatal("config", fmt.Errorf("network.panel_token is not set and the bind %s is not loopback — set OPENPANDA_PANEL_TOKEN (refusing to serve /api/* unauthenticated)", cfg.Network.PanelAddr))
		}
		cfg.Network.PanelToken = panel.NewToken()
		fmt.Fprintln(os.Stderr, "panda-webui: no panel_token configured — generated an ephemeral one for this session")
	}

	log.Setup(cfg.Log.Level, nil)
	logger := log.From(context.Background())

	// The panel speaks plain HTTP: a non-loopback bind exposes the Bearer
	// token and task contents to anyone on the path (P1-24). Warn loudly;
	// the fix is a TLS reverse proxy, not a wider bind.
	if !panel.IsLoopbackAddr(cfg.Network.PanelAddr) {
		logger.Warn("panel bound to a non-loopback address over plain HTTP — bearer token and task contents are sniffable; use 127.0.0.1 or a TLS reverse proxy",
			"addr", cfg.Network.PanelAddr)
	}

	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		fatal("migrate database", err)
	}

	store := core.NewTaskStore(db, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The ask engine powers POST /api/ask — the same unified entry `panda ask`
	// runs (classification, memory/MCP tools, task submission). It needs a
	// model endpoint; --card additionally opts in to task execution exactly
	// like `panda ask --card`. Without an endpoint the panel still serves the
	// queue/projects/nodes and /api/ask reports it is not configured.
	var engine *askengine.Engine
	if cfg.Model.BaseURL != "" {
		engine, err = askengine.New(ctx, cfg, askengine.Options{
			CardPath:   *cardPath,
			MCPCommand: *mcpCommand,
			Logger:     logger,
		})
		if err != nil {
			fatal("init ask engine", err)
		}
		defer engine.Close()
	}

	var pushSvc *push.Service
	if cfg.Push.Enabled {
		keys, err := push.LoadOrCreateVAPIDKeys(cfg.Push.VAPIDKeyPath, cfg.Push.VAPIDSubject)
		if err != nil {
			fatal("load vapid keys", err)
		}
		pushSvc = push.NewService(keys, push.NewStore(db), logger)
		store.SetOnReview(func(t core.Task) {
			nctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := pushSvc.Notify(nctx, push.Notification{
				Title: "OpenPanda · 任务需要审批",
				Body:  t.Title,
				ID:    t.TaskID,
				Icon:  "/icons/icon-192.png",
				Badge: "/icons/badge-72.png",
			}); err != nil {
				logger.Warn("notify review", "task", t.TaskID, "err", err)
			}
		})
	}

	// Reminders (P1-28): the sidecar is long-lived, so it runs the scanner —
	// Web Push when configured, SSE change signal to open consoles otherwise.
	reminderStore := reminders.NewStore(db)
	reminderScan := reminders.NewScanner(reminderStore, 15*time.Second, func(r reminders.Reminder) {
		if pushSvc != nil {
			nctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := pushSvc.Notify(nctx, push.Notification{
				Title: "OpenPanda · Reminder",
				Body:  r.Message,
				ID:    fmt.Sprintf("reminder-%d", r.ID),
				Icon:  "/icons/icon-192.png",
				Badge: "/icons/badge-72.png",
			}); err != nil {
				logger.Warn("reminder push", "err", err)
			}
		}
	}, logger)
	go reminderScan.Run(ctx)

	srv := &http.Server{
		Addr: cfg.Network.PanelAddr,
		Handler: panel.New(panel.Deps{
			Store:     store,
			Engine:    engine,
			DB:        db,
			Projects:  memory.NewProjects(cfg.Storage.ProjectsPath),
			Push:      pushSvc,
			Reminders: reminderStore,
			Cfg:       cfg,
			ConfigPath: configPathOrDefault(*configPath),
			StaticDir: *staticDir,
			Token:     cfg.Network.PanelToken,
		}),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	logger.Info("openpanda webui sidecar listening", "addr", cfg.Network.PanelAddr, "static", *staticDir)
	// The ready URL carries the token so the console logs in without a
	// manual paste (the app strips it from the address bar on load).
	fmt.Println("open:", panel.AppendToken(panelURL(cfg.Network.PanelAddr), cfg.Network.PanelToken))

	select {
	case <-ctx.Done():
		logger.Info("openpanda webui sidecar shutting down")
		_ = srv.Shutdown(context.Background())
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fatal("panel server", err)
		}
	}
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "panda-webui: %s: %v\n", step, err)
	os.Exit(1)
}

// configPathOrDefault mirrors config.Load's path resolution so the settings
// APIs persist into the file the sidecar loaded.
func configPathOrDefault(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if env := os.Getenv("OPENPANDA_CONFIG_PATH"); env != "" {
		return env
	}
	return config.DefaultPath
}

// panelURL turns a listen address into the URL a browser can open (a bare
// or wildcard host becomes localhost).
func panelURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}
