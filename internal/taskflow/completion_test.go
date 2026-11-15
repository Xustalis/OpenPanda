// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskflow/finalaction"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
)

type completionFixture struct {
	s           *Service
	e           *executionRuntime
	c           Connection
	f           flowRun
	record      tsync.ExecutionRecord
	events      map[string]string
	statuses    []string
	deliveries  []tsync.Delivery
	failStatus  string
	failCount   int
	late        []tsync.ExecutionEvent
	releases    []string
	loseRelease bool
}

func completionTest(t *testing.T, mode taskproviders.ApprovalMode) *completionFixture {
	t.Helper()
	s, e, c, p, wire := automationTest(t, true)
	c.Scopes = append(c.Scopes, "delivery:write", "delivery:report-decision")
	c.Spaces[0].Scopes = c.Scopes
	scopes, _ := json.Marshal(c.Scopes)
	spaces, _ := json.Marshal(c.Spaces)
	if _, err := s.db.Exec(`UPDATE task_connections SET scopes_json=?,spaces_json=?`, string(scopes), string(spaces)); err != nil {
		t.Fatal(err)
	}
	p.FinalAction = true
	p.Capabilities = append(p.Capabilities, "file_publish")
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.Publication = &FilePublication{Directory: directory, Filename: "result-{request}.txt", FullGoal: true}
	p.Mode = mode
	wire.ApprovalMode = mode
	snapshot, err := wire.Snapshot(1, "Space")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot)
	if _, err = s.db.Exec(`UPDATE task_snapshots SET snapshot_json=?`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPolicy(context.Background(), c.ID, "s", p); err != nil {
		t.Fatal(err)
	}
	f := discoverTestFlow(t, s, e)
	x := &completionFixture{s: s, e: e, c: c, f: f, events: map[string]string{}}
	x.record = leaseRecord(f)
	s.signConsent = func(_ context.Context, _ Connection, p tsync.ConsentPayload) (tsync.SignedProof, error) {
		return tsync.SignedProof{Payload: p, Signature: "fixture"}, nil
	}
	automationServer(t, s, c, wire, func(w http.ResponseWriter, r *http.Request) { x.serve(t, w, r) })
	if _, err = s.db.Exec(`UPDATE task_flow_runs SET prepared_json='Prepared file content',prepare_attempt='attempt' WHERE request_id=?`, f.ID); err != nil {
		t.Fatal(err)
	}
	return x
}
func (x *completionFixture) worker(t *testing.T) *managedExecution {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := &managedExecution{runtime: x.e, id: x.f.ID, owner: "owner", ctx: ctx, cancel: cancel}
	if err := m.acquire(); err != nil {
		t.Fatal(err)
	}
	return m
}
func (x *completionFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	event := r.Header.Get("Idempotency-Key")
	if old, ok := x.events[event]; ok && event != "" {
		var stored struct {
			Body   json.RawMessage `json:"body"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal([]byte(old), &stored)
		if string(stored.Body) != string(raw) {
			w.WriteHeader(409)
			return
		}
		_, _ = w.Write(stored.Result)
		return
	}
	var response any
	switch {
	case strings.HasSuffix(r.URL.Path, "/tasks/t/executions"):
		x.record.Fence.Counter++
		x.record.Status = taskproviders.Queued
		x.record.ServerTime = time.Now().UTC().Format(time.RFC3339Nano)
		until := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
		x.record.LeaseUntil = &until
		response = x.record
	case strings.HasSuffix(r.URL.Path, "/heartbeat"):
		response = x.record
	case strings.HasSuffix(r.URL.Path, "/release"):
		var body struct {
			Reason string      `json:"reason"`
			Fence  tsync.Fence `json:"fence"`
		}
		_ = json.Unmarshal(raw, &body)
		if body.Fence != x.record.Fence {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"lease_lost"}}`))
			return
		}
		x.releases = append(x.releases, event)
		x.record.Status = taskproviders.ExecutionState(body.Reason)
		x.record.LeaseUntil = nil
		response = x.record
	case strings.HasSuffix(r.URL.Path, "/events"):
		var v tsync.ExecutionEvent
		_ = json.Unmarshal(raw, &v)
		if x.record.LeaseUntil != nil && v.Fence == x.record.Fence {
			t.Error("late evidence sent under a current lease")
		}
		x.late = append(x.late, v)
		response = x.record
	case strings.HasSuffix(r.URL.Path, "/deliveries"):
		var v tsync.Delivery
		if json.Unmarshal(raw, &v) != nil {
			t.Error("invalid delivery")
		}
		if v.Fence != x.record.Fence {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"lease_lost"}}`))
			return
		}
		x.deliveries = append(x.deliveries, v)
		x.record.Status = taskproviders.AwaitingSubmit
		response = v
	case strings.HasSuffix(r.URL.Path, "/final-actions"):
		var v tsync.FinalAction
		_ = json.Unmarshal(raw, &v)
		if v.Status == x.failStatus && x.failCount > 0 {
			x.failCount--
			w.WriteHeader(503)
			return
		}
		x.statuses = append(x.statuses, v.Status)
		if v.Status == "dispatching" {
			x.record.Status = taskproviders.NeedsReconciliation
		}
		if (v.Status == "succeeded" || v.Status == "reconciled") && x.f.Policy.Mode == taskproviders.BeforeSubmit {
			x.record.Status = taskproviders.Succeeded
		}
		response = v
	case strings.HasSuffix(r.URL.Path, "/decisions"):
		var v tsync.DeliveryDecision
		_ = json.Unmarshal(raw, &v)
		p := v.ConsentProof.Payload
		response = tsync.DeliveryDecisionRecord{DecisionID: v.DecisionID, DeliveryID: p.DeliveryID, Revision: v.Revision, Decision: v.Decision, AccountID: p.AccountID, Completed: v.Decision == "accept", InputRevision: p.InputRevision, AssignmentRevision: p.AssignmentRevision, Generation: p.Generation, ScopeHash: p.ScopeHash, Reason: v.Reason, ConsentProof: v.ConsentProof}
	case strings.HasSuffix(r.URL.Path, "/executions/execution"):
		response = x.record
	default:
		t.Errorf("unexpected %s", r.URL.Path)
		w.WriteHeader(404)
		return
	}
	b, _ := json.Marshal(response)
	if event != "" {
		stored, _ := json.Marshal(map[string]any{"body": json.RawMessage(raw), "result": json.RawMessage(b)})
		x.events[event] = string(stored)
	}
	if strings.HasSuffix(r.URL.Path, "/release") && x.loseRelease {
		x.loseRelease = false
		w.WriteHeader(503)
		return
	}
	_, _ = w.Write(b)
}
func TestBeforeSubmitExactConsentAndCompletedReceipt(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	if err := m.finishPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	work, err := x.s.Work(context.Background(), x.f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if work.State != "awaiting_submit" || len(work.Actions) != 1 || len(x.statuses) != 0 {
		t.Fatalf("premature dispatch: %+v %v", work, x.statuses)
	}
	a := work.Actions[0]
	if err = x.s.ApproveAction(context.Background(), a.ID, "changed"); !errors.Is(err, taskproviders.ErrConflict) {
		t.Fatalf("changed consent: %v", err)
	}
	if err = x.s.ApproveAction(context.Background(), a.ID, a.Fingerprint); err != nil {
		t.Fatal(err)
	}
	m = x.worker(t)
	if err = m.finishPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	work, err = x.s.Work(context.Background(), x.f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if work.State != "succeeded" || work.Actions[0].Receipt == nil {
		t.Fatalf("not verified: %+v", work)
	}
	if string(bMustRead(t, filepath.Join(a.Intent.Directory, a.Intent.Filename))) != a.Intent.Content {
		t.Fatal("content mismatch")
	}
	if strings.Join(x.statuses, ",") != "prepared,dispatching,succeeded" {
		t.Fatal(x.statuses)
	}
	b, _ := json.Marshal(work)
	if strings.Contains(string(b), "fixture") || strings.Contains(string(b), "signature") {
		t.Fatal("private proof leaked")
	}
}
func bMustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestAfterExecutionAcceptanceIsRevisionBound(t *testing.T) {
	x := completionTest(t, taskproviders.AfterExecution)
	m := x.worker(t)
	if err := m.finishPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	work, err := x.s.Work(context.Background(), x.f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if work.State != "awaiting_acceptance" {
		t.Fatal(work.State)
	}
	d := work.Deliveries[0].Delivery
	if err = x.s.DecideDelivery(context.Background(), d.DeliveryID, d.Revision+1, "accept", ""); err == nil {
		t.Fatal("wrong revision accepted")
	}
	if err = x.s.DecideDelivery(context.Background(), d.DeliveryID, d.Revision, "accept", "reviewed"); err != nil {
		t.Fatal(err)
	}
	if err = x.s.DecideDelivery(context.Background(), d.DeliveryID, d.Revision, "accept", "reviewed"); err != nil {
		t.Fatal(err)
	}
	if err = x.s.DecideDelivery(context.Background(), d.DeliveryID, d.Revision, "accept", "changed"); !errors.Is(err, taskproviders.ErrConflict) {
		t.Fatalf("changed decision body: %v", err)
	}
	if err = x.s.DecideDelivery(context.Background(), d.DeliveryID, d.Revision, "return", ""); !errors.Is(err, taskproviders.ErrConflict) {
		t.Fatal(err)
	}
	if err = x.s.flushOutbox(context.Background(), "owner", x.f.ID); err != nil {
		t.Fatal(err)
	}
	work, err = x.s.Work(context.Background(), x.f.ID)
	if err != nil || work.State != "succeeded" {
		t.Fatalf("%+v %v", work, err)
	}
}
func TestLateReceiptSurvivesLostAuthorityAndNeverRepublishes(t *testing.T) {
	x := completionTest(t, taskproviders.AfterExecution)
	m := x.worker(t)
	x.failStatus = "succeeded"
	x.failCount = 1
	if err := m.finishPrepared(context.Background()); err == nil {
		t.Fatal("expected sync failure")
	}
	work, err := x.s.Work(context.Background(), x.f.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := work.Actions[0]
	if a.State != "verified" || a.Receipt == nil {
		t.Fatal("receipt not durable")
	}
	if _, err = x.s.db.Exec(`UPDATE task_connections SET state='reconnect'`); err != nil {
		t.Fatal(err)
	}
	receipt, err := finalaction.Reconcile(context.Background(), a.Intent)
	if err != nil {
		t.Fatal(err)
	}
	if err = x.s.saveReceipt(a, receipt, "reconciled"); err != nil {
		t.Fatal(err)
	}
	if _, err = x.s.db.Exec(`UPDATE task_connections SET state='connected'`); err != nil {
		t.Fatal(err)
	}
	if _, err = x.s.db.Exec(`UPDATE task_sync_outbox SET next_at=0`); err != nil {
		t.Fatal(err)
	}
	// An edited destination must remain untouched by a fact-only retry.
	if err = os.WriteFile(filepath.Join(a.Intent.Directory, a.Intent.Filename), []byte("edited later"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = x.s.flushOutbox(context.Background(), "owner", x.f.ID); err != nil {
		t.Fatal(err)
	}
	if string(bMustRead(t, filepath.Join(a.Intent.Directory, a.Intent.Filename))) != "edited later" {
		t.Fatal("sender reran work")
	}
}
func TestOutboxRebasesOnlyRejectedOldFenceWithArchivedBytes(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	f, c, err := m.checkpoint(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ensureDelivery(context.Background(), f, c); err != nil {
		t.Fatal(err)
	}
	original := scalar(t, x.s.db, `SELECT body_json FROM task_sync_outbox`)
	x.record.Fence.Counter++
	m.mu.Lock()
	err = m.persistLease(context.Background(), f, x.record, 0)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = x.s.flushOutbox(context.Background(), "owner", x.f.ID); err != nil {
		t.Fatal(err)
	}
	if scalar(t, x.s.db, `SELECT body_json FROM task_sync_event_history`) != original {
		t.Fatal("lost original event")
	}
	if len(x.deliveries) != 1 || x.deliveries[0].Fence != x.record.Fence {
		t.Fatal("stale fence")
	}
}
func TestExpiredApprovalCannotPublish(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	if err := m.finishPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	work, _ := x.s.Work(context.Background(), x.f.ID)
	a := work.Actions[0]
	if err := x.s.ApproveAction(context.Background(), a.ID, a.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.db.Exec(`UPDATE approval_records SET expires_at=?`, time.Now().Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	m = x.worker(t)
	if err := m.finishPrepared(context.Background()); err == nil {
		t.Fatal("expired consent dispatched")
	}
	if _, err := os.Stat(filepath.Join(a.Intent.Directory, a.Intent.Filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file exists: %v", err)
	}
}

func TestRecoveredReceiptReportsUnknownBeforeReconciled(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	if err := m.finishPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	w, _ := x.s.Work(context.Background(), x.f.ID)
	a := w.Actions[0]
	if err := x.s.ApproveAction(context.Background(), a.ID, a.Fingerprint); err != nil {
		t.Fatal(err)
	}
	m = x.worker(t)
	f, c, err := m.checkpoint(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	a, err = x.s.action(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"prepared", "dispatching"} {
		local := ""
		if status == "dispatching" {
			local = status
		}
		if err = m.queueAction(context.Background(), f, c, a, status, nil, local); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a crash after publication with dispatch reports still pending.
	if _, err = finalaction.Publish(context.Background(), a.Intent, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = x.e.recover(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	a, err = x.s.action(context.Background(), a.ID)
	if err != nil || a.State != "verified" || a.Receipt == nil {
		t.Fatalf("%+v %v", a, err)
	}
	m = x.worker(t)
	if err = m.finishPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(x.statuses, ","); got != "prepared,dispatching,unknown,reconciled" {
		t.Fatal(got)
	}
}

func TestApprovalRetryRetainsProofAndExpiry(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	if err := m.finishPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	w, _ := x.s.Work(context.Background(), x.f.ID)
	a := w.Actions[0]
	for i := 0; i < 2; i++ {
		if err := x.s.ApproveAction(context.Background(), a.ID, a.Fingerprint); err != nil {
			t.Fatal(err)
		}
		proof := scalar(t, x.s.db, `SELECT proof_json FROM final_actions`)
		if i == 0 {
			a.Proof = tsync.SignedProof{}
			if err := json.Unmarshal([]byte(proof), &a.Proof); err != nil {
				t.Fatal(err)
			}
		} else {
			var p tsync.SignedProof
			_ = json.Unmarshal([]byte(proof), &p)
			if p != a.Proof {
				t.Fatal("retry replaced consent proof")
			}
		}
	}
}

func TestRejectedOldFenceStaysPendingUntilNewOwnerLease(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	f, c, err := m.checkpoint(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ensureDelivery(context.Background(), f, c); err != nil {
		t.Fatal(err)
	}
	before := scalar(t, x.s.db, `SELECT body_json FROM task_sync_outbox`)
	x.record.Fence.Counter++
	if err = x.s.flushOutbox(context.Background(), "owner", x.f.ID); err == nil {
		t.Fatal("accepted stale fence")
	}
	if scalar(t, x.s.db, `SELECT state FROM task_sync_outbox`) != "pending" || scalar(t, x.s.db, `SELECT body_json FROM task_sync_outbox`) != before {
		t.Fatal("lost exact pending retry")
	}
}
