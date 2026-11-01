// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

func clarifyPost(t *testing.T, h *handler, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/clarify", strings.NewReader(body))
	req.SetPathValue("id", sessionID)
	rr := httptest.NewRecorder()
	h.sessionClarify(rr, req)
	return rr
}

func TestClarifyWaiterDeliversOnce(t *testing.T) {
	h := &handler{}
	ch := make(chan []entry.ClarifyAnswer, 1)
	h.setClarifyWaiter("s1", "op1", ch)
	defer h.clearClarifyWaiter("op1")

	if h.answerClarify("s2", "op1", nil) {
		t.Fatal("an operation id from another session answered the question")
	}
	want := []entry.ClarifyAnswer{{ID: "go", Selected: []string{"run"}}}
	if !h.answerClarify("s1", "op1", want) {
		t.Fatal("answer not delivered")
	}
	select {
	case got := <-ch:
		if len(got) != 1 || got[0].ID != "go" || got[0].Selected[0] != "run" {
			t.Fatalf("delivered = %+v", got)
		}
	default:
		t.Fatal("nothing reached the parked ask")
	}
	// The waiter is consumed: a retry is a conflict, not a second answer.
	if h.answerClarify("s1", "op1", want) {
		t.Fatal("a consumed waiter accepted a second answer")
	}
}

func TestSessionClarifyEndpoint(t *testing.T) {
	h := &handler{}
	// No parked question: 409, not a silent success.
	rr := clarifyPost(t, h, "s1", `{"operation_id":"op1","answers":[]}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rr.Code, rr.Body.String())
	}

	ch := make(chan []entry.ClarifyAnswer, 1)
	h.setClarifyWaiter("s1", "op1", ch)
	defer h.clearClarifyWaiter("op1")
	rr = clarifyPost(t, h, "s1", `{"operation_id":"op1","answers":[{"id":"scope","selected":["internal"]}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	select {
	case got := <-ch:
		if len(got) != 1 || got[0].ID != "scope" || got[0].Selected[0] != "internal" {
			t.Fatalf("delivered = %+v", got)
		}
	default:
		t.Fatal("endpoint did not deliver the answer")
	}
}

func TestSessionClarifyRequiresOperationID(t *testing.T) {
	h := &handler{}
	rr := clarifyPost(t, h, "s1", `{"answers":[]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
