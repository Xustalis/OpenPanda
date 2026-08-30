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
	"path/filepath"
	"strconv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/log"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/updater"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
	"github.com/Xustalis/OpenPanda/webui/panel"
	"github.com/Xustalis/OpenPanda/webui/push"
)

func runWeb(args []string) {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", defaultCardPath(), "path to capabilities.yaml (default: discovered; enables task execution in /api/ask)")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated)")
	noBrowser := fs.Bool("no-browser", false, "print the URL instead of opening a browser")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()

	// The shutdown context is created up front (not at the end of the
	// function) so the background reminder scanner and the updater's
	// auto-check are wired to the same ctx that ends the process — they
	// stop with the server instead of holding background.Context.
	ctx, cancel := shutdownContext()
	defer cancel()

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

	// Optional ask engine behind a reloadable holder — same contract as the
	// REPL: without a model endpoint the console still serves
	// queue/projects/nodes; /api/ask reports it is not configured. The
	// holder lets the settings page build the engine at runtime once a
	// model is saved (zero-config start → first save), no restart needed.
	engines, err := panel.NewEngineHolder(cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
		Logger:     logger,
	})
	if err != nil {
		fatal("init ask engine", err)
	}
	defer engines.Close()

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
	guard.Go(logger, "web: reminder scanner", cancel, func() { reminderScan.Run(ctx) })

	// Self-update: check the release channel in the background while the panel
	// runs, so a newer CLI is discovered during normal use rather than only on
	// demand. Apply gates on task-queue idle so an update never interrupts work.
	updateMgr := updater.New(updater.Options{
		Current: versionpkg.Version,
		Logger:  logger,
		Idle:    store.Idle,
	})
	// StartAutoCheck spawns its loop internally; it is wired to ctx here so it
	// stops with the process, but a panic inside it is not guard-wrapped
	// (internal/updater is outside the cmd/panda wiring scope).
	updateMgr.StartAutoCheck(ctx, 0)

	srv := &http.Server{
		Addr: addr,
		Handler: panel.New(panel.Deps{
			Store:        store,
			EngineHolder: engines,
			DB:           db,
			Projects:     memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg)),
			Sessions:     sessions.NewStore(filepath.Join(filepath.Dir(cfg.Storage.DBPath), "sessions")),
			Worktrees:    openWorktreesBestEffort(cfg.Storage.WorkPath),
			SkillStore:   skills.NewStore(cfg.Storage.SkillsPath),
			Reminders:    reminderStore,
			Push:         pushSvc,
			Cfg:          cfg,
			ConfigPath:   resolvedConfigPath(*configPath),
			CardPath:     *cardPath,
			Token:        token,
			Updater:      updateMgr,
		}),
	}
	// Bind synchronously so a taken port surfaces as an error, not a
	// silent goroutine death. A taken port falls forward to a nearby one
	// (listenPanel) instead of failing — the user asked for the console,
	// not an error message.
	ln, bound, err := listenPanel(addr)
	if err != nil {
		fatal("listen", err)
	}
	if bound != addr {
		fmt.Println(i18n.Tf(loc, "web.portfallback", "orig", addr, "actual", bound))
	}
	go func() { _ = srv.Serve(ln) }()

	url := panelURL(ln.Addr().String())
	if *noBrowser {
		// Manual mode only: the user opens the browser themselves, so the
		// printed URL must carry the token. In the normal path the browser
		// is opened for them, already authenticated — the token never
		// needs to be seen or remembered.
		fmt.Println(i18n.Tf(loc, "web.started", "url", panel.AppendToken(url, token)))
		fmt.Println(i18n.T(loc, "web.nobrowser"))
	} else {
		fmt.Println(i18n.Tf(loc, "web.started", "url", url))
		openBrowser(panel.AppendToken(url, token))
	}

	<-ctx.Done()
	fmt.Println(i18n.T(loc, "web.stopped"))
	_ = srv.Shutdown(context.Background())
}

// listenPanel binds the panel address, falling forward through a few nearby
// ports when the configured one is taken (typically another `panda web`
// already running). Failing with "address already in use" leaves the user
// with nothing actionable; serving on the next port with a notice keeps the
// one-command promise. Returns the listener and the address actually bound.
func listenPanel(addr string) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, addr, nil
	}
	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, "", err
	}
	port, convErr := strconv.Atoi(portStr)
	if convErr != nil {
		return nil, "", err
	}
	for i := 1; i <= 5; i++ {
		alt := net.JoinHostPort(host, strconv.Itoa(port+i))
		altLn, altErr := net.Listen("tcp", alt)
		if altErr == nil {
			return altLn, alt, nil
		}
	}
	return nil, "", err
}

// resolvedConfigPath mirrors config.Load's path resolution so the settings
// API persists into the same file the node loaded.
func resolvedConfigPath(flagPath string) string {
	return config.ResolvePath(flagPath)
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
