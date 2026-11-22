// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
)

type requestSource struct {
	*syncSource
	task taskproviders.Snapshot
}

func (p *requestSource) Task(context.Context, taskproviders.Key) (taskproviders.Snapshot, error) {
	return p.task, nil
}
func requestService(t *testing.T) (*Service, *executionRuntime, *requestSource) {
	t.Helper()
	s, e, _, _, wire := automationTest(t, true)
	snap, err := wire.Snapshot(1, "Space")
	if err != nil {
		t.Fatal(err)
	}
	provider, _, err := s.source(Connection{})
	if err != nil {
		t.Fatal(err)
	}
	p := &requestSource{syncSource: provider.(*syncSource), task: snap}
	s.source = func(Connection) (taskproviders.TaskProvider, func() error, error) {
		return p, func() error { return nil }, nil
	}
	return s, e, p
}
func TestExplicitRequestSharesAutomaticTrigger(t *testing.T) {
	s, e, p := requestService(t)
	ctx := context.Background()
	id, _ := util.UUIDv7()
	first, err := s.RequestExecution(ctx, "c", p.task.Key, id)
	if err != nil || first.AlreadyStarted || first.RequestID != id {
		t.Fatalf("first=%+v %v", first, err)
	}
	if err := e.discover(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	other, _ := util.UUIDv7()
	again, err := s.RequestExecution(ctx, "c", p.task.Key, other)
	if err != nil || !again.AlreadyStarted || again.RequestID != id {
		t.Fatalf("again=%+v %v", again, err)
	}
	if n := scalar(t, s.db, `SELECT count(*) FROM task_flow_runs`); n != "1" {
		t.Fatal(n)
	}
	if n := scalar(t, s.db, `SELECT count(*) FROM tasks`); n != "0" {
		t.Fatal("foreground request bypassed cloud lease")
	}
	var claim tsync.ExecutionRequest
	f, err := s.flow(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	claim = f.Claim
	if claim.Purpose != "execute" || claim.Generation != 1 {
		t.Fatal(claim)
	}
}
func TestExplicitRequestConcurrentReplayAndPolicy(t *testing.T) {
	s, _, p := requestService(t)
	id, _ := util.UUIDv7()
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := s.RequestExecution(ctx, "c", p.task.Key, id); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := scalar(t, s.db, `SELECT count(*) FROM task_flow_runs`); n != "1" {
		t.Fatal(n)
	}
	if err := s.SetPolicy(ctx, "c", "s", Policy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestExecution(ctx, "c", p.task.Key, id); err == nil {
		t.Fatal("disabled policy granted request")
	}
}
func TestExplicitRequestRejectsUnassignedAndStaleTask(t *testing.T) {
	for _, kind := range []string{"unassigned", "input", "generation", "request-id"} {
		t.Run(kind, func(t *testing.T) {
			s, _, p := requestService(t)
			id, _ := util.UUIDv7()
			switch kind {
			case "unassigned":
				p.task.Permissions.Execute = false
			case "input":
				p.task.InputRevision = "new"
			case "generation":
				p.task.AutomationGeneration = "9"
			case "request-id":
				id = "not-a-uuid"
			}
			_, err := s.RequestExecution(context.Background(), "c", p.task.Key, id)
			if err == nil {
				t.Fatal("invalid authority accepted")
			}
			if kind == "request-id" && !errors.Is(err, taskproviders.ErrPrecondition) {
				t.Fatal(err)
			}
			if n := scalar(t, s.db, `SELECT count(*) FROM task_flow_runs`); n != "0" {
				t.Fatal(n)
			}
		})
	}
}

func TestFirstDraftIsScopedAtomicAndReplayable(t *testing.T) {
	s, e, src := requestService(t)
	ctx := context.Background()
	now := s.now()
	s.now = func() time.Time { return now }
	key := src.task.Key
	consent := DraftConsent{AccountID: "a", AuthEpoch: "1", TaskRevision: src.task.TaskRevision}
	c, err := s.Get(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	consent.AccountID = c.AccountID
	id, _ := util.UUIDv7()
	if _, err = s.RequestFirstDraft(ctx, "c", key, id, consent); !errors.Is(err, taskproviders.ErrConflict) {
		t.Fatalf("existing policy: %v", err)
	}
	if _, err = s.db.Exec("DELETE FROM task_policies"); err != nil {
		t.Fatal(err)
	}
	invalid := consent
	invalid.TaskRevision = "stale"
	if _, err = s.RequestFirstDraft(ctx, "c", key, id, invalid); err == nil {
		t.Fatal("stale consent accepted")
	}
	// A changed cached version fails after the provisional policy insert.
	if _, err = s.db.Exec("UPDATE task_snapshots SET snapshot_json=json_set(snapshot_json,'$.taskRevision','stale')"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestFirstDraft(ctx, "c", key, id, consent); err == nil {
		t.Fatal("stale snapshot accepted")
	}
	if n := scalar(t, s.db, "SELECT count(*) FROM task_policies"); n != "0" {
		t.Fatal("partial policy persisted")
	}
	if _, err = s.db.Exec("UPDATE task_snapshots SET snapshot_json=json_set(snapshot_json,'$.taskRevision',?)", consent.TaskRevision); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			run, err := s.RequestFirstDraft(ctx, "c", key, id, consent)
			if err != nil || run.RequestID != id {
				t.Errorf("replay: %+v %v", run, err)
			}
		})
	}
	wg.Wait()
	if err = e.discover(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	p, found, err := s.LookupPolicy(ctx, "c", key.SpaceID)
	if err != nil || !found || len(p.TaskIDs) != 1 || p.TaskIDs[0] != key.TaskID || !p.Execute || p.FinalAction || p.Research || p.ExpiresAt != s.now().Add(24*time.Hour).UnixMilli() {
		t.Fatalf("policy: %+v %v", p, err)
	}
	if n := scalar(t, s.db, "SELECT count(*) FROM task_flow_runs"); n != "1" {
		t.Fatalf("runs: %s", n)
	}
}
