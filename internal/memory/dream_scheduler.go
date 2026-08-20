package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// dreamInterval is the minimum gap between Deep promotions (design §17.3:
// Dreaming runs when the node is idle, at most once per day).
const dreamInterval = 24 * time.Hour

// pruneInterval is the minimum gap between daily-log retention sweeps (A4).
// Independent of dreamInterval: pruning is cheap filesystem housekeeping and
// must happen even on days no dream is due.
const pruneInterval = 24 * time.Hour

// Scheduler runs the Dreamer when the node is idle, at most once per day. It
// is the Go-side trigger (design P3-11); the "idle" predicate is supplied by
// the caller (the core checks its running-task count) so this package stays
// free of a scheduler dependency. When a Daily store is attached (WithDaily),
// each tick additionally enforces the daily-log retention windows at most once
// per day — the production wiring of daily.Prune (A4).
type Scheduler struct {
	dreamer *Dreamer
	diary   *DreamDiary
	daily   *Daily
	idle    func() bool
	every   time.Duration
	// OnError, when set, receives sweep failures so the daemon can log them
	// rather than have them swallowed silently.
	OnError func(error)
}

// NewScheduler builds a scheduler. idle may be nil (treat as always idle);
// every is the tick interval.
func NewScheduler(dreamer *Dreamer, diary *DreamDiary, idle func() bool, every time.Duration) *Scheduler {
	return &Scheduler{dreamer: dreamer, diary: diary, idle: idle, every: every}
}

// WithDaily attaches the warm-layer daily store so each tick enforces the
// retention windows (archive >30d, delete >90d) at most once per day.
// Chainable; nil disables the sweep.
func (s *Scheduler) WithDaily(d *Daily) *Scheduler {
	s.daily = d
	return s
}

// Run ticks until ctx is done. A failed sweep is reported to OnError (if set)
// and retried on the next tick rather than aborting the loop.
func (s *Scheduler) Run(ctx context.Context) error {
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			if _, err := s.tick(now); err != nil && s.OnError != nil {
				s.OnError(err)
			}
		}
	}
}

// tick enforces the daily-log retention windows when due, then runs one dream
// sweep if the node is idle and a dream is due (>=dreamInterval since the
// last one). Pruning runs before the idle/dream gates: it is cheap filesystem
// housekeeping that should not depend on the node being idle or a dream being
// due. It reports whether a dream sweep ran.
func (s *Scheduler) tick(now time.Time) (bool, error) {
	if err := s.pruneIfDue(now); err != nil {
		return false, err
	}
	if s.idle != nil && !s.idle() {
		return false, nil
	}
	last, err := s.readState("last-deep")
	if err != nil {
		return false, err
	}
	if now.Sub(last) < dreamInterval {
		return false, nil
	}
	report, err := s.dreamer.Dream()
	if err != nil {
		return false, err
	}
	if err := s.writeState("last-deep", now); err != nil {
		return false, err
	}
	if s.diary != nil {
		if err := s.diary.Append(report, now); err != nil {
			return false, err
		}
	}
	return true, nil
}

// pruneIfDue runs daily.Prune at most once per pruneInterval, recording the
// last run in .dreams/last-prune (a separate stamp from last-deep so a dream
// neither triggers nor suppresses a prune). No Daily attached: no-op.
func (s *Scheduler) pruneIfDue(now time.Time) error {
	if s.daily == nil {
		return nil
	}
	last, err := s.readState("last-prune")
	if err != nil {
		return err
	}
	if now.Sub(last) < pruneInterval {
		return nil
	}
	if err := s.daily.Prune(now); err != nil {
		return fmt.Errorf("memory: prune daily logs: %w", err)
	}
	return s.writeState("last-prune", now)
}

// statePath is a .dreams/ timestamp file (OpenClaw's machine-state directory):
// "last-deep" for promotions, "last-prune" for retention sweeps.
func (s *Scheduler) statePath(name string) string {
	return filepath.Join(s.dreamer.hermes.ColdDir(), name)
}

// readState reads a stored timestamp, or a zero time on first run.
func (s *Scheduler) readState(name string) (time.Time, error) {
	data, err := os.ReadFile(s.statePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("memory: read %s: %w", name, err)
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("memory: parse %s: %w", name, err)
	}
	return time.Unix(sec, 0), nil
}

// writeState records now under the named timestamp file.
func (s *Scheduler) writeState(name string, now time.Time) error {
	if err := os.MkdirAll(s.dreamer.hermes.ColdDir(), 0o755); err != nil {
		return fmt.Errorf("memory: create .dreams dir: %w", err)
	}
	if err := util.WriteFileAtomic(s.statePath(name), []byte(strconv.FormatInt(now.Unix(), 10)), 0o644); err != nil {
		return fmt.Errorf("memory: write %s: %w", name, err)
	}
	return nil
}
