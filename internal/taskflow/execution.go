// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// AutomationOptions is installed only by the process owning the core. Model
// access is tool-free and uses the user's live configuration and credentials.
type AutomationOptions struct {
	Model         func() AuxiliaryModel
	PrivateMemory func() ([]string, error)
	ProjectMemory func(string) ([]string, error)
}

type AuxiliaryModel interface {
	CompleteBounded(context.Context, string, string, int) (string, error)
	AuxiliaryVersion() string
}

type executionRuntime struct {
	service *Service
	core    *core.Core
	options AutomationOptions
	mu      sync.Mutex
	owner   string
	runs    map[string]*managedExecution
}

type flowRun struct {
	ID, ConnectionID, AccountID, Key, Trigger, AuthEpoch         string
	Generation                                                   int64
	Input, Assignment, Automation                                string
	ExecutionID, State, Error, ClaimID, Prepared, PrepareAttempt string
	Snapshot                                                     taskproviders.Snapshot
	Policy                                                       Policy
	Claim                                                        tsync.ExecutionRequest
	Record                                                       tsync.ExecutionRecord
}

const flowColumns = `request_id,connection_id,account_id,task_key,trigger_key,auth_epoch,connection_generation,input_revision,assignment_revision,automation_generation,execution_id,state,error_code,claim_id,prepared_json,prepare_attempt,snapshot_json,policy_json,claim_json,record_json`

func scanFlow(row interface{ Scan(...any) error }) (flowRun, error) {
	var f flowRun
	var snapshot, policy, claim, record string
	err := row.Scan(&f.ID, &f.ConnectionID, &f.AccountID, &f.Key, &f.Trigger, &f.AuthEpoch, &f.Generation, &f.Input, &f.Assignment, &f.Automation, &f.ExecutionID, &f.State, &f.Error, &f.ClaimID, &f.Prepared, &f.PrepareAttempt, &snapshot, &policy, &claim, &record)
	if err != nil {
		return f, err
	}
	for _, item := range []struct {
		raw    string
		target any
	}{{snapshot, &f.Snapshot}, {policy, &f.Policy}, {claim, &f.Claim}, {record, &f.Record}} {
		if err = json.Unmarshal([]byte(item.raw), item.target); err != nil {
			return f, err
		}
	}
	return f, nil
}
func (s *Service) flow(ctx context.Context, id string) (flowRun, error) {
	return scanFlow(s.db.QueryRowContext(ctx, `SELECT `+flowColumns+` FROM task_flow_runs WHERE request_id=?`, id))
}

// ConfigureExecution must precede queue startup. The core also fails closed
// when this owner or a fresh cloud lease is unavailable.
func (s *Service) ConfigureExecution(c *core.Core, opts AutomationOptions) {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	s.exec = &executionRuntime{service: s, core: c, options: opts, runs: map[string]*managedExecution{}}
	c.SetBusinessExecutor(s.exec)
}
func (s *Service) execution() *executionRuntime {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	return s.exec
}

// Start waits for the ownership/recovery barrier; only then may the caller
// start the core scheduler. Run remains available for sync-only owners.
func (s *Service) Start(ctx context.Context) error {
	s.runMu.Lock()
	if s.done != nil {
		select {
		case <-s.done:
		default:
			s.runMu.Unlock()
			return ErrRuntimeHeld
		}
	}
	s.done = make(chan struct{})
	done := s.done
	s.runMu.Unlock()
	ready := make(chan error, 1)
	go func() { defer close(done); _ = s.run(ctx, ready) }()
	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait joins the runtime after its owning context has been cancelled.
func (s *Service) Wait(ctx context.Context) error {
	s.runMu.Lock()
	done := s.done
	s.runMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *executionRuntime) recover(ctx context.Context, owner string) error {
	// Persisted wall-clock deadlines are never authority after restart.
	if _, err := e.service.db.ExecContext(ctx, `UPDATE task_flow_runs SET deadline=0,lease_owner='',state=CASE WHEN state IN ('claiming','leased','running','recovering','lease_lost') THEN 'recovering' ELSE state END WHERE state NOT IN ('succeeded','cancelled','superseded')`); err != nil {
		return err
	}
	// A model invocation with no durable result is ambiguous. Neither a poll nor
	// a restart is permission to spend again or replay possible side effects.
	if _, err := e.service.db.ExecContext(ctx, `UPDATE task_flow_runs SET state='needs_reconciliation',error_code='preparation_interrupted' WHERE prepare_attempt!='' AND prepared_json='' AND state='recovering'`); err != nil {
		return err
	}
	if _, err := e.service.db.ExecContext(ctx, `UPDATE final_actions SET state='unknown',updated_at=? WHERE state='dispatching'`, e.service.now().UnixMilli()); err != nil {
		return err
	}
	if err := e.service.reconcileLocalReceipts(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	e.owner = owner
	e.mu.Unlock()
	return nil
}

func (e *executionRuntime) loop(ctx context.Context, owner string) {
	var wg sync.WaitGroup
	defer func() {
		e.mu.Lock()
		for _, m := range e.runs {
			m.cancel()
		}
		e.mu.Unlock()
		wg.Wait()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			_ = e.researchOnce(ctx, owner)
			if wait(ctx, 30*time.Second) != nil {
				return
			}
		}
	}()
	for ctx.Err() == nil {
		_ = e.discover(ctx, owner)
		rows, err := e.service.db.QueryContext(ctx, `SELECT request_id FROM task_flow_runs WHERE (state IN ('claiming','leased','running','recovering','lease_lost') OR (state='needs_reconciliation' AND prepared_json!='')) AND next_at<=? ORDER BY created_at LIMIT 64`, e.service.now().UnixMilli())
		if err == nil {
			var ids []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			_ = rows.Close()
			for _, id := range ids {
				e.mu.Lock()
				if _, exists := e.runs[id]; exists {
					e.mu.Unlock()
					continue
				}
				runCtx, cancel := context.WithCancel(ctx)
				m := &managedExecution{runtime: e, id: id, owner: owner, ctx: runCtx, cancel: cancel, ready: make(chan struct{})}
				e.runs[id] = m
				e.mu.Unlock()
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer cancel()
					defer func() { e.mu.Lock(); delete(e.runs, id); e.mu.Unlock() }()
					m.coordinate()
				}()
			}
		}
		if wait(ctx, time.Second) != nil {
			return
		}
	}
}

func (e *executionRuntime) discover(ctx context.Context, owner string) error {
	tasks, err := taskhub.New(e.service.db).List(ctx, "")
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ConnectionID == "local" {
			continue
		}
		c, err := e.service.Get(ctx, t.ConnectionID)
		if err != nil {
			continue
		}
		p, err := e.service.Policy(ctx, c.ID, t.Snapshot.Key.SpaceID)
		if err != nil || eligible(c, p, t.Snapshot, e.service.now()) != nil {
			continue
		}
		id, err := util.UUIDv7()
		if err != nil {
			return err
		}
		command, err := util.UUIDv7()
		if err != nil {
			return err
		}
		err = e.service.write(ctx, owner, c, func(tx *sql.Tx) error {
			if err := e.service.automationSnapshotTx(ctx, tx, c, p, t.Snapshot); err != nil {
				return err
			}
			_, _, err := e.service.persistExecutionTx(ctx, tx, c, p, t.Snapshot, id, command)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type managedExecution struct {
	runtime    *executionRuntime
	id, owner  string
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex // serializes heartbeat/release and protects monotonic authority
	record     tsync.ExecutionRecord
	deadline   time.Time
	renewAt    time.Time
	ready      chan struct{}
	acquireErr error
}

func (m *managedExecution) authority(ctx context.Context, fresh bool) (flowRun, Connection, error) {
	s := m.runtime.service
	f, err := s.flow(ctx, m.id)
	if err != nil {
		return f, Connection{}, err
	}
	if f.State == "paused" || f.State == "cancelled" || f.State == "superseded" || f.State == "succeeded" {
		return f, Connection{}, taskproviders.ErrAuthorizationChanged
	}
	c, err := s.Get(ctx, f.ConnectionID)
	if err != nil {
		return f, c, err
	}
	if c.AccountID != f.AccountID || c.AuthEpoch != f.AuthEpoch || c.Generation != f.Generation || c.DeviceID != f.Claim.DeviceID {
		return f, c, taskproviders.ErrAuthorizationChanged
	}
	p, err := s.Policy(ctx, c.ID, f.Snapshot.Key.SpaceID)
	if err != nil {
		return f, c, err
	}
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(f.Policy)
	if string(a) != string(b) {
		return f, c, taskproviders.ErrAuthorizationChanged
	}
	if err = eligible(c, p, f.Snapshot, s.now()); err != nil {
		return f, c, err
	}
	// Check the live authorized cache too; sync invalidation cancels authority
	// even before the next cloud heartbeat detects the changed input/assignment.
	current, err := taskhub.New(s.db).Get(ctx, c.ID, f.Key)
	if err != nil {
		return f, c, err
	}
	if current.Snapshot.InputRevision != f.Input || current.Snapshot.AssignmentRevision != f.Assignment || current.Snapshot.AutomationGeneration != f.Automation {
		return f, c, taskproviders.ErrAuthorizationChanged
	}
	if err := eligible(c, p, current.Snapshot, s.now()); err != nil {
		return f, c, err
	}
	if !fresh {
		return f, c, nil
	}
	client, err := s.executionClient(c)
	if err != nil {
		return f, c, err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return f, c, err
	}
	if me.Account.ID != c.AccountID || !me.Account.Active || me.Device == nil || me.Device.ID != c.DeviceID || me.Device.Revoked || fmt.Sprint(me.AuthEpoch) != c.AuthEpoch {
		return f, c, taskproviders.ErrAuthorizationChanged
	}
	live := c
	live.Scopes = me.Scopes
	live.Spaces = nil
	for _, sp := range me.Spaces {
		if !sp.Archived {
			live.Spaces = append(live.Spaces, taskproviders.Space{SpaceID: sp.SpaceID, WorkspaceID: sp.WorkspaceID, Name: sp.Name, Scopes: sp.Scopes})
		}
	}
	wire, err := client.Task(ctx, f.Snapshot.Key)
	if err != nil {
		return f, c, err
	}
	snapshot, err := wire.Snapshot(me.AuthEpoch, f.Snapshot.SpaceName)
	if err != nil {
		return f, c, err
	}
	if wire.Key != f.Snapshot.Key || snapshot.InputRevision != f.Input || snapshot.AssignmentRevision != f.Assignment || snapshot.AutomationGeneration != f.Automation {
		return f, c, taskproviders.ErrAuthorizationChanged
	}
	return f, c, eligible(live, p, snapshot, s.now())
}

func (m *managedExecution) command(ctx context.Context, kind string, body any) (string, error) {
	id, err := util.UUIDv7()
	if err != nil {
		return "", err
	}
	raw, err := tsync.JSONBytes(body)
	if err != nil {
		return "", err
	}
	now := m.runtime.service.now().UnixMilli()
	_, err = m.runtime.service.db.ExecContext(ctx, `INSERT INTO task_flow_commands(command_id,request_id,kind,body_json,created_at,updated_at) VALUES(?,?,?,?,?,?)`, id, m.id, kind, string(raw), now, now)
	return id, err
}

func (m *managedExecution) checkpoint(ctx context.Context, fresh bool) (flowRun, Connection, error) {
	m.mu.Lock()
	deadline := m.deadline
	m.mu.Unlock()
	if time.Until(deadline) <= 0 || m.ctx.Err() != nil {
		return flowRun{}, Connection{}, errors.New("execution lease expired")
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	f, c, err := m.authority(callCtx, fresh)
	if err != nil {
		return f, c, err
	}
	m.mu.Lock()
	valid := time.Until(m.deadline) > 0 && m.ctx.Err() == nil
	m.mu.Unlock()
	if !valid {
		return f, c, errors.New("execution lease expired")
	}
	return f, c, nil
}
func (m *managedExecution) persistLease(ctx context.Context, f flowRun, r tsync.ExecutionRecord, elapsed time.Duration) error {
	remaining, err := r.RemainingLease()
	if err != nil {
		return err
	}
	remaining -= elapsed
	if remaining < time.Second {
		return errors.New("execution lease has no safe remaining time")
	}
	if r.RequestID != f.ID || r.TaskKey != f.Snapshot.Key || r.AccountID != f.AccountID || r.DeviceID != f.Claim.DeviceID || r.TriggerKey != f.Trigger || r.Versions.Input != f.Input || r.Versions.Assignment != f.Assignment || fmt.Sprint(r.Versions.Generation) != f.Automation {
		return errors.New("execution lease identity changed")
	}
	if !completionAuthority(f, r) {
		return errors.New("execution has no running authority")
	}
	if r.Mode != f.Claim.Mode || r.Purpose != f.Claim.Purpose || r.Scope.FullGoal != f.Claim.Scope.FullGoal || !slices.Equal(r.Scope.Objectives, f.Claim.Scope.Objectives) {
		return errors.New("execution scope changed")
	}
	raw, _ := json.Marshal(r)
	now := m.runtime.service.now()
	result, err := m.runtime.service.db.ExecContext(ctx, `UPDATE task_flow_runs SET execution_id=?,record_json=?,lease_owner=?,deadline=?,state='leased',error_code='',updated_at=? WHERE request_id=? AND state IN ('claiming','leased','running','recovering','lease_lost','needs_reconciliation') AND EXISTS(SELECT 1 FROM task_runtime_leases WHERE lease_key='sync' AND owner_id=? AND expires_at>?)`, r.ExecutionID, string(raw), m.owner, now.Add(remaining).UnixMilli(), now.UnixMilli(), m.id, m.owner, now.UnixMilli())
	if err == nil {
		err = requireOne(result)
	}
	if err == nil {
		m.record = r
		m.deadline = time.Now().Add(remaining)
		m.renewAt = time.Now().Add(remaining / 2)
	}
	return err
}
func (m *managedExecution) acquire() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, c, err := m.authority(m.ctx, true)
	if err != nil {
		return err
	}
	client, err := m.runtime.service.executionClient(c)
	if err != nil {
		return err
	}
	// claimId and the exact body were committed before any network request.
	r, err := client.ClaimExecutionWithKey(m.ctx, f.Snapshot.Key, f.Claim, f.ClaimID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// A response confirms acquisition identity but grants no local authority
	// until a new heartbeat succeeds. Preserve it for reconciliation/recovery.
	if _, err = m.runtime.service.db.ExecContext(m.ctx, `UPDATE task_flow_runs SET execution_id=?,record_json=? WHERE request_id=? AND state IN ('claiming','leased','running','recovering','lease_lost','needs_reconciliation')`, r.ExecutionID, string(raw), f.ID); err != nil {
		return err
	}
	if !completionAuthority(f, r) {
		_, err = m.runtime.service.db.ExecContext(m.ctx, `UPDATE task_flow_runs SET state=?,error_code='remote_execution_stopped',deadline=0,lease_owner='' WHERE request_id=?`, string(r.Status), f.ID)
		if err != nil {
			return err
		}
		return errors.New("remote execution has no running authority")
	}
	// Never use the possibly replayed claim's timestamps as live authority.
	command, err := m.command(m.ctx, "heartbeat", struct {
		Fence tsync.Fence `json:"fence"`
	}{r.Fence})
	if err != nil {
		return err
	}
	start := time.Now()
	claimFence := r.Fence
	r, err = client.HeartbeatExecution(m.ctx, r.ExecutionID, r.Fence, command)
	if err != nil {
		return err
	}
	if r.Fence != claimFence {
		return errors.New("execution fence changed")
	}
	return m.persistLease(m.ctx, f, r, time.Since(start))
}
func (m *managedExecution) heartbeat() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, c, err := m.authority(m.ctx, false)
	if err != nil {
		return err
	}
	if time.Until(m.deadline) <= 0 {
		return errors.New("execution lease expired")
	}
	client, err := m.runtime.service.executionClient(c)
	if err != nil {
		return err
	}
	id, err := m.command(m.ctx, "heartbeat", struct {
		Fence tsync.Fence `json:"fence"`
	}{m.record.Fence})
	if err != nil {
		return err
	}
	// A fresh command ID is always persisted. Lost heartbeat responses never
	// extend the local deadline, even if the server renewed its copy.
	callCtx, cancel := context.WithDeadline(m.ctx, m.deadline)
	defer cancel()
	start := time.Now()
	r, err := client.HeartbeatExecution(callCtx, m.record.ExecutionID, m.record.Fence, id)
	if err != nil {
		return err
	}
	if r.Fence != m.record.Fence {
		return errors.New("execution fence changed")
	}
	return m.persistLease(m.ctx, f, r, time.Since(start))
}
func (m *managedExecution) guard(done chan<- error) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-m.ctx.Done():
			done <- m.ctx.Err()
			return
		case <-tick.C:
			if _, _, err := m.authority(m.ctx, false); err != nil {
				m.cancel()
				done <- err
				return
			}
			m.mu.Lock()
			left := time.Until(m.deadline)
			renew := time.Now().After(m.renewAt)
			m.mu.Unlock()
			if left <= 0 {
				m.cancel()
				done <- errors.New("execution lease expired")
				return
			}
			// Refresh early enough to stop promptly through network loss. The bounded
			// heartbeat context cannot outlive the previous lease.
			if renew {
				if err := m.heartbeat(); err != nil {
					m.cancel()
					done <- err
					return
				}
			}
		}
	}
}
func (m *managedExecution) coordinate() {
	s := m.runtime.service
	done, err := m.observeCompleted()
	if done {
		m.acquireErr = err
		close(m.ready)
		return
	}
	if err == nil {
		err = m.acquire()
	}
	m.acquireErr = err
	close(m.ready)
	if err != nil {
		m.lose(err)
		return
	}
	guardDone := make(chan error, 1)
	go m.guard(guardDone)
	defer func() { m.cancel(); <-guardDone }()
	f, _, err := m.authority(m.ctx, true)
	if err != nil {
		m.lose(err)
		return
	}
	if f.Prepared != "" {
		if err = m.finishPrepared(m.ctx); err != nil {
			m.lose(err)
		}
		return
	}
	q := core.DefaultQueueSpec()
	q.ResourceKeys = []string{"business:" + f.ConnectionID + ":" + f.Key}
	switch f.Snapshot.Priority {
	case 1:
		q.Priority = core.PriorityLow
	case 3, 4:
		q.Priority = core.PriorityHigh
	}
	req := core.ExecutionRequest{RequestID: f.ID, ConnectionID: f.ConnectionID, AccountID: f.AccountID, TaskKey: f.Key, TriggerKey: f.Trigger, InputRevision: f.Input, Purpose: "execute", ApprovalMode: string(f.Policy.Mode)}
	project, err := m.runtime.boundProject(m.ctx, f)
	if err != nil {
		m.lose(err)
		return
	}
	published, err := m.runtime.core.EnqueueRequest(m.ctx, req, core.TaskInput{Title: f.Snapshot.Title, Intent: f.Snapshot.Description, Project: project}, q)
	if err != nil {
		m.lose(err)
		return
	}
	for m.ctx.Err() == nil {
		f, err = s.flow(m.ctx, m.id)
		if err != nil {
			m.lose(err)
			return
		}
		if f.Prepared != "" {
			if err = m.finishPrepared(m.ctx); err != nil {
				m.lose(err)
			}
			return
		}
		var state string
		if s.db.QueryRowContext(m.ctx, `SELECT state FROM tasks WHERE task_id=?`, published.Task.TaskID).Scan(&state) == nil && (state == core.StateFailed || state == core.StateCancelled || state == core.StateExpired) {
			m.lose(errors.New("preparation did not complete"))
			return
		}
		if wait(m.ctx, 200*time.Millisecond) != nil {
			break
		}
	}
	m.lose(m.ctx.Err())
}
func (m *managedExecution) release(reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, c, err := m.authority(m.ctx, true)
	if err != nil {
		return err
	}
	if time.Until(m.deadline) <= 0 {
		return errors.New("execution lease expired")
	}
	client, err := m.runtime.service.executionClient(c)
	if err != nil {
		return err
	}
	body := struct {
		Fence  tsync.Fence `json:"fence"`
		Reason string      `json:"reason"`
	}{m.record.Fence, reason}
	id, err := m.command(m.ctx, "release", body)
	if err != nil {
		return err
	}
	_, err = client.ReleaseExecution(m.ctx, m.record.ExecutionID, m.record.Fence, reason, id)
	if err != nil {
		return err
	}
	_, err = m.runtime.service.db.ExecContext(m.ctx, `UPDATE task_flow_runs SET state=?,deadline=0,lease_owner='',updated_at=? WHERE request_id=? AND lease_owner=?`, reason, m.runtime.service.now().UnixMilli(), f.ID, m.owner)
	m.deadline = time.Time{}
	return err
}
func (m *managedExecution) lose(cause error) {
	m.cancel()
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := m.runtime.service
	f, err := s.flow(cleanup, m.id)
	if err != nil {
		return
	}
	state, code := "lease_lost", "lease_unavailable"
	if errors.Is(cause, taskproviders.ErrAuthorizationChanged) || errors.Is(cause, taskproviders.ErrReconnect) {
		state, code = "paused", "authorization_changed"
	}
	if f.PrepareAttempt != "" && f.Prepared == "" {
		state, code = "needs_reconciliation", "preparation_interrupted"
	}
	if f.Record.Status == taskproviders.NeedsReconciliation {
		state, code = "needs_reconciliation", "remote_action_unresolved"
	}
	var unresolved, verified int
	_ = s.db.QueryRowContext(cleanup, `SELECT COUNT(*) FROM final_actions WHERE request_id=? AND state IN ('dispatching','unknown')`, f.ID).Scan(&unresolved)
	_ = s.db.QueryRowContext(cleanup, `SELECT COUNT(*) FROM final_actions WHERE request_id=? AND state='verified'`, f.ID).Scan(&verified)
	if unresolved > 0 {
		state, code = "needs_reconciliation", "final_action_unresolved"
	} else if verified > 0 {
		state, code = "recovering", "completed_pending_sync"
	}
	// Reacquisition retains the logical request but must use a new claim key.
	claimID := f.ClaimID
	if f.ExecutionID != "" {
		if next, err := util.UUIDv7(); err == nil {
			claimID = next
		}
	}
	_, _ = s.db.ExecContext(cleanup, `UPDATE task_flow_runs SET state=?,error_code=?,deadline=0,lease_owner='',claim_id=?,next_at=?,updated_at=? WHERE request_id=? AND state IN ('claiming','leased','running','recovering','lease_lost','needs_reconciliation')`, state, code, claimID, s.now().Add(30*time.Second).UnixMilli(), s.now().UnixMilli(), m.id)
}

func (e *executionRuntime) Prepare(ctx context.Context, t core.Task) (string, error) {
	var id string
	if err := e.service.db.QueryRowContext(ctx, `SELECT request_id FROM execution_links WHERE task_id=?`, t.TaskID).Scan(&id); err != nil {
		return "", err
	}
	var m *managedExecution
	for m == nil {
		e.mu.Lock()
		m = e.runs[id]
		e.mu.Unlock()
		if m != nil {
			break
		}
		f, err := e.service.flow(ctx, id)
		if err != nil {
			return "", err
		}
		switch f.State {
		case "claiming", "leased", "running", "recovering", "lease_lost":
		default:
			return "", errors.New("business execution is stopped: " + f.State)
		}
		if err := wait(ctx, 50*time.Millisecond); err != nil {
			return "", err
		}
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-m.ready:
	}
	if m.acquireErr != nil {
		return "", m.acquireErr
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	f, _, err := m.authority(workCtx, true)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	valid := time.Until(m.deadline) > 0
	m.mu.Unlock()
	if !valid {
		return "", errors.New("execution lease expired")
	}
	if f.Prepared != "" {
		return f.Prepared, nil
	}
	return e.prepare(workCtx, m, f, t)
}
