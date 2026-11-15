// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskflow/finalaction"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func pendingDelivery(t *testing.T, x *completionFixture, m *managedExecution) (flowRun, Connection, WorkDelivery) {
	t.Helper()
	f, c, err := m.checkpoint(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	d, err := m.ensureDelivery(context.Background(), f, c)
	if err != nil {
		t.Fatal(err)
	}
	return f, c, d
}

func TestPauseReleasesBeforeArchivingPrivatePendingReports(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	_, _, d := pendingDelivery(t, x, m)
	original := scalar(t, x.s.db, `SELECT body_json FROM task_sync_outbox WHERE kind='delivery'`)
	if err := x.s.PauseWork(context.Background(), x.f.ID); err != nil {
		t.Fatal(err)
	}
	if err := x.s.flushOutbox(context.Background(), "owner", x.f.ID); err != nil {
		t.Fatal(err)
	}
	if len(x.releases) != 1 || len(x.deliveries) != 0 || len(x.late) != 1 {
		t.Fatalf("release=%v delivery=%v late=%v", x.releases, x.deliveries, x.late)
	}
	if strings.Contains(x.late[0].Summary, "Prepared file content") {
		t.Fatal("private draft leaked into evidence")
	}
	if got := scalar(t, x.s.db, `SELECT body_json FROM task_sync_event_history WHERE kind='delivery'`); got != original {
		t.Fatal("original report changed")
	}
	stored, err := x.s.delivery(context.Background(), d.Delivery.DeliveryID, d.Delivery.Revision)
	if err != nil || stored.SyncState != "archived" {
		t.Fatalf("%+v %v", stored, err)
	}
	w, _ := x.s.Work(context.Background(), x.f.ID)
	if _, err = os.Stat(filepath.Join(w.Actions[0].Intent.Directory, w.Actions[0].Intent.Filename)); !os.IsNotExist(err) {
		t.Fatalf("paused action ran: %v", err)
	}
}

func TestExpiredPauseFenceOnlyReacquiresToRelease(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	pendingDelivery(t, x, m)
	x.record.Fence.Counter++ // the local worker's issued fence has expired
	if err := x.s.PauseWork(context.Background(), x.f.ID); err != nil {
		t.Fatal(err)
	}
	if err := x.s.flushOutbox(context.Background(), "owner", x.f.ID); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, x.s.db, `SELECT deadline FROM task_flow_runs`); got != "0" {
		t.Fatal("pause recovery granted execution authority", got)
	}
	if got := scalar(t, x.s.db, `SELECT state FROM task_flow_commands WHERE kind='pause_claim'`); got != "sent" {
		t.Fatal(got)
	}
	if len(x.releases) != 1 || len(x.statuses) != 0 || len(x.late) != 1 || x.record.LeaseUntil != nil {
		t.Fatal("pause recovery resumed work")
	}
}

func TestLostReleaseResponseRetriesOriginalBytes(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	pendingDelivery(t, x, m)
	if err := x.s.PauseWork(context.Background(), x.f.ID); err != nil {
		t.Fatal(err)
	}
	before := scalar(t, x.s.db, `SELECT body_json FROM task_sync_outbox WHERE kind='release'`)
	x.loseRelease = true
	if err := x.s.flushOutbox(context.Background(), "owner", x.f.ID); err == nil {
		t.Fatal("expected ambiguous response")
	}
	if got := scalar(t, x.s.db, `SELECT body_json FROM task_sync_outbox WHERE kind='release'`); got != before {
		t.Fatal("ambiguous release rewritten")
	}
	if _, err := x.s.db.Exec(`UPDATE task_sync_outbox SET next_at=0`); err != nil {
		t.Fatal(err)
	}
	if err := x.s.flushOutbox(context.Background(), "owner", x.f.ID); err != nil {
		t.Fatal(err)
	}
	if len(x.releases) != 1 {
		t.Fatal("release executed twice")
	}
}

func TestLocalPauseAloneCannotArchiveUnderCurrentFence(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	f, c, _ := pendingDelivery(t, x, m)
	if _, err := x.s.db.Exec(`UPDATE task_flow_runs SET state='paused',deadline=0,lease_owner=''`); err != nil {
		t.Fatal(err)
	}
	if err := x.s.archiveStoppedEvents(context.Background(), "owner", f, c); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, x.s.db, `SELECT kind FROM task_sync_outbox`); got != "delivery" {
		t.Fatal("unreleased current fence archived", got)
	}
	w, _ := x.s.Work(context.Background(), x.f.ID)
	a := w.Actions[0]
	if err := x.s.ApproveAction(context.Background(), a.ID, a.Fingerprint); err == nil {
		t.Fatal("paused action approved")
	}
	if err := m.queueAction(context.Background(), f, c, a, "dispatching", nil, "dispatching"); err == nil {
		t.Fatal("paused action dispatched")
	}
}

func TestReceiptArrivingAfterPauseIsDurableHashOnlyEvidence(t *testing.T) {
	x := completionTest(t, taskproviders.BeforeSubmit)
	m := x.worker(t)
	pendingDelivery(t, x, m)
	w, _ := x.s.Work(context.Background(), x.f.ID)
	a := w.Actions[0]
	if _, err := x.s.db.Exec(`UPDATE final_actions SET state='dispatching'`); err != nil {
		t.Fatal(err)
	}
	if err := x.s.PauseWork(context.Background(), x.f.ID); err != nil {
		t.Fatal(err)
	}
	// The physical publication passed its checkpoint immediately before pause.
	receipt, err := finalaction.Publish(context.Background(), a.Intent, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = x.s.saveReceipt(a, receipt, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err = x.s.flushOutbox(context.Background(), "owner", x.f.ID); err != nil {
		t.Fatal(err)
	}
	if err = x.s.flushOutbox(context.Background(), "owner", x.f.ID); err != nil {
		t.Fatal(err)
	}
	if len(x.late) != 2 {
		t.Fatal("receipt evidence missing or duplicated", len(x.late))
	}
	for _, v := range x.late {
		if strings.Contains(v.Summary, a.Intent.Content) || strings.Contains(v.Summary, a.Intent.Directory) {
			t.Fatal("private receipt content leaked")
		}
	}
	w, err = x.s.Work(context.Background(), x.f.ID)
	if err != nil || w.Actions[0].Receipt == nil || w.State != "paused" {
		t.Fatalf("%+v %v", w, err)
	}
}

func TestExpiredDecisionRequiresExplicitReconsentAfterRejection(t *testing.T) {
	x := completionTest(t, taskproviders.AfterExecution)
	m := x.worker(t)
	if err := m.finishPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	w, _ := x.s.Work(context.Background(), x.f.ID)
	d := w.Deliveries[0].Delivery
	if err := x.s.DecideDelivery(context.Background(), d.DeliveryID, d.Revision, "accept", "reviewed"); err != nil {
		t.Fatal(err)
	}
	old := scalar(t, x.s.db, `SELECT proof_json FROM work_delivery_decisions`)
	now := x.s.now().Add(6 * time.Minute)
	x.s.now = func() time.Time { return now }
	if err := x.s.DecideDelivery(context.Background(), d.DeliveryID, d.Revision, "accept", "reviewed"); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, x.s.db, `SELECT proof_json FROM work_delivery_decisions`); got != old {
		t.Fatal("ambiguous decision renewed")
	}
	if _, err := x.s.db.Exec(`UPDATE task_sync_outbox SET state='conflict',error_code='decision_rejected' WHERE kind='decision'`); err != nil {
		t.Fatal(err)
	}
	if err := x.s.DecideDelivery(context.Background(), d.DeliveryID, d.Revision, "accept", "reviewed"); err != nil {
		t.Fatal(err)
	}
	var prior, next map[string]any
	_ = json.Unmarshal([]byte(old), &prior)
	_ = json.Unmarshal([]byte(scalar(t, x.s.db, `SELECT proof_json FROM work_delivery_decisions`)), &next)
	if prior["decisionId"] == next["decisionId"] {
		t.Fatal("explicit decision not renewed")
	}
	if got := scalar(t, x.s.db, `SELECT body_json FROM task_sync_event_history WHERE kind='decision'`); got != old {
		t.Fatal("old proof lost")
	}
}
