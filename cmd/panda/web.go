package main

// Command web boots the embedded web console with zero ceremony — the
// one-command path for "I just want the panel": defaults to loopback,
// generates an ephemeral token when none is configured, and opens the
// browser already logged in (the URL carries the token; the app consumes it
// once and strips it from the address bar). It shares the handler with the
// REPL's /web and the webui sidecar, reading the same SQLite store the
// daemon writes, and never starts the daemon loop itself.
//
//	panda web                     # http://127.0.0.1:7840, ephemeral token
//	panda web --config config.yaml --card capabilities.yaml

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xenith/openpanda/internal/askengine"
	"github.com/xenith/openpanda/internal/config"
	"github.com/xenith/openpanda/internal/i18n"
	"github.com/xenith/openpanda/internal/log"
	"github.com/xenith/openpanda/internal/memory"
	"github.com/xenith/openpanda/internal/reminders"
	"github.com/xenith/openpanda/internal/sessions"
	"github.com/xenith/openpanda/internal/skills"
	"github.com/xenith/openpanda/webui/panel"
	"github.com/xenith/openpanda/webui/push"
)

func runWeb(args []string) {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", "", "path to capabilities.yaml (enables task execution in /api/ask)")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated)")
	noBrowser := fs.Bool("no-browser", false, "print the URL instead of opening a browser")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()

	// Zero-config on loopback, fail closed elsewhere (same policy as the
	// sidecar and /web).
	addr := cfg.Network.PanelAddr
	if addr == "" {
		addr = "127.0.0.1:7840"
	}
	token := cfg.Network.PanelToken
	if token == "" {
		if !panel.IsLoopbackAddr(addr) {
			fatal("config", fmt.Errorf("network.panel_token is not set and the bind %s is not loopback — set OPENPANDA_PANEL_TOKEN", addr))
		}
		token = panel.NewToken()
		fmt.Println(i18n.T(loc, "repl.web.ephemeral"))
	}

	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	logger := log.From(context.Background())

	// Optional ask engine — same contract as the REPL: without a model
	// endpoint the console still serves queue/projects/nodes; /api/ask
	// reports it is not configured.
	var engine *askengine.Engine
	if cfg.Model.BaseURL != "" {
		engine, err = askengine.New(context.Background(), cfg, askengine.Options{
			CardPath:   *cardPath,
			MCPCommand: *mcpCmd,
			Logger:     logger,
		})
		if err != nil {
			fatal("init ask engine", err)
		}
		defer engine.Close()
	}

	// Web Push (optional): reminder notifications go straight to the browser
	// when a subscription exists.
	var pushSvc *push.Service
	if cfg.Push.Enabled {
		keys, err := push.LoadOrCreateVAPIDKeys(cfg.Push.VAPIDKeyPath, cfg.Push.VAPIDSubject)
		if err != nil {
			logger.Warn("push disabled: load vapid keys failed", "err", err)
		} else {
			pushSvc = push.NewService(keys, push.NewStore(db), logger)
		}
	}

	// Reminders (P1-28): the panel is a long-lived process, so it runs the
	// reminder scanner — Web Push when configured, and the SSE change feed
	// (the reminder fingerprint) refreshes any open console.
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
	go reminderScan.Run(context.Background())

	srv := &http.Server{
		Addr: addr,
		Handler: panel.New(panel.Deps{
			Store:      store,
			Engine:     engine,
			DB:         db,
			Projects:   memory.NewProjects(cfg.Storage.ProjectsPath),
			Sessions:   sessions.NewStore(filepath.Join(filepath.Dir(cfg.Storage.DBPath), "sessions")),
			Worktrees:  openWorktreesBestEffort(cfg.Storage.WorkPath),
			SkillStore: skills.NewStore(cfg.Storage.SkillsPath),
			Reminders:  reminderStore,
			Push:       pushSvc,
			Cfg:        cfg,
			ConfigPath: resolvedConfigPath(*configPath),
			Token:      token,
		}),
	}
	// Bind synchronously so a taken port surfaces as an error, not a
	// silent goroutine death.
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		fatal("listen", err)
	}
	go func() { _ = srv.Serve(ln) }()

	url := panel.AppendToken(panelURL(ln.Addr().String()), token)
	fmt.Println(i18n.Tf(loc, "web.started", "url", url))
	if *noBrowser {
		fmt.Println(i18n.T(loc, "web.nobrowser"))
	} else {
		openBrowser(url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	fmt.Println(i18n.T(loc, "web.stopped"))
	_ = srv.Shutdown(context.Background())
}

// resolvedConfigPath mirrors config.Load's path resolution so the settings
// API persists into the same file the node loaded.
func resolvedConfigPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if env := os.Getenv("OPENPANDA_CONFIG_PATH"); env != "" {
		return env
	}
	return config.DefaultPath
}

// openWorktreesBestEffort returns a Worktrees for the work path, or nil when
// it is not a git repository (sessions then run without isolation).
func openWorktreesBestEffort(workPath string) *sessions.Worktrees {
	wt, err := sessions.OpenWorktrees(workPath)
	if err != nil {
		return nil
	}
	return wt
}
