// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/fslock"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// withOwner combines a strict process lock with a renewable database lease.
// Every cache/cursor transaction checks that lease again before publishing.
func (s *Service) withOwner(ctx context.Context, blocking bool, fn func(context.Context, string) error) error {
	if err := s.privateDir(); err != nil {
		return err
	}
	path := filepath.Join(s.dir, "runtime.lock")
	var unlock func()
	var err error
	if blocking {
		unlock, err = fslock.AcquireStrict(ctx, path)
	} else {
		unlock, err = fslock.TryAcquireStrict(path)
	}
	if errors.Is(err, fslock.ErrHeld) {
		return ErrRuntimeHeld
	}
	if err != nil {
		return err
	}
	defer unlock()
	owner, err := util.UUIDv7()
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	result, err := s.db.ExecContext(ctx, `INSERT INTO task_runtime_leases(lease_key,owner_id,expires_at) VALUES('sync',?,?) ON CONFLICT(lease_key) DO UPDATE SET owner_id=excluded.owner_id,expires_at=excluded.expires_at WHERE task_runtime_leases.expires_at<=?`, owner, now+45000, now)
	if err != nil {
		return err
	}
	if err = requireOne(result); err != nil {
		if errors.Is(err, ErrStale) {
			return ErrRuntimeHeld
		}
		return err
	}
	ownCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	lost := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ownCtx.Done():
				return
			case <-tick.C:
				now := s.now().UnixMilli()
				r, e := s.db.ExecContext(ownCtx, `UPDATE task_runtime_leases SET expires_at=? WHERE lease_key='sync' AND owner_id=? AND expires_at>?`, now+45000, owner, now)
				if e == nil {
					e = requireOne(r)
				}
				if e != nil {
					lost <- e
					cancel()
					return
				}
			}
		}
	}()
	err = fn(ownCtx, owner)
	cancel()
	wg.Wait()
	select {
	case e := <-lost:
		err = e
	default:
	}
	cleanup, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	_, _ = s.db.ExecContext(cleanup, `DELETE FROM task_runtime_leases WHERE lease_key='sync' AND owner_id=?`, owner)
	return err
}

// Run belongs to the node owner, not to a browser request or borrowed front
// end. Closing any page leaves this duty running; cancelling ownership stops
// requests and joins the lease heartbeat before returning.
func (s *Service) Run(ctx context.Context) error { return s.run(ctx, nil) }

func (s *Service) run(ctx context.Context, ready chan<- error) error {
	var once sync.Once
	started := false
	signal := func(err error) {
		if ready != nil {
			once.Do(func() { ready <- err })
		}
	}
	defer func() { signal(ctx.Err()) }()
	for ctx.Err() == nil {
		err := s.withOwner(ctx, true, func(owned context.Context, owner string) error {
			owned, cancel := context.WithCancel(owned)
			defer cancel()
			if e := s.execution(); e != nil {
				if err := e.recover(owned, owner); err != nil {
					signal(err)
					return err
				}
				done := make(chan struct{})
				go func() { defer close(done); e.loop(owned, owner) }()
				defer func() { cancel(); <-done }()
			}
			resultDone := make(chan struct{})
			go func() {
				defer close(resultDone)
				for owned.Err() == nil {
					s.flushPendingResults(owned, owner)
					if wait(owned, 5*time.Second) != nil {
						return
					}
				}
			}()
			defer func() { cancel(); <-resultDone }()
			started = true
			signal(nil)
			for owned.Err() == nil {
				conns, e := s.List(owned)
				if e != nil {
					return e
				}
				for _, c := range conns {
					if (c.State == "connected" || c.State == "authorizing" || c.State == "unsupported") && c.NextSyncAt <= s.now().UnixMilli() {
						_ = s.syncConnection(owned, owner, c.ID)
					}
				}
				if e = wait(owned, time.Second); e != nil {
					return e
				}
			}
			return owned.Err()
		})
		// Once Start has returned, a lost lease or transient read failure must
		// reacquire ownership instead of silently terminating the service.
		if ready != nil && !started && err != nil {
			signal(err)
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = wait(ctx, 5*time.Second); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// SyncNow is a bounded one-shot owner for a CLI with no daemon. When a
// persistent owner exists, callers leave RequestSync queued for that owner.
func (s *Service) SyncNow(ctx context.Context, id string) error {
	return s.withOwner(ctx, false, func(owned context.Context, owner string) error {
		return s.syncConnection(owned, owner, id)
	})
}
