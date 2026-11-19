// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func TestSessionDeliveryExcerpt(t *testing.T) {
	prose := "Added the animation.\n\n" + strings.Repeat("车", 2000)
	encoded, err := json.Marshal(map[string]string{"answer": prose, "stdout": "fallback log"})
	if err != nil {
		t.Fatal(err)
	}
	got := sessionDeliveryExcerpt(core.StateDone, string(encoded))
	if len([]rune(got)) != 1800 || !strings.HasPrefix(prose, got) {
		t.Fatalf("excerpt = %d runes, want a valid 1800-rune prefix", len([]rune(got)))
	}
	if got := sessionDeliveryExcerpt(core.StateReview, string(encoded)); got != "" {
		t.Fatal("an unsettled task should not advertise a report excerpt")
	}
	if got := sessionDeliveryExcerpt(core.StateFailed, `{"failed":"unable to start"}`); got != "unable to start" {
		t.Fatalf("failed report = %q", got)
	}
}

func TestDeliverySeparatesReviewWaitAndFinalDecision(t *testing.T) {
	task := core.Task{State: core.StateDone, UpdatedAt: 2112}
	events := []core.Event{
		{Type: core.EvAccept, TS: 10},
		{Type: core.EvExecAgentStart, TS: 11},
		{Type: core.EvSupervise, TS: 589, DataJSON: `{"status":"review","reason":"old objective assessment error"}`},
		{Type: core.EvReview, TS: 589, DataJSON: `{"reason":"awaiting approval"}`},
		{Type: core.EvReview, TS: 2108, DataJSON: `{"approved":true,"resumed":false}`},
	}
	got := summarizeDelivery(task, events)
	if got.status != "manual" || got.judge != "" || got.executionMS != 579000 || got.approvalWaitMS != 1519000 {
		t.Fatalf("bad delivery: %+v", got)
	}
	task.State = core.StateFailed
	if got := summarizeDelivery(task, events); got.status != "unverified" {
		t.Fatalf("failed task marked accepted: %+v", got)
	}
}

func TestDeliveryApprovalToResumeIsNotAcceptance(t *testing.T) {
	task := core.Task{State: core.StateDone, UpdatedAt: 110}
	events := []core.Event{
		{Type: core.EvReview, TS: 10, DataJSON: `{"reason":"authorization required"}`},
		{Type: core.EvReview, TS: 100, DataJSON: `{"approved":true,"resumed":true}`},
		{Type: core.EvExecAgentStart, TS: 105},
	}
	got := summarizeDelivery(task, events)
	if got.status != "unverified" || got.executionMS != 5000 || got.approvalWaitMS != 90000 {
		t.Fatalf("bad delivery: %+v", got)
	}
}

// Runs only against an explicitly supplied copy of the historical fixture;
// it starts no scheduler, saves no turns, and never executes old tasks.
func TestHistoricalSessionDeliveryCopy(t *testing.T) {
	root := os.Getenv("PANDA_HISTORICAL_SESSION_COPY")
	if root == "" {
		t.Skip("opt-in historical snapshot")
	}
	db, err := storage.Open(filepath.Join(root, "panda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := sessions.NewStore(filepath.Join(root, "sessions"))
	sess, err := store.Get("01a1ee4d457171149d989f3a3bdc0e2c")
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{store: core.NewTaskStore(db, nil), sessions: store}
	h.projectSessionDeliveries(context.Background(), sess)
	for _, turn := range sess.Turns {
		if turn.Kind != "answer" || turn.Ref != "01a1ee4e-1b8d-70ac-8b5d-f3b015561334" {
			continue
		}
		if turn.DeliveryStatus != "manual" || turn.Verdict != "" || turn.DurationMS != 579000 || turn.ApprovalWaitMS != 1519000 ||
			strings.Contains(turn.Text, "invalid or duplicate assessment") || !strings.Contains(turn.Delivery, "pelican-bike.html") {
			t.Fatalf("legacy read projection: status=%s verdict=%q execution=%d wait=%d", turn.DeliveryStatus, turn.Verdict, turn.DurationMS, turn.ApprovalWaitMS)
		}
		return
	}
	t.Fatal("historical task delivery not found")
}
