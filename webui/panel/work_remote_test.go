// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
)

func TestRemoteWorkCannotGrantAuthorityOrCreateReruns(t *testing.T) {
	db := newTestDB(t)
	h := New(Deps{Store: core.NewTaskStore(db, nil), DB: db, TaskFlow: taskflow.New(db, ""), Token: testToken, StaticDir: t.TempDir()})
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/work/connections/c/spaces/s/tasks", "{\"title\":\"goal\",\"authorized\":true}", 400},
		{"/api/work/connections/c/spaces/s/tasks", "{\"title\":\"goal\",\"startAt\":{\"kind\":\"date\",\"value\":\"2026-11-20\",\"authorized\":true}}", 400},
		{"/api/work/tasks/i%2Fw%2Fs%2Ft/run?connection=c", "{\"purpose\":\"rerun\"}", 501},
		{"/api/work/tasks/i%2Fw%2Fs%2Ft/run?connection=c", "{\"approvalMode\":\"after_execution\"}", 501},
		{"/api/work/tasks/i%2Fw%2Fs%2Ft/run?connection=c", "{\"plan\":{}}", 501},
	} {
		r := authedReq(http.MethodPost, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Idempotency-Key", "019c0000-0000-7000-8000-000000000001")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body)
		}
	}
}

func TestRemoteWorkPreservesProviderStatusAndRetry(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 410, 428, 429, 503} {
		w := httptest.NewRecorder()
		writeWorkError(w, &tsync.HTTPError{Status: status, RetryAfter: 1500 * time.Millisecond})
		if w.Code != status || w.Header().Get("Retry-After") != "2" {
			t.Fatalf("status=%d: %d %s", status, w.Code, w.Header().Get("Retry-After"))
		}
	}
}
