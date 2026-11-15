// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
)

func automationTest(t *testing.T, assigned bool) (*Service, *executionRuntime, Connection, Policy, tsync.Task) {
	t.Helper()
	s, source := newSyncTest(t)
	scopes := []string{"tasks:read", "members:read", "execution:read", "execution:claim", "execution:report"}
	source.account.Scopes = slices.Clone(scopes)
	source.spaces[0].Scopes = slices.Clone(scopes)
	source.caps.Capabilities = append(source.caps.Capabilities, "executions")
	body := "Write a draft about solar energy"
	wire := tsync.Task{Key: taskFor("s", "t").Key, Title: "solar energy", Body: &body, BodyAvailable: true, Status: "todo", Versions: tsync.Versions{Task: "r1", Input: "i1", Assignment: "a1", Generation: 1}, ApprovalMode: taskproviders.BeforeSubmit}
	wire.Permissions.Read, wire.Permissions.Claim = true, true
	if assigned {
		wire.Assignees = []string{"a"}
	}
	snapshot, err := wire.Snapshot(1, "Space")
	if err != nil {
		t.Fatal(err)
	}
	source.begin = func(string) (taskproviders.SnapshotPage, error) {
		return taskproviders.SnapshotPage{SnapshotID: "snapshot", FeedEpoch: "f", AuthEpoch: "1", Cursor: "cursor", TotalTasks: 1, ExpiresAt: time.Now().Add(time.Hour), Tasks: []taskproviders.Snapshot{snapshot}}, nil
	}
	syncOK(t, s)
	p := Policy{Research: true, Execute: true, Mode: taskproviders.BeforeSubmit, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), MaxConcurrent: 2, DailyTokens: 20000, PreparationTokens: 512, Capabilities: []string{"draft"}}
	if err = s.SetPolicy(context.Background(), "c", "s", p); err != nil {
		t.Fatal(err)
	}
	c, err := s.Get(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO task_runtime_leases(lease_key,owner_id,expires_at) VALUES('sync','owner',?)`, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	e := &executionRuntime{service: s, owner: "owner", runs: map[string]*managedExecution{}}
	return s, e, c, p, wire
}

func automationServer(t *testing.T, s *Service, c Connection, wire tsync.Task, extra http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tsync/v1/integration/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"accountId": c.AccountID, "active": true}, "device": map[string]any{"deviceId": c.DeviceID, "revoked": false}, "scopes": c.Scopes, "spaces": []tsync.Space{{SpaceID: "s", WorkspaceID: "w", Scopes: c.Scopes}}, "authEpoch": 1})
		case "/tsync/v1/integration/tasks/t":
			_ = json.NewEncoder(w).Encode(wire)
		default:
			extra(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := tsync.New(server.URL + "/tsync/")
	if err != nil {
		t.Fatal(err)
	}
	client.Token = func(context.Context) (string, error) { return "test-token", nil }
	s.executionClient = func(Connection) (*tsync.Client, error) { return client, nil }
}

func discoverTestFlow(t *testing.T, s *Service, e *executionRuntime) flowRun {
	t.Helper()
	if err := e.discover(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	id := scalar(t, s.db, `SELECT request_id FROM task_flow_runs`)
	f, err := s.flow(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func leaseRecord(f flowRun) tsync.ExecutionRecord {
	now := time.Now().UTC()
	until := now.Add(60 * time.Second).Format(time.RFC3339Nano)
	return tsync.ExecutionRecord{ExecutionID: "execution", RequestID: f.ID, TaskKey: f.Snapshot.Key, AccountID: f.AccountID, DeviceID: f.Claim.DeviceID, TriggerKey: f.Trigger, Versions: tsync.Versions{Input: f.Input, Assignment: f.Assignment, Generation: 1}, Purpose: f.Claim.Purpose, Scope: f.Claim.Scope, Mode: f.Claim.Mode, Status: taskproviders.Queued, Fence: tsync.Fence{Epoch: "epoch", Counter: 1}, ServerTime: now.Format(time.RFC3339Nano), LeaseUntil: &until}
}

func TestExecutionCanonicalDelegationAndDisplayOnly(t *testing.T) {
	s, _, c, p, wire := automationTest(t, false)
	wire.Assignees = []string{"another-account"}
	snapshot, err := wire.Snapshot(1, "Space")
	if err != nil {
		t.Fatal(err)
	}
	if err = eligible(c, p, snapshot, s.now()); err != nil {
		t.Fatalf("canonical accepted delegation rejected: %v", err)
	}
	wire.Permissions.Claim = false
	snapshot, err = wire.Snapshot(1, "Space")
	if err != nil {
		t.Fatal(err)
	}
	if eligible(c, p, snapshot, s.now()) == nil {
		t.Fatal("another member's task accepted without claim permission")
	}
}

func TestAutomationSnapshotRejectsChangedAuthority(t *testing.T) {
	for _, change := range []string{"permissions", "status", "taskRevision"} {
		t.Run(change, func(t *testing.T) {
			s, _, c, p, wire := automationTest(t, true)
			original, err := wire.Snapshot(1, "Space")
			if err != nil {
				t.Fatal(err)
			}
			current := original
			switch change {
			case "permissions":
				current.Permissions.Execute = false
			case "status":
				current.Status = "done"
			case "taskRevision":
				current.TaskRevision = "r2"
			}
			raw, _ := json.Marshal(current)
			if _, err = s.db.Exec(`UPDATE task_snapshots SET snapshot_json=?`, string(raw)); err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = s.automationSnapshotTx(context.Background(), tx, c, p, original); !errors.Is(err, taskproviders.ErrAuthorizationChanged) {
				t.Fatalf("changed %s authorized: %v", change, err)
			}
		})
	}
}

func TestExecutionClaimRetryKeepsKeyAndRequiresFreshFence(t *testing.T) {
	s, e, c, _, wire := automationTest(t, true)
	f := discoverTestFlow(t, s, e)
	var keys []string
	var heartbeat atomic.Int32
	automationServer(t, s, c, wire, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tsync/v1/integration/tasks/t/executions":
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if len(keys) == 1 {
				w.WriteHeader(503)
				return
			}
			record := leaseRecord(f)
			// A replayed claim's clock is stale; only the heartbeat is authority.
			record.ServerTime = time.Now().Add(-24 * time.Hour).Format(time.RFC3339Nano)
			_ = json.NewEncoder(w).Encode(record)
		case "/tsync/v1/integration/executions/execution/heartbeat":
			heartbeat.Add(1)
			record := leaseRecord(f)
			_ = json.NewEncoder(w).Encode(record)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &managedExecution{runtime: e, id: f.ID, owner: "owner", ctx: ctx, cancel: cancel}
	if err := m.acquire(); err == nil {
		t.Fatal("lost claim response accepted")
	}
	m.lose(errors.New("response lost"))
	if key := scalar(t, s.db, `SELECT claim_id FROM task_flow_runs`); key != f.ClaimID {
		t.Fatal("lost response changed idempotency key")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	m2 := &managedExecution{runtime: e, id: f.ID, owner: "owner", ctx: ctx2, cancel: cancel2}
	if err := m2.acquire(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != keys[1] || heartbeat.Load() != 1 || time.Until(m2.deadline) < time.Second {
		t.Fatalf("keys=%v, heartbeat=%d", keys, heartbeat.Load())
	}
}

func TestExecutionRejectsWrongFreshFenceAndIdentity(t *testing.T) {
	for _, kind := range []string{"fence", "account", "scope", "elapsed"} {
		t.Run(kind, func(t *testing.T) {
			s, e, c, _, wire := automationTest(t, true)
			f := discoverTestFlow(t, s, e)
			automationServer(t, s, c, wire, func(w http.ResponseWriter, r *http.Request) {
				record := leaseRecord(f)
				if r.URL.Path == "/tsync/v1/integration/executions/execution/heartbeat" {
					switch kind {
					case "fence":
						record.Fence.Counter++
					case "account":
						record.AccountID = "other"
					case "scope":
						record.Scope.FullGoal = false
					case "elapsed":
						record.ServerTime = *record.LeaseUntil
					}
				}
				_ = json.NewEncoder(w).Encode(record)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := &managedExecution{runtime: e, id: f.ID, owner: "owner", ctx: ctx, cancel: cancel}
			if err := m.acquire(); err == nil {
				t.Fatal("invalid fresh lease accepted")
			}
			if !m.deadline.IsZero() || scalar(t, s.db, `SELECT deadline FROM task_flow_runs`) != "0" {
				t.Fatal("invalid response granted authority")
			}
		})
	}
}

func TestResearchBudgetReservationsAreAtomicAndSurviveInvalidation(t *testing.T) {
	s, e, c, p, wire := automationTest(t, false)
	p.DailyTokens = 2300
	if err := s.SetPolicy(context.Background(), "c", "s", p); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := wire.Snapshot(1, "Space")
	var wg sync.WaitGroup
	var calls atomic.Int32
	for _, key := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := e.reserveBudget(context.Background(), "owner", c, p, snapshot, key, "memory", "model", 2300, nil)
			if err != nil && !errors.Is(err, ErrBudget) {
				t.Error(err)
			}
			if ok {
				calls.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("reservations=%d", calls.Load())
	}
	if _, err := s.db.Exec(`UPDATE research_briefs SET brief='private',state='done'`); err != nil {
		t.Fatal(err)
	}
	p.Research = false
	if err := s.SetPolicy(context.Background(), "c", "s", p); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.db, `SELECT brief FROM research_briefs`) != "" || scalar(t, s.db, `SELECT SUM(reserved_tokens) FROM research_briefs`) != "2300" {
		t.Fatal("invalidation erased cost ledger or retained private text")
	}
	p.Research = true
	if err := s.SetPolicy(context.Background(), "c", "s", p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.reserveBudget(context.Background(), "owner", c, p, snapshot, "three", "memory", "model", 2300, nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("budget reset by policy toggle: %v", err)
	}
}

func TestResearchPolicyChangeDuringModelCannotPublishOrReplay(t *testing.T) {
	s, e, c, p, wire := automationTest(t, false)
	automationServer(t, s, c, wire, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	model := &auxiliaryTestModel{call: func() {
		p.Research = false
		if err := s.SetPolicy(context.Background(), "c", "s", p); err != nil {
			t.Error(err)
		}
	}}
	e.options = AutomationOptions{Model: func() AuxiliaryModel { return model }, PrivateMemory: func() ([]string, error) { return []string{"solar energy research experience"}, nil }}
	if err := e.researchOnce(context.Background(), "owner"); err == nil {
		t.Fatal("changed policy published private result")
	}
	if model.calls.Load() != 1 || scalar(t, s.db, `SELECT brief FROM research_briefs`) != "" {
		t.Fatal("invalid private result")
	}
	p.Research = true
	if err := s.SetPolicy(context.Background(), "c", "s", p); err != nil {
		t.Fatal(err)
	}
	if err := e.researchOnce(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	if model.calls.Load() != 1 {
		t.Fatal("invalidated work was replayed")
	}
}

type auxiliaryTestModel struct {
	calls atomic.Int32
	call  func()
}

func (m *auxiliaryTestModel) AuxiliaryVersion() string { return "model" }
func (m *auxiliaryTestModel) CompleteBounded(context.Context, string, string, int) (string, error) {
	m.calls.Add(1)
	if m.call != nil {
		m.call()
	}
	return "private result", nil
}
