// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// sseEventNames drains one ask stream and returns the event names in order.
// The body is consumed to EOF, which is what the console's reader does too:
// a frame written on the handler's way out (queued_turn) is only visible if
// the client keeps reading until the connection closes.
func sseEventNames(t *testing.T, resp *http.Response) []string {
	t.Helper()
	defer resp.Body.Close()
	var names []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			names = append(names, strings.TrimPrefix(line, "event: "))
		}
	}
	return names
}

// TestQueuedNoteResumesAfterTurnEnds walks the whole console contract for a
// note typed while the agent is answering:
//
//	ask (held open) -> POST /messages -> turn ends -> queued_turn frame ->
//	GET /notes still lists it -> resume ask -> the words become a real turn.
//
// A note that vanishes at the end of the turn is the failure this covers.
// TestConsumedNoteReturnsToCarryWhenTurnFails covers the drain-then-die path:
// the round loop takes a note before calling the model, so a provider error
// leaves the words nowhere — not in the queue (drained), not in the thread
// (no answer). The turn's exit has to put them back.
func TestConsumedNoteReturnsToCarryWhenTurnFails(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	sessStore := sessions.NewStore(t.TempDir())
	sess, err := sessStore.Create("failed turn keeps note")
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{
		store: engine.TaskStore(), engine: engine, sessions: sessStore,
		cfgMu: &sync.RWMutex{}, serviceCtx: context.Background(),
		activeAsks: map[string]sessionOperation{},
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"bad request","type":"invalid_request_error"}}`, http.StatusBadRequest)
	}))
	defer model.Close()
	if err := engine.SetModel(config.ModelConfig{
		APIType: config.APITypeAnthropic, BaseURL: model.URL, Model: "queue-test", NoAuth: true,
	}); err != nil {
		t.Fatal(err)
	}

	note, ok := h.pushSteer(sess.ID, "must survive a dead turn", false)
	if !ok {
		t.Fatal("note was not accepted")
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"go"}`))
	r.SetPathValue("id", sess.ID)
	w := httptest.NewRecorder()
	h.sessionAsk(w, r)

	left := h.listSteer(sess.ID)
	if len(left) != 1 || left[0].text != note.text {
		t.Fatalf("consumed note was eaten by a failed turn: %+v (stream %s)", left, w.Body.String())
	}
}

func TestQueuedNoteResumesAfterTurnEnds(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	sessStore := sessions.NewStore(t.TempDir())
	sess, err := sessStore.Create("queued note resume")
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{
		store: engine.TaskStore(), engine: engine, sessions: sessStore,
		cfgMu: &sync.RWMutex{}, serviceCtx: context.Background(),
		activeAsks: map[string]sessionOperation{},
	}

	gate := make(chan struct{})
	var calls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		emit := func(v any) {
			raw, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		emit(map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]int{"input_tokens": 5}}})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		n := calls.Add(1)
		if n == 1 {
			<-gate // hold the first turn open until the note is queued
		}
		text := "first answer"
		if n > 1 {
			text = "second answer"
		}
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text"}})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		emit(map[string]any{"type": "content_block_stop", "index": 0})
		emit(map[string]any{"type": "message_stop"})
	}))
	defer model.Close()
	if err := engine.SetModel(config.ModelConfig{
		APIType: config.APITypeAnthropic, BaseURL: model.URL, Model: "queue-test", NoAuth: true,
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/ask", h.sessionAsk)
	mux.HandleFunc("POST /api/sessions/{id}/messages", h.sendSessionMessage)
	mux.HandleFunc("GET /api/sessions/{id}/notes", h.listSessionNotes)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// First turn: starts, blocks in the model stub. The note is only queued
	// once round 0 has actually begun — its GetSteer poll runs before the
	// first model call, so queueing earlier would hand the words to that
	// round instead of the turn boundary under test.
	firstEvents := make(chan []string, 1)
	roundStarted := make(chan struct{})
	var once sync.Once
	go func() {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sessions/"+sess.ID+"/ask",
			strings.NewReader(`{"prompt":"first question"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			firstEvents <- []string{"transport:" + err.Error()}
			return
		}
		defer resp.Body.Close()
		var names []string
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "event: ") {
				continue
			}
			name := strings.TrimPrefix(line, "event: ")
			names = append(names, name)
			if name == "usage" {
				once.Do(func() { close(roundStarted) })
			}
		}
		firstEvents <- names
	}()

	select {
	case <-roundStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("first round never started")
	}

	// Queue the note mid-turn.
	msgReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sessions/"+sess.ID+"/messages",
		strings.NewReader(`{"text":"queued words","inject":false}`))
	msgResp, err := http.DefaultClient.Do(msgReq)
	if err != nil {
		t.Fatal(err)
	}
	msgBody, _ := io.ReadAll(msgResp.Body)
	msgResp.Body.Close()
	if msgResp.StatusCode != http.StatusOK || !strings.Contains(string(msgBody), `"queued"`) {
		t.Fatalf("note was not queued: %d %s", msgResp.StatusCode, msgBody)
	}

	close(gate)
	names := <-firstEvents
	announced := false
	for _, n := range names {
		if n == "queued_turn" {
			announced = true
		}
	}
	if !announced {
		t.Fatalf("turn ended without a queued_turn frame: %v", names)
	}

	// The note must still be listed: this is what the console's fallback reads
	// when the frame is missed.
	notesResp, err := http.Get(srv.URL + "/api/sessions/" + sess.ID + "/notes")
	if err != nil {
		t.Fatal(err)
	}
	notesBody, _ := io.ReadAll(notesResp.Body)
	notesResp.Body.Close()
	if !strings.Contains(string(notesBody), "queued words") {
		t.Fatalf("parked note is not listed: %s", notesBody)
	}

	// The continuation the console opens with it.
	resumeReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sessions/"+sess.ID+"/ask",
		strings.NewReader(`{"prompt":"queued words","resume_queued":true}`))
	resumeResp, err := http.DefaultClient.Do(resumeReq)
	if err != nil {
		t.Fatal(err)
	}
	resumeNames := sseEventNames(t, resumeResp)
	if resumeResp.StatusCode != http.StatusOK {
		t.Fatalf("resume refused: %d %v", resumeResp.StatusCode, resumeNames)
	}

	got, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var userTurns []string
	for _, turn := range got.Turns {
		if turn.Role == "user" {
			userTurns = append(userTurns, turn.Text)
		}
	}
	if len(userTurns) != 2 || userTurns[1] != "queued words" {
		t.Fatalf("queued words did not become a turn: %+v (turns=%+v)", userTurns, got.Turns)
	}
	if left := h.listSteer(sess.ID); len(left) != 0 {
		t.Fatalf("resumed note still pending: %+v", left)
	}
}
