// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

// Concurrency guards for the shared-state layer: the engine's *config.Config
// is aliased by the REPL (r.cfg) and mutated from its goroutines while ask
// and tool goroutines read the same fields; the scheduler pointer is lazy-
// initialized on ask goroutines while exec paths (CancelTask, DialPeer) read
// it. These tests only fail under `go test -race`.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// TestSharedConfigConcurrentAccess runs the MutateConfig write protocol (the
// path /model, /nodes add and /config set take) against the field reads ask
// and tool goroutines perform: ModelConfig, approvalFor, WorkPath,
// systemStatus, checkCardPath. Without cfgMu/cardMu this is a torn
// read/write on cfg.Model and e.cardPath.
func TestSharedConfigConcurrentAccess(t *testing.T) {
	e, _ := newMgmtTestEngine(t)
	ctx := context.Background()

	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { // writer: every hot field a front end can mutate
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			v := i
			e.MutateConfig(func(c *config.Config) {
				c.Model.Model = fmt.Sprintf("model-%d", v)
				c.Approval.Mode = config.ApprovalModeOnRequest
				c.Storage.WorkPath = fmt.Sprintf("/tmp/w%d", v)
				c.Network.Peers = []string{fmt.Sprintf("127.0.0.1:%d", v%65536)}
				c.UI.Locale = "en"
			})
		}
	}()

	readers := func() { // reader: the goroutine-visible engine accessors
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = e.ModelConfig()
			_ = e.WorkPath()
			_ = e.approvalFor("sess", "proj")
			_, _ = e.systemStatus(ctx)
			_, _ = e.checkCardPath()
			_ = e.CardPath()
		}
	}
	wg.Add(2)
	go readers()
	go readers()

	time.Sleep(80 * time.Millisecond)
	close(done)
	wg.Wait()
}

// TestSchedulerInitConcurrentAccess races the lazy scheduler init
// (tryAutoInitScheduler on an ask goroutine) against the exec-side readers
// that never held schedMu: CancelTask, DialPeer, CardPath, bare Load.
func TestSchedulerInitConcurrentAccess(t *testing.T) {
	e, _ := newMgmtTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Clear the seeded scheduler so lazy init races the readers.
	e.sched.Store(nil)

	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			e.tryAutoInitScheduler()
		}
	}()

	readers := func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = e.sched.Load()
			_, _ = e.CancelTask(ctx, "no-such-task")
			_ = e.CardPath()
			_ = e.ModelConfig()
		}
	}
	wg.Add(2)
	go readers()
	go readers()

	time.Sleep(80 * time.Millisecond)
	close(done)
	wg.Wait()
	if e.sched.Load() == nil {
		t.Fatal("scheduler should have been lazily initialized")
	}
}
