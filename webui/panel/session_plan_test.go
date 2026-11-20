// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func TestSessionAskPlanModePersistsUntilExplicitExit(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	var mu sync.Mutex
	var planned []bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		planned = append(planned, strings.Contains(string(body), "Request Mode: PLAN"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Which project should the plan cover?\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	if err := engine.SetModel(config.ModelConfig{APIType: config.APITypeOpenAI, BaseURL: model.URL, Model: "plan-test", NoAuth: true}); err != nil {
		t.Fatal(err)
	}
	store := sessions.NewStore(t.TempDir())
	sess, err := store.Create("plan mode")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermissionMode(sess.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	h := &handler{store: engine.TaskStore(), engine: engine, sessions: store, cfgMu: &sync.RWMutex{}, serviceCtx: context.Background(), activeAsks: map[string]sessionOperation{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/ask", h.sessionAsk)
	for index, mode := range []string{"plan", "", ""} {
		if index == 2 {
			if err := engine.Conversations(store).SetPlanning(context.Background(), sess.ID, false); err != nil {
				t.Fatal(err)
			}
		}
		rr := httptest.NewRecorder()
		body := fmt.Sprintf(`{"prompt":"Prepare a staged project roadmap; ask which project first.","mode":%q}`, mode)
		mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/ask", strings.NewReader(body)))
		if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "event: error") {
			t.Fatalf("mode %q: %d %s", mode, rr.Code, rr.Body.String())
		}
		mu.Lock()
		if len(planned) == 0 || planned[len(planned)-1] != (index < 2) {
			t.Errorf("mode %q: model planning lenses %v", mode, planned)
		}
		mu.Unlock()
	}
	current, err := store.Get(sess.ID)
	if err != nil || current.PermissionMode != "manual" {
		t.Fatalf("planning changed the stored permission: %+v, %v", current, err)
	}
}

func TestSessionAskRejectsUnknownRequestMode(t *testing.T) {
	h := &handler{}
	rr := httptest.NewRecorder()
	h.sessionAsk(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"Make a plan","mode":"plna"}`)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown request mode") {
		t.Fatalf("unknown mode: %d %s", rr.Code, rr.Body.String())
	}
}
