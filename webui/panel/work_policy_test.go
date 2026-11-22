// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

// A local policy switch needs its own code: the console points at the policy
// editor, not at a connection that was never revoked.
func TestWorkErrorNamesTheLocalAutomationPolicy(t *testing.T) {
	w := httptest.NewRecorder()
	writeWorkError(w, taskproviders.ErrAutomationNotAuthorized)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("X-OpenPanda-Error"); got != "automation_not_authorized" {
		t.Fatalf("error code: %q", got)
	}
	if errors.Is(taskproviders.ErrAutomationNotAuthorized, taskproviders.ErrAuthorizationChanged) {
		t.Fatal("a policy switch must not pass as an authorization change")
	}
}
