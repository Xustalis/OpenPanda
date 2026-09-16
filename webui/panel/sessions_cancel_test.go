package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func TestSessionCancelEndpoint(t *testing.T) {
	sessStore := sessions.NewStore(t.TempDir())
	s, err := sessStore.Create("test session", "")
	if err != nil {
		t.Fatal(err)
	}

	h := New(Deps{
		Store:    newTestStore(t),
		Sessions: sessStore,
		Token:    testToken,
	})

	// Missing operation identity is rejected instead of cancelling whichever
	// generation happens to be current.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+s.ID+"/cancel", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	// The /stop alias has the same identity requirement.
	rrStop := httptest.NewRecorder()
	h.ServeHTTP(rrStop, authedReq(http.MethodPost, "/api/sessions/"+s.ID+"/stop", nil))
	if rrStop.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rrStop.Code)
	}
}

func TestSessionCancelActiveAsk(t *testing.T) {
	h := &handler{activeAsks: make(map[string]sessionOperation)}
	cancelled := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ctx.Done()
		close(cancelled)
	}()

	op, err := h.registerSessionAsk("s1", cancel)
	if err != nil {
		t.Fatal(err)
	}
	if h.cancelSessionAsk("s1", "stale-operation") {
		t.Fatal("stale operation must not cancel the current ask")
	}
	if !h.cancelSessionAsk("s1", op.operationID) {
		t.Fatal("expected matching operation to cancel")
	}

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("expected cancel func to be called")
	}
	if h.cancelSessionAsk("s1", op.operationID) {
		t.Fatal("expected second cancel to return false")
	}
}

func TestSessionStaleUnregisterPreservesNewGeneration(t *testing.T) {
	h := &handler{activeAsks: make(map[string]sessionOperation)}
	oldCtx, oldCancel := context.WithCancel(context.Background())
	old, err := h.registerSessionAsk("s1", oldCancel)
	if err != nil {
		t.Fatal(err)
	}
	newCtx, newCancel := context.WithCancel(context.Background())
	newer, err := h.registerSessionAsk("s1", newCancel)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldCtx.Done():
	default:
		t.Fatal("replacement must cancel the old generation")
	}

	h.unregisterSessionAsk("s1", old)
	if h.cancelSessionAsk("s1", old.operationID) {
		t.Fatal("old operation ID must not cancel the replacement")
	}
	if !h.cancelSessionAsk("s1", newer.operationID) {
		t.Fatal("stale unregister deleted the newer generation")
	}
	select {
	case <-newCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("new generation was not cancelled")
	}
}
