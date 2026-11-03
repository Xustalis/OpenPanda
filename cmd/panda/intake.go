// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/intake"
)

// startIntake serves the operator's webhook listener until ctx ends.
//
// A bind failure is logged rather than fatal: the rest of the node — mesh,
// queue, CLI — works without inbound events, and taking the daemon down would
// turn a missing webhook into a missing node. The operator sees the error on
// startup and in the log.
func startIntake(ctx context.Context, logger *slog.Logger, node *core.Core, cfg config.IntakeConfig) {
	if !cfg.Enabled {
		return
	}
	srv := &intake.Server{
		Policy: intake.Policy{
			Enabled:        true,
			Repos:          cfg.GitHub.Repos,
			Actors:         cfg.GitHub.Actors,
			Mention:        cfg.GitHub.Mention,
			Self:           cfg.GitHub.Self,
			RequireMention: cfg.GitHub.WantsMention(),
		},
		Secret: cfg.GitHub.Secret,
		Seen:   node,
		Sink: intakeSink{
			core:     node,
			project:  cfg.GitHub.Project,
			priority: intakePriority(cfg.GitHub.Priority),
		},
		Logger: logger,
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("intake listener: bind failed, inbound events will not be accepted",
			"addr", cfg.Listen, "err", err)
		return
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("intake listener ready",
		"addr", ln.Addr().String(),
		"repos", strings.Join(cfg.GitHub.Repos, ","),
		"actors", len(cfg.GitHub.Actors),
		"mention", cfg.GitHub.Mention)

	guard.Go(logger, "intake listener shutdown", nil, func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	})
	guard.Go(logger, "intake listener serve", nil, func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("intake listener stopped", "err", err)
		}
	})
}

// intakeSink adapts the core to the intake package's one-method sink.
type intakeSink struct {
	core     *core.Core
	project  string
	priority int
}

func (s intakeSink) IntakeTask(ctx context.Context, ev intake.Event) (string, error) {
	return s.core.IntakeTask(ctx, ev, s.project, s.priority)
}

// intakePriority maps the configured word onto the queue board's constants.
// An unknown word falls back to normal; config validation already refuses one
// at load time, so this is the second guard rather than the first.
func intakePriority(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "high":
		return core.PriorityHigh
	case "low":
		return core.PriorityLow
	default:
		return core.PriorityNormal
	}
}
