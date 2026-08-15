package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xenith/panda/internal/util"
)

// dreamInterval is the minimum gap between Deep promotions (design §17.3:
// Dreaming runs when the node is idle, at most once per day).
const dreamInterval = 24 * time.Hour

// Scheduler runs the Dreamer when the node is idle, at most once per day. It
// is the Go-side trigger (design P3-11); the "idle" predicate is supplied by
// the caller (the core checks its running-task count) so this package stays
// free of a scheduler dependency.
type Scheduler struct {
	dreamer *Dreamer
	diary   *DreamDiary
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

// tick runs one sweep if the node is idle and a dream is due (>=dreamInterval
// since the last one). It reports whether a sweep ran.
func (s *Scheduler) tick(now time.Time) (bool, error) {
	if s.idle != nil && !s.idle() {
		return false, nil
	}
	last, err := s.lastDeep(now)
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
	if err := s.markDeep(now); err != nil {
		return false, err
	}
	if s.diary != nil {
		if err := s.diary.Append(report, now); err != nil {
			return false, err
		}
	}
	return true, nil
}

// statePath is the .dreams/last-deep timestamp file (OpenClaw's machine-state
// directory).
func (s *Scheduler) statePath() string {
	return filepath.Join(s.dreamer.hermes.ColdDir(), "last-deep")
}

// lastDeep reads the previous promotion timestamp, or a zero time on first run.
func (s *Scheduler) lastDeep(now time.Time) (time.Time, error) {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("memory: read last-deep: %w", err)
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("memory: parse last-deep: %w", err)
	}
	return time.Unix(sec, 0), nil
}

// markDeep records now as the last promotion time.
func (s *Scheduler) markDeep(now time.Time) error {
	if err := os.MkdirAll(s.dreamer.hermes.ColdDir(), 0o755); err != nil {
		return fmt.Errorf("memory: create .dreams dir: %w", err)
	}
	if err := util.WriteFileAtomic(s.statePath(), []byte(strconv.FormatInt(now.Unix(), 10)), 0o644); err != nil {
		return fmt.Errorf("memory: write last-deep: %w", err)
	}
	return nil
}
