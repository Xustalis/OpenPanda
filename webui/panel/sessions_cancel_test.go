package panel

import (
	"context"
	"encoding/json"
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

	// When no ask is in flight, cancel returns cancelled: false
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+s.ID+"/cancel", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var res map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["cancelled"] != false {
		t.Fatalf("expected cancelled: false, got %v", res["cancelled"])
	}

	// Verify /stop alias also works
	rrStop := httptest.NewRecorder()
	h.ServeHTTP(rrStop, authedReq(http.MethodPost, "/api/sessions/"+s.ID+"/stop", nil))
	if rrStop.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rrStop.Code)
	}
}

func TestSessionCancelActiveAsk(t *testing.T) {
	h := &handler{
		activeAsks: make(map[string]context.CancelFunc),
	}
	cancelled := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ctx.Done()
		close(cancelled)
	}()

	h.registerSessionAsk("s1", cancel)

	if !h.cancelSessionAsk("s1") {
		t.Fatal("expected cancelSessionAsk to return true")
	}

	select {
	case <-cancelled:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("expected cancel func to be called")
	}

	// Double cancel returns false
	if h.cancelSessionAsk("s1") {
		t.Fatal("expected second cancelSessionAsk to return false")
	}
}
