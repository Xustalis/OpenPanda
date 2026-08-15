// Command panel runs the legacy PWA control panel as an optional sidecar,
// reading the same SQLite store the kernel daemon writes. The kernel daemon no
// longer mounts the web UI; start this separately if you still want the browser
// console (see webui/README.md). Kept frozen — no further optimization planned.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/core"
	"github.com/xenith/panda/internal/log"
	"github.com/xenith/panda/internal/storage"
	"github.com/xenith/panda/webui/panel"
	"github.com/xenith/panda/webui/push"
)

func main() {
	var (
		configPath = flag.String("config", "", "path to config.yaml (default /etc/panda/config.yaml)")
		staticDir  = flag.String("static", "webui/web/pwa", "directory holding the PWA static files")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	if cfg.Network.PanelAddr == "" {
		fatal("config", fmt.Errorf("network.panel_addr is not set (refusing to serve with no bind address)"))
	}
	if cfg.Network.PanelToken == "" {
		fatal("config", fmt.Errorf("network.panel_token is not set (refusing to serve /api/* unauthenticated)"))
	}

	log.Setup(cfg.Log.Level, nil)
	logger := log.From(context.Background())

	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		fatal("migrate database", err)
	}

	store := core.NewTaskStore(db, logger)

	var pushSvc *push.Service
	if cfg.Push.Enabled {
		keys, err := push.LoadOrCreateVAPIDKeys(cfg.Push.VAPIDKeyPath, cfg.Push.VAPIDSubject)
		if err != nil {
			fatal("load vapid keys", err)
		}
		pushSvc = push.NewService(keys, push.NewStore(db), logger)
		store.SetOnReview(func(t core.Task) {
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

	srv := &http.Server{
		Addr:    cfg.Network.PanelAddr,
		Handler: panel.New(store, *staticDir, pushSvc, cfg.Network.PanelToken),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	logger.Info("panda webui sidecar listening", "addr", cfg.Network.PanelAddr, "static", *staticDir)

	select {
	case <-ctx.Done():
		logger.Info("panda webui sidecar shutting down")
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
