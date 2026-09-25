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
	"io"
	stdlog "log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/log"
	"github.com/Xustalis/OpenPanda/internal/memory"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/updater"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
	"github.com/Xustalis/OpenPanda/webui/panel"
	"github.com/Xustalis/OpenPanda/webui/push"
)

func runWeb(args []string) {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", cardFlagDefault(), "path to capabilities.yaml (default: discovered; enables task execution in /api/ask)")
	mcpCmd := fs.String("mcp", cliMCP, "MCP server command (space-separated)")
	noBrowser := fs.Bool("no-browser", false, "print the URL instead of opening a browser")
	daemon := fs.Bool("daemon", false, "run web console quietly in the background")
	fs.BoolVar(daemon, "d", false, "alias for -daemon")
	lan := fs.Bool("lan", false, "bind all interfaces so the console is reachable from the LAN (auto-generates a token when none is configured)")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()

	// Ensure background log directory and file
	logDir := filepath.Join(cliStateDir(), "logs")
	_ = os.MkdirAll(logDir, 0o755)
	logFile := filepath.Join(logDir, "web.log")

	// Daemon mode: spawn detached background process and return immediately
	if *daemon {
		exe, err := os.Executable()
		if err != nil {
			fatal("daemon start", err)
		}
		var childArgs []string
		for _, a := range args {
			if a == "-d" || a == "--daemon" || a == "-daemon" {
				continue
			}
			childArgs = append(childArgs, a)
		}
		subArgs := append([]string{"web"}, childArgs...)
		lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fatal("open web log", err)
		}
		cmd := exec.Command(exe, subArgs...)
		cmd.Stdout = lf
		cmd.Stderr = lf
		cmd.Stdin = nil
		if err := cmd.Start(); err != nil {
			fatal("start background web", err)
		}
		targetAddr := cfg.Network.PanelAddr
		if targetAddr == "" {
			targetAddr = "127.0.0.1:7840"
		}
		if *lan {
			_, port, err := net.SplitHostPort(targetAddr)
			if err != nil || port == "" {
				port = "7840"
			}
			targetAddr = net.JoinHostPort("0.0.0.0", port)
		}
		fmt.Println(i18n.Tf(loc, "web.background",
			"pid", strconv.Itoa(cmd.Process.Pid),
			"url", panelURL(targetAddr),
			"log", logFile))
		return
	}

	// The shutdown context is created up front (not at the end of the
	// function) so the background reminder scanner and the updater's
	// auto-check are wired to the same ctx that ends the process — they
	// stop with the server instead of holding background.Context.
	ctx, cancel := shutdownContext()
	defer cancel()

	// Zero-config on loopback. --lan (or a non-loopback panel_addr) widens the
	// bind to every interface; an unconfigured token then gets an ephemeral
	// one — the API never runs open — and the LAN URLs carry it so the link is
	// usable from another device on the network. Plain HTTP: prefer a stable
	// network.panel_token (or a TLS reverse proxy) for anything long-lived.
	addr := cfg.Network.PanelAddr
	if addr == "" {
		addr = "127.0.0.1:7840"
	}
	if *lan {
		_, port, err := net.SplitHostPort(addr)
		if err != nil || port == "" {
			port = "7840"
		}
		addr = net.JoinHostPort("0.0.0.0", port)
	}
	token := cfg.Network.PanelToken
	ephemeral := false
	if token == "" {
		token = panel.NewToken()
		ephemeral = true
		if !panel.IsLoopbackAddr(addr) {
			fmt.Println(i18n.T(loc, "web.lan.ephemeral"))
		} else {
			fmt.Println(i18n.T(loc, "repl.web.ephemeral"))
		}
	}

	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	// Redirect logger to file to prevent console spam
	var logWriter io.Writer = io.Discard
	if lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		defer lf.Close()
		logWriter = lf
	}
	log.Setup(cfg.Log.Level, logWriter)
	logger := slog.New(slog.NewJSONHandler(logWriter, &slog.HandlerOptions{Level: log.ParseLevel(cfg.Log.Level)}))

	// Optional ask engine behind a reloadable holder — same contract as the
	// REPL: without a model endpoint the console still serves
	// queue/projects/nodes; /api/ask reports it is not configured. The
	// holder lets the settings page build the engine at runtime once a
	// model is saved (zero-config start → first save), no restart needed.
	// QueueTasks is not optional for the embedded console: without it the
	// board's "new task" and every chat-classified task land in queued with
	// no scheduler to claim them — a silent stall unless a daemon happens to
	// run alongside. The standalone sidecar sets the same flag.
	engines, err := panel.NewEngineHolder(cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
		QueueTasks: true,
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

	// A task that parks in review is waiting on the user, so the console must
	// say so rather than let it sit in the queue unnoticed: the open console
	// picks it up through the SSE change feed, and Web Push reaches a closed
	// one. The hook is installed on the engine (not on this process's separate
	// TaskStore handle), because the review transition happens inside the
	// engine's scheduler core — a callback on any other handle never fires.
	// It only announces the approval; the decision stays with the user.
	engines.SetOnReview(func(t core.Task) {
		logger.Info("task waiting for user approval", "task", t.TaskID, "title", t.Title)
		if pushSvc == nil {
			return
		}
		nctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := pushSvc.Notify(nctx, push.Notification{
			Title: "OpenPanda · " + i18n.T(loc, "web.push.reviewTitle"),
			Body:  t.Title,
			ID:    t.TaskID,
			Icon:  "/icons/icon-192.png",
			Badge: "/icons/badge-72.png",
		}); err != nil {
			logger.Warn("notify review", "task", t.TaskID, "err", err)
		}
	})

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
		Current:         versionpkg.Version,
		CurrentCodename: versionpkg.Codename,
		Logger:          logger,
		Idle:            store.Idle,
		SchemaFloor:     schemaFloorFunc(db),
	})
	// StartAutoCheck spawns its loop internally; it is wired to ctx here so it
	// stops with the process, but a panic inside it is not guard-wrapped
	// (internal/updater is outside the cmd/panda wiring scope).
	updateMgr.StartAutoCheck(ctx, 0)

	srv := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Handler: panel.New(panel.Deps{
			Store:        store,
			EngineHolder: engines,
			DB:           db,
			Projects:     memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg)),
			ProjectStore: projectstore.NewStore(db),
			Sessions:     sessions.NewStore(filepath.Join(filepath.Dir(cfg.Storage.DBPath), "sessions")),
			Worktrees:    openWorktreesBestEffort(cfg.Storage.WorkPath),
			SkillStore: func() *skills.Store {
				st := skills.NewStore(cfg.Storage.SkillsPath)
				_ = st.EnsureBuiltins()
				return st
			}(),
			Reminders:  reminderStore,
			Push:       pushSvc,
			Cfg:        cfg,
			ConfigPath: resolvedConfigPath(*configPath),
			CardPath:   *cardPath,
			Token:      token,
			Updater:    updateMgr,
		}),
		ErrorLog: stdlog.New(logWriter, "", 0),
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
	for _, lanURL := range panel.LANURLs(ln.Addr().String()) {
		fmt.Println(i18n.Tf(loc, "web.lan.url", "url", panel.AppendToken(lanURL, token)))
	}
	if ephemeral && !panel.IsLoopbackAddr(addr) {
		fmt.Println(i18n.T(loc, "web.lan.hint"))
	}

	<-ctx.Done()
	fmt.Println(i18n.T(loc, "web.stopped"))
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
	_ = storage.Checkpoint(sctx, db, "TRUNCATE")
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
