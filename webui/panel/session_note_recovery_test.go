// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

func TestSessionAskRestoresNotesAfterRoundFailure(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "carry"
		if live {
			name = "live queue"
		}
		t.Run(name, func(t *testing.T) {
			f := newSummaryDeliveryFixture(t, false)
			engine, counter := newApprovalQueueEngine(t)
			roundDB, err := storage.Open(filepath.Join(filepath.Dir(counter), "panda.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { roundDB.Close() })
			f.h.store = engine.TaskStore()
			f.h.engine = engine
			f.h.cfgMu = &sync.RWMutex{}
			f.h.serviceCtx = context.Background()
			note, ok := f.h.pushSteer(f.sessionID, "keep this queued message", true)
			if !ok {
				t.Fatal("note was not accepted")
			}
			if !live {
				f.h.parkLeftovers(f.sessionID, nil)
			}
			if _, err := roundDB.Exec(`CREATE TRIGGER fail_note_round BEFORE INSERT ON ask_rounds
				BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END`); err != nil {
				t.Fatal(err)
			}
			ask := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"","resume_queued":true}`))
				r.SetPathValue("id", f.sessionID)
				w := httptest.NewRecorder()
				f.h.sessionAsk(w, r)
				return w
			}
			failed := ask()
			if !strings.Contains(failed.Body.String(), "create round failed") {
				t.Fatalf("did not reach injected failure: %s", failed.Body.String())
			}
			if _, err := roundDB.Exec(`DROP TRIGGER fail_note_round`); err != nil {
				t.Fatal(err)
			}
			pending := f.h.listSteer(f.sessionID)
			if len(pending) != 1 || pending[0] != note {
				t.Fatalf("failed start lost or changed note: %+v, want %+v", pending, note)
			}
			resumed := ask()
			if resumed.Code != http.StatusOK {
				t.Fatalf("retry failed: %d %s", resumed.Code, resumed.Body.String())
			}
			// The entry-model fixture may reject its stub answer. Once the
			// user turn was saved, even a later model error must not replay it.
			sess, err := f.h.sessions.Get(f.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, turn := range sess.Turns {
				if turn.Role == "user" && turn.Text == note.text {
					count++
				}
			}
			if count != 1 || len(f.h.listSteer(f.sessionID)) != 0 {
				t.Fatalf("recovered note must be persisted once and consumed: turns=%d pending=%+v", count, f.h.listSteer(f.sessionID))
			}
		})
	}
}

type noteFailureWriter struct {
	*httptest.ResponseRecorder
	onOperation func()
}

func (w *noteFailureWriter) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), "event: operation\n") && w.onOperation != nil {
		w.onOperation()
		w.onOperation = nil
	}
	return w.ResponseRecorder.Write(p)
}

func TestSessionAskRestoresNotesAfterOperationFailure(t *testing.T) {
	f := newSummaryDeliveryFixture(t, false)
	engine, _ := newApprovalQueueEngine(t)
	f.h.engine = engine
	f.h.cfgMu = &sync.RWMutex{}
	note, _ := f.h.pushSteer(f.sessionID, "preserve original note metadata", false)
	f.h.parkLeftovers(f.sessionID, nil)
	path := filepath.Join(f.root, f.sessionID+".json")
	backup := path + ".unavailable"
	w := &noteFailureWriter{ResponseRecorder: httptest.NewRecorder(), onOperation: func() {
		if err := os.Rename(path, backup); err != nil {
			t.Fatal(err)
		}
	}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"resume_queued":true}`))
	r.SetPathValue("id", f.sessionID)
	f.h.sessionAsk(w, r)
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "save operation failed") {
		t.Fatalf("operation failure not reached: %s", w.Body.String())
	}
	if got := f.h.listSteer(f.sessionID); len(got) != 1 || got[0] != note {
		t.Fatalf("operation failure lost the note: %+v", got)
	}
}

func TestPendingNotesRollbackKeepsOrderAndDeletion(t *testing.T) {
	const id = "session"
	h := &handler{}
	first, _ := h.pushSteer(id, "first", false)
	claim1 := h.claimPendingNotes(id, true)
	second, _ := h.pushSteer(id, "second", true)
	claim2 := h.claimPendingNotes(id, true)
	h.settlePendingNotes(claim1, false)
	h.settlePendingNotes(claim2, false) // later claim rolls back last
	h.settlePendingNotes(claim1, false) // duplicate cleanup is harmless
	got := h.listSteer(id)
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("rollback reordered or duplicated input: %+v", got)
	}
	committed := h.claimPendingNotes(id, true)
	h.settlePendingNotes(committed, true)
	h.settlePendingNotes(committed, false)
	if len(h.listSteer(id)) != 0 || len(h.steerClaims) != 0 {
		t.Fatal("committed notes were restored or claim leaked")
	}
	h.pushSteer(id, "deleted", false)
	deleted := h.claimPendingNotes(id, true)
	h.dropSteer(id)
	h.settlePendingNotes(deleted, false)
	if len(h.listSteer(id)) != 0 || len(h.steerClaims) != 0 {
		t.Fatal("failed start resurrected deleted session notes")
	}
}
