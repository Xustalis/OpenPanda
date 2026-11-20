// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func TestDeepHTTPExhaustedBudgetResumeIsAtomic(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("HTTP budget fixture")
	if err != nil {
		t.Fatal(err)
	}
	service := engine.Conversations(st)
	service.Close()
	ctx := context.Background()
	if err = service.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	budget := int64(10)
	g, err := service.Create(ctx, sess.ID, "preserve the full objective", &budget)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(filepath.Dir(counter), "panda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE conversation_goals SET tokens=10,status='budget_limited' WHERE id=?", g.ID); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Store: engine.TaskStore(), Engine: engine, Sessions: st, DB: db, Token: testToken, StaticDir: t.TempDir()})
	for _, budget := range []int{9, 10, 11} {
		body, _ := json.Marshal(map[string]any{"action": "resume", "goal_id": g.ID, "token_budget": budget})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/control", strings.NewReader(string(body))))
		want := 400
		if budget == 11 {
			want = 200
		}
		if rr.Code != want {
			t.Fatalf("budget %d: HTTP %d %s", budget, rr.Code, rr.Body.String())
		}
		state, err := service.State(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if budget <= 10 {
			if !state.Planning || state.Goal.Status != "budget_limited" || *state.Goal.TokenBudget != 10 {
				t.Fatalf("rejected control mutated state: %+v %+v", state, state.Goal)
			}
		} else if state.Planning || state.Goal.Status != "active" || *state.Goal.TokenBudget != 11 {
			t.Fatalf("valid resume: %+v %+v", state, state.Goal)
		}
	}
}
