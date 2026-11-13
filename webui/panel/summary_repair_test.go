// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func TestSummaryRepairRestoresOnlyProvenOrphan(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	if err := f.h.store.RecordEvent(ctx, f.task.TaskID, evSessionSummary, map[string]string{"session_id": f.sessionID}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.h.auditSessionSummaries(ctx, f.sessionID, false)
	if err != nil || len(rows) != 1 || rows[0].Status != "orphan" {
		t.Fatalf("audit %+v %v", rows, err)
	}
	f.assertTurns(t, 0) // read-only audit
	rows, err = f.h.auditSessionSummaries(ctx, f.sessionID, true)
	if err != nil || rows[0].Status != "repaired" {
		t.Fatalf("repair %+v %v", rows, err)
	}
	f.assertTurns(t, 1)
	rows, err = f.h.auditSessionSummaries(ctx, f.sessionID, true)
	if err != nil || rows[0].Status != "present" {
		t.Fatalf("replay %+v %v", rows, err)
	}
	f.assertTurns(t, 1)
}

func TestSummaryRepairPreservesAmbiguousLegacyAnswer(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	if err := f.h.store.RecordEvent(ctx, f.task.TaskID, evSessionSummary, map[string]string{"session_id": f.sessionID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.sessions.AppendTurn(f.sessionID, sessions.Turn{Role: "assistant", Kind: "answer", Ref: f.task.TaskID, Text: "historical conclusion"}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.h.auditSessionSummaries(ctx, f.sessionID, true)
	if err != nil || rows[0].Status != "ambiguous" {
		t.Fatalf("audit %+v %v", rows, err)
	}
	f.assertTurns(t, 1)
}

func TestSummaryRepairDoesNotResurrectCompactedResults(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	ctx := context.Background()
	f.h.finalizeSessionSweep(ctx)
	if _, err := f.h.sessions.AppendTurns(f.sessionID, sessions.Turn{Role: "user", Text: "new question"}, sessions.Turn{Role: "assistant", Text: "new answer"}, sessions.Turn{Role: "user", Text: "latest"}, sessions.Turn{Role: "assistant", Text: "response"}); err != nil {
		t.Fatal(err)
	}
	compacted, err := f.h.sessions.CompactHistory(ctx, f.sessionID, 25, func(context.Context, []sessions.Turn) (string, error) { return "Earlier results summarized.", nil })
	if err != nil || len(compacted.CompactedTurnKeys) == 0 {
		t.Fatalf("compaction: %+v %v", compacted, err)
	}
	rows, err := f.h.auditSessionSummaries(ctx, f.sessionID, true)
	if err != nil || len(rows) != 1 || rows[0].Status != "present" {
		t.Fatalf("unsafe repair: %+v %v", rows, err)
	}
	f.assertTurns(t, len(compacted.Turns))
}

func TestSummaryRecoveryRoutesRemainAuthenticated(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	h := New(Deps{Store: f.h.store, Sessions: f.h.sessions, StaticDir: t.TempDir(), Token: testToken})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/sessions/" + f.sessionID + "/summary-audit"},
		{http.MethodPost, "/api/sessions/" + f.sessionID + "/summary-repair"},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(route.method, route.path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", route.path, rr.Code)
		}
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(route.method, route.path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("authenticated %s: %d %s", route.path, rr.Code, rr.Body.String())
		}
	}
}
