// Package updater implements the self-update path for the `panda` CLI: it
// checks GitHub for a newer release, downloads and verifies the platform's
// release archive, and — once the task queue is idle — atomically swaps the
// running binary (and its agent adapters) into place and restarts. Everything
// it downloads is staged under a temporary directory that is removed on
// completion, error, or cancel, so a cancelled or deleted update leaves no
// residue behind.
package updater

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Manager drives the check → download → apply pipeline and is safe for
// concurrent use from the web panel. All state transitions happen under a
// mutex; the long-running network steps (check, download) release the lock so
// status polls are never blocked.
type Manager struct {
	mu       sync.Mutex
	opts     Options
	stage    Stage
	latest   string
	notes    string
	errMsg   string
	staged   *stagedRelease
	notified string // last version OnAvailable fired for, so ticks don't repeat
}

// stagedRelease is a downloaded-and-verified release waiting to be applied.
type stagedRelease struct {
	dir     string // temporary staging dir (removed on cleanup)
	root    string // extracted openpanda/ dir inside dir
	version string
}

// New builds a Manager from opts. Empty values fall back to safe defaults:
// repo → DefaultRepo, current → "0.0.0", idle → always true.
func New(opts Options) *Manager {
	if opts.Repo == "" {
		opts.Repo = DefaultRepo
	}
	if opts.Current == "" {
		opts.Current = "0.0.0"
	}
	if opts.Idle == nil {
		opts.Idle = func(context.Context) bool { return true }
	}
	if opts.Logger == nil {
		opts.Logger = defaultLogger()
	}
	return &Manager{opts: opts, stage: StageIdle}
}

// Status returns a point-in-time snapshot for GET /api/update.
func (m *Manager) Status() Status {
	return m.status(context.Background())
}

func (m *Manager) status(ctx context.Context) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(ctx)
}

func (m *Manager) statusLocked(ctx context.Context) Status {
	st := Status{
		Stage:   m.stage,
		Current: m.opts.Current,
		Latest:  m.latest,
		Notes:   m.notes,
		Error:   m.errMsg,
		Idle:    m.opts.Idle(ctx),
	}
	switch m.stage {
	case StageAvailable, StageDownloading, StageStaged, StageApplying:
		st.Available = true
	}
	return st
}

// Check queries GitHub for the latest release and updates the stage to
// available (newer) or idle (up to date). A network or GitHub error leaves the
// manager in the error stage with the message recorded; the auto-check loop
// retries on the next tick. When a newer release is found, OnAvailable (if
// set) fires once per version — the headless daemon's update notice.
func (m *Manager) Check(ctx context.Context) error {
	m.mu.Lock()
	m.stage = StageChecking
	m.errMsg = ""
	m.latest = ""
	m.notes = ""
	m.mu.Unlock()

	rel, err := Latest(ctx, m.opts.Repo)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.errMsg = err.Error()
		m.stage = StageError
		return err
	}
	m.latest = rel.Version
	m.notes = summarizeNotes(rel.Notes)
	if CompareVersion(rel.Version, m.opts.Current) > 0 {
		m.stage = StageAvailable
		if m.opts.OnAvailable != nil && m.notified != rel.Version {
			m.notified = rel.Version
			m.opts.OnAvailable(rel.Version)
		}
	} else {
		m.stage = StageIdle
	}
	return nil
}

// Download fetches and verifies the latest release, staging it for Apply. Any
// previously staged release is discarded first. On error the staging dir is
// removed and the manager moves to the error stage.
func (m *Manager) Download(ctx context.Context) error {
	m.mu.Lock()
	version := m.latest
	if version == "" || CompareVersion(version, m.opts.Current) <= 0 {
		m.mu.Unlock()
		return fmt.Errorf("no newer release is known; run a check first")
	}
	m.stage = StageDownloading
	m.errMsg = ""
	m.mu.Unlock()

	dir, err := os.MkdirTemp("", "openpanda-update-")
	if err != nil {
		m.fail(err)
		return err
	}
	archive, err := downloadRelease(ctx, m.opts.Repo, version, dir)
	if err != nil {
		os.RemoveAll(dir)
		m.fail(err)
		return err
	}
	root, err := extractRelease(archive, dir)
	if err != nil {
		os.RemoveAll(dir)
		m.fail(err)
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "bin", exeName())); err != nil {
		os.RemoveAll(dir)
		m.fail(fmt.Errorf("release archive has no %s binary", exeName()))
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.staged != nil {
		os.RemoveAll(m.staged.dir)
	}
	m.staged = &stagedRelease{dir: dir, root: root, version: version}
	m.stage = StageStaged
	return nil
}

// Apply installs the staged release over the running install and restarts the
// process so the new code runs. It refuses to proceed while the task queue is
// busy (opts.Idle returns false), so an update never interrupts live work.
func (m *Manager) Apply(ctx context.Context) error {
	m.mu.Lock()
	if m.staged == nil {
		m.mu.Unlock()
		return fmt.Errorf("nothing staged to apply")
	}
	if !m.opts.Idle(ctx) {
		m.mu.Unlock()
		return fmt.Errorf("task queue is busy; apply once it is idle")
	}
	s := m.staged
	m.stage = StageApplying
	m.errMsg = ""
	m.mu.Unlock()

	if err := applyRelease(ctx, m, s); err != nil {
		m.fail(err)
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	os.RemoveAll(s.dir)
	m.staged = nil
	m.stage = StageDone
	return nil
}

// Cancel aborts any in-flight step and discards the staged release. It is the
// "delete" path: the staged download (and its archive) are removed from disk,
// leaving no residue.
func (m *Manager) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.staged != nil {
		os.RemoveAll(m.staged.dir)
		m.staged = nil
	}
	m.latest = ""
	m.notes = ""
	m.errMsg = ""
	m.stage = StageIdle
}

func (m *Manager) fail(err error) {
	m.mu.Lock()
	m.errMsg = err.Error()
	m.stage = StageError
	m.mu.Unlock()
}

// StartAutoCheck runs a background version check every interval, so an update
// is discovered during normal use rather than only on demand. It skips ticks
// while a step is in flight or an update is already staged for the user. A
// network failure merely records the error and retries on the next tick.
func (m *Manager) StartAutoCheck(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	go func() {
		timer := time.NewTimer(10 * time.Second) // gentle startup delay
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				m.mu.Lock()
				skip := m.stage == StageChecking || m.stage == StageDownloading ||
					m.stage == StageApplying || m.stage == StageStaged
				m.mu.Unlock()
				if !skip {
					_ = m.Check(ctx)
				}
				timer.Reset(interval)
			}
		}
	}()
}
