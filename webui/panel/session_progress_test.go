// SPDX-License-Identifier: AGPL-3.0-or-later
package panel

import (
	"bufio"
	"context"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAskProgressSnapshotIsConcurrentAndSeparate(t *testing.T) {
	var p askProgress
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				p.delta("t", false)
				p.delta("r", true)
				p.status(map[string]any{"kind": "model", "text": "waiting"})
				p.task("t1")
				_ = p.snapshot(sessions.Operation{ID: "op", Status: "running"})
			}
		})
	}
	wg.Wait()
	p.usage(123, true)
	op := p.snapshot(sessions.Operation{ID: "op", Status: "running"})
	if len(op.Text) != 400 || len(op.Thought) != 400 || op.ID != "op" || op.TaskID != "t1" || op.LastActivity.IsZero() || op.Tokens != 123 || !op.Estimated {
		t.Fatalf("snapshot lost data: %+v", op)
	}
}

// Real HTTP disconnect -> durable live checkpoint -> explicit cancellation.
// The provider is local SSE; no user credentials/daemon/storage are involved.
func TestSessionAskDisconnectRestoresProgressAndStop(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"provider summary\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer model.Close()
	if err := engine.SetModel(config.ModelConfig{APIType: config.APITypeOpenAI, BaseURL: model.URL, Model: "test", NoAuth: true}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := sessions.NewStore(root)
	sess, _ := store.Create("reconnect smoke")
	h := &handler{store: engine.TaskStore(), engine: engine, sessions: store, cfgMu: &sync.RWMutex{}, serviceCtx: context.Background(), activeAsks: map[string]sessionOperation{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/ask", h.sessionAsk)
	mux.HandleFunc("POST /api/sessions/{id}/cancel", h.sessionCancel)
	panel := httptest.NewServer(mux)
	defer panel.Close()
	resp, err := http.Post(panel.URL+"/api/sessions/"+sess.ID+"/ask", "application/json", strings.NewReader(`{"prompt":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Consume the operation frame, then close the HTTP connection as refresh does.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "operation_id") {
			break
		}
	}
	_ = resp.Body.Close()
	var current *sessions.Session
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, _ = sessions.NewStore(root).Get(sess.ID)
		if current.Operation != nil && current.Operation.Thought == "provider summary" && current.Operation.Text == "partial answer" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current.Operation == nil || current.Operation.Thought != "provider summary" || current.Operation.Status != "running" {
		t.Fatalf("no reconnect progress: %+v", current.Operation)
	}
	operationID := current.Operation.ID
	if len(current.Turns) != 1 || current.Turns[0].Text != "hello" {
		t.Fatalf("question overwritten: %+v", current.Turns)
	}
	stopped, err := http.Post(panel.URL+"/api/sessions/"+sess.ID+"/cancel", "application/json", strings.NewReader(`{"operation_id":"`+operationID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, stopped.Body)
	stopped.Body.Close()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, _ = store.Get(sess.ID)
		if current.Operation.Status == "cancelled" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current.Operation.Status != "cancelled" || len(current.Turns) != 2 {
		t.Fatalf("stop did not settle transcript: %+v", current)
	}
	reply := current.Turns[1]
	if reply.Thought != "provider summary" || !strings.Contains(reply.Text, "partial answer") || reply.OperationID != operationID {
		t.Fatalf("partial display lost after stop: %+v", reply)
	}
}

func TestSessionAskPersistenceFailureNeverAcknowledgesSuccess(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	store := sessions.NewStore(t.TempDir())
	sess, _ := store.Create("save failure")
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate another surface removing storage after the question was filed.
		_ = store.Delete(sess.ID)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer only on the wire\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	if err := engine.SetModel(config.ModelConfig{APIType: config.APITypeOpenAI, BaseURL: model.URL, Model: "test", NoAuth: true}); err != nil {
		t.Fatal(err)
	}
	h := &handler{store: engine.TaskStore(), engine: engine, sessions: store, cfgMu: &sync.RWMutex{}, serviceCtx: context.Background(), activeAsks: map[string]sessionOperation{}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"hello"}`))
	req.SetPathValue("id", sess.ID)
	rr := httptest.NewRecorder()
	h.sessionAsk(rr, req)
	if !strings.Contains(rr.Body.String(), `"code":"persistence"`) || strings.Contains(rr.Body.String(), "event: result") {
		t.Fatalf("unsaved answer acknowledged: %s", rr.Body.String())
	}
}

func TestSessionAskProviderFailureKeepsPartialAnswer(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"summary before error\",\"content\":\"partial before error\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"error\":{\"type\":\"invalid_api_key\",\"message\":\"credential rejected\"}}\n\n")
	}))
	defer model.Close()
	if err := engine.SetModel(config.ModelConfig{APIType: config.APITypeOpenAI, BaseURL: model.URL, Model: "test", NoAuth: true}); err != nil {
		t.Fatal(err)
	}
	store := sessions.NewStore(t.TempDir())
	sess, _ := store.Create("provider failure")
	h := &handler{store: engine.TaskStore(), engine: engine, sessions: store, cfgMu: &sync.RWMutex{}, serviceCtx: context.Background(), activeAsks: map[string]sessionOperation{}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"hello"}`))
	req.SetPathValue("id", sess.ID)
	rr := httptest.NewRecorder()
	h.sessionAsk(rr, req)
	got, _ := store.Get(sess.ID)
	if len(got.Turns) != 2 || got.Operation.Status != "failed" || !strings.Contains(got.Turns[1].Text, "partial before error") || got.Turns[1].Thought != "summary before error" {
		t.Fatalf("failure erased display: %+v", got)
	}
}
