// SPDX-License-Identifier: AGPL-3.0-or-later

package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeSink struct {
	got []Event
	err error
}

func (f *fakeSink) IntakeTask(_ context.Context, ev Event) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.got = append(f.got, ev)
	return "task-1", nil
}

type fakeSeen struct {
	seen map[string]bool
	err  error
}

func (f *fakeSeen) ClaimIntake(_ context.Context, source, key string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	k := source + "|" + key
	if f.seen[k] {
		return false, nil
	}
	f.seen[k] = true
	return true, nil
}

func (f *fakeSeen) ReleaseIntake(_ context.Context, source, key string) error {
	if f.err != nil {
		return f.err
	}
	delete(f.seen, source+"|"+key)
	return nil
}

func newTestServer(sink Sink, seen Seen) *Server {
	return &Server{
		Policy: testPolicy(),
		Secret: "s3cret",
		Seen:   seen,
		Sink:   sink,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func deliver(t *testing.T, srv *Server, eventType, delivery, body string, sign bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("Content-Type", "application/json")
	if sign {
		req.Header.Set("X-Hub-Signature-256", signFor("s3cret", body))
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func signFor(secret, body string) string { return sign(secret, []byte(body)) }

func decode(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()
	var got response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %q", rec.Body.String())
	}
	return got
}

func TestServerRejectsUnsignedDelivery(t *testing.T) {
	sink := &fakeSink{}
	rec := deliver(t, newTestServer(sink, nil), "issues", "d1", issueOpenedPayload, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(sink.got) != 0 {
		t.Fatalf("an unsigned delivery created %d tasks", len(sink.got))
	}
}

func TestServerIgnoresUnaddressed(t *testing.T) {
	sink := &fakeSink{}
	body := strings.Replace(issueOpenedPayload, "@panda-bot please fix this", "just a comment", 1)
	rec := deliver(t, newTestServer(sink, nil), "issues", "d1", body, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := decode(t, rec); got.Ignored != CodeNotAddressed {
		t.Fatalf("ignored = %q, want %q", got.Ignored, CodeNotAddressed)
	}
	if len(sink.got) != 0 {
		t.Fatalf("an unaddressed event created %d tasks", len(sink.got))
	}
}

func TestServerAcceptsAndQueues(t *testing.T) {
	sink := &fakeSink{}
	rec := deliver(t, newTestServer(sink, &fakeSeen{}), "issues", "d1", issueOpenedPayload, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	got := decode(t, rec)
	if !got.OK || got.Task != "task-1" {
		t.Fatalf("response = %+v", got)
	}
	if len(sink.got) != 1 || sink.got[0].Delivery != "d1" {
		t.Fatalf("sink got %+v", sink.got)
	}
}

func TestServerDeduplicatesRetries(t *testing.T) {
	sink := &fakeSink{}
	srv := newTestServer(sink, &fakeSeen{})
	if rec := deliver(t, srv, "issues", "same-delivery", issueOpenedPayload, true); rec.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", rec.Code)
	}
	rec := deliver(t, srv, "issues", "same-delivery", issueOpenedPayload, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", rec.Code)
	}
	if got := decode(t, rec); got.Ignored != CodeDuplicate {
		t.Fatalf("retry ignored = %q, want duplicate", got.Ignored)
	}
	if len(sink.got) != 1 {
		t.Fatalf("a replayed delivery queued %d tasks, want 1", len(sink.got))
	}
}

func TestServerSinkFailure(t *testing.T) {
	sink := &fakeSink{err: errors.New("queue closed")}
	rec := deliver(t, newTestServer(sink, nil), "issues", "d1", issueOpenedPayload, true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// A delivery whose enqueue failed must stay retryable: the ledger claim is
// taken before the sink runs, so without a release the source's retry would be
// answered "duplicate" for a task that was never created.
func TestServerSinkFailureReleasesClaim(t *testing.T) {
	sink := &fakeSink{err: errors.New("queue closed")}
	srv := newTestServer(sink, &fakeSeen{})

	if rec := deliver(t, srv, "issues", "d1", issueOpenedPayload, true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failing delivery status = %d, want 500", rec.Code)
	}

	// The queue recovers; the source retries the same delivery id.
	sink.err = nil
	rec := deliver(t, srv, "issues", "d1", issueOpenedPayload, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry after failure = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	if got := decode(t, rec); got.Task != "task-1" {
		t.Fatalf("retry response = %+v", got)
	}
	if len(sink.got) != 1 {
		t.Fatalf("retry queued %d tasks, want 1", len(sink.got))
	}

	// Deduplication still holds once the delivery did produce work.
	rec = deliver(t, srv, "issues", "d1", issueOpenedPayload, true)
	if rec.Code != http.StatusOK || decode(t, rec).Ignored != CodeDuplicate {
		t.Fatalf("replay after success = %d (%s), want 200 duplicate", rec.Code, rec.Body.String())
	}
	if len(sink.got) != 1 {
		t.Fatalf("a replayed delivery queued %d tasks, want 1", len(sink.got))
	}
}

type retryableSink struct {
	fakeSink
	calls int
}

func (s *retryableSink) IntakeDelivery(ctx context.Context, ev Event) (string, error) {
	s.calls++
	return s.fakeSink.IntakeTask(ctx, ev)
}

func TestServerRetryableSinkDoesNotConsumeFailedDelivery(t *testing.T) {
	sink := &retryableSink{fakeSink: fakeSink{err: errors.New("queue closed")}}
	seen := &fakeSeen{}
	srv := newTestServer(sink, seen)
	if rec := deliver(t, srv, "issues", "same-delivery", issueOpenedPayload, true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d, want 500", rec.Code)
	}
	sink.err = nil
	if rec := deliver(t, srv, "issues", "same-delivery", issueOpenedPayload, true); rec.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	if sink.calls != 2 || len(sink.got) != 1 {
		t.Fatalf("delivery sink calls=%d queued=%d, want 2 calls and one queued task", sink.calls, len(sink.got))
	}
	if len(seen.seen) != 0 {
		t.Fatalf("reliable delivery consumed the legacy ledger: %v", seen.seen)
	}
	hash := sha256.Sum256([]byte(issueOpenedPayload))
	if got := sink.got[0].BodyHash; got != hex.EncodeToString(hash[:]) {
		t.Fatalf("body hash=%q does not match the signed body", got)
	}
	if strings.Contains(sink.got[0].Intent(), sink.got[0].BodyHash) {
		t.Fatal("transport hash leaked into execution instructions")
	}
}

func TestServerDeliverySinkConflictAndLegacyReplay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		ignored string
	}{
		{"changed body", ErrDeliveryConflict, http.StatusConflict, ""},
		{"legacy replay", ErrLegacyDelivery, http.StatusOK, CodeDuplicate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &retryableSink{fakeSink: fakeSink{err: tc.err}}
			srv := newTestServer(sink, &fakeSeen{err: errors.New("legacy ledger unavailable")})
			rec := deliver(t, srv, "issues", "d1", issueOpenedPayload, true)
			if rec.Code != tc.status || sink.calls != 1 || decode(t, rec).Ignored != tc.ignored {
				t.Fatalf("status=%d calls=%d response=%s", rec.Code, sink.calls, rec.Body.String())
			}
		})
	}
}

func TestServerIgnoresUnmodelledEvent(t *testing.T) {
	sink := &fakeSink{}
	rec := deliver(t, newTestServer(sink, nil), "ping", "d1", `{"zen":"hi"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := decode(t, rec); got.Ignored != CodeUnsupported {
		t.Fatalf("ignored = %q", got.Ignored)
	}
}

func TestServerRejectsOversizedBody(t *testing.T) {
	sink := &fakeSink{}
	body := strings.Repeat("x", maxBodyBytes+1)
	rec := deliver(t, newTestServer(sink, nil), "issues", "d1", body, true)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if len(sink.got) != 0 {
		t.Fatalf("an oversized body created %d tasks", len(sink.got))
	}
}

func TestServerRejectsGet(t *testing.T) {
	srv := newTestServer(&fakeSink{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/hooks/github", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
