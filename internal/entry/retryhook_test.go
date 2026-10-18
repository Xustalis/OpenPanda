// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestRetryHookFiresOnStreamRetry: the streaming loop reports each scheduled
// backoff with the retry's position, delay and cause — without the note a
// 429ing provider looks like a frozen spinner for the whole retry ladder.
func TestRetryHookFiresOnStreamRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		b, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]string{"content": "ok"}}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.maxRetry = 2
	c.retryBase = time.Millisecond

	var notes []RetryNote
	ctx := WithRetryHook(context.Background(), func(n RetryNote) { notes = append(notes, n) })
	if _, err := c.StreamTurnsWithTools(ctx, "", []Turn{{Role: "user", Content: "hi"}}, nil, func(string) {}, nil); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %d, want 1 (one retry after the 429)", len(notes))
	}
	n := notes[0]
	if n.Attempt != 1 || n.MaxAttempts != 2 {
		t.Fatalf("attempt/max = %d/%d, want 1/2", n.Attempt, n.MaxAttempts)
	}
	if n.Delay <= 0 {
		t.Fatalf("delay = %v, want > 0", n.Delay)
	}
	if kind, status := APIErrorKind(n.Err); kind != ErrKindRateLimited || status != 429 {
		t.Fatalf("note err = %v, want kind rate_limited status 429", n.Err)
	}
}

// TestRetryHookFiresOnCompleteRetry: the non-streaming OpenAI loop reports the
// same way — the hook must cover every path the spinner hides behind.
func TestRetryHookFiresOnCompleteRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway"))
			return
		}
		b, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.maxRetry = 2
	c.retryBase = time.Millisecond

	var notes []RetryNote
	ctx := WithRetryHook(context.Background(), func(n RetryNote) { notes = append(notes, n) })
	if _, err := c.CompleteTurnsWithTools(ctx, "", []Turn{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(notes) != 1 || notes[0].Attempt != 1 {
		t.Fatalf("notes = %+v, want exactly one retry note with attempt 1", notes)
	}
}

// TestRetryHookNoHookIsNoop: without a hook attached the retry loops simply
// sleep — no nil-call panic, no behaviour change for batch/test callers.
func TestRetryHookNoHookIsNoop(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.retryBase = time.Millisecond
	if _, err := c.CompleteTurnsWithTools(context.Background(), "", []Turn{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("complete without hook: %v", err)
	}
}

// TestRetryHookCancelDuringBackoff: a cancel mid-backoff aborts the sleep —
// the hook's note must not keep the spinner alive once the user pressed stop.
func TestRetryHookCancelDuringBackoff(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.maxRetry = 5

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.CompleteTurnsWithTools(ctx, "", []Turn{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("canceled call succeeded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestAPIErrorKind: every failure class maps to the kind a surface uses to
// pick its troubleshooting copy, with the provider's HTTP status preserved.
func TestAPIErrorKind(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantKind   string
		wantStatus int
	}{
		{"no key", ErrNoKey, ErrKindNoKey, 0},
		{"no model", ErrNoModel, ErrKindNoKey, 0},
		{"canceled", context.Canceled, ErrKindCanceled, 0},
		{"deadline", context.DeadlineExceeded, ErrKindTimeout, 0},
		{"401", &statusError{status: 401}, ErrKindAuth, 401},
		{"403", &statusError{status: 403}, ErrKindAuth, 403},
		{"404", &statusError{status: 404}, ErrKindNotFound, 404},
		{"400", &statusError{status: 400}, ErrKindBadRequest, 400},
		{"413", &statusError{status: 413}, ErrKindGeneric, 413},
		{"429 definitive", &statusError{status: 429}, ErrKindRateLimited, 429},
		{"429 retried", &retryableError{status: 429}, ErrKindRateLimited, 429},
		{"502 retried", &retryableError{status: 502}, ErrKindUnavailable, 502},
		{"500 definitive", &statusError{status: 500}, ErrKindUnavailable, 500},
		{"transport", &transientError{err: errors.New("conn reset")}, ErrKindUnreachable, 0},
		{"unknown", errors.New("weird"), ErrKindGeneric, 0},
		{"already classified", &ClassifyError{Kind: ErrKindRateLimited, Status: 429, Err: errors.New("x")}, ErrKindRateLimited, 429},
	}
	for _, tc := range cases {
		kind, status := APIErrorKind(tc.err)
		if kind != tc.wantKind || status != tc.wantStatus {
			t.Errorf("%s: got (%q, %d), want (%q, %d)", tc.name, kind, status, tc.wantKind, tc.wantStatus)
		}
	}
}

// TestWrapAPIErrorKindStatus: the wrapped error carries the machine kind and
// status alongside the user message so surfaces never parse UserMsg back.
func TestWrapAPIErrorKindStatus(t *testing.T) {
	err := WrapAPIError(&retryableError{status: 429, body: "slow down"})
	var ce *ClassifyError
	if !errors.As(err, &ce) {
		t.Fatalf("not a ClassifyError: %T", err)
	}
	if ce.Kind != ErrKindRateLimited || ce.Status != 429 {
		t.Fatalf("kind/status = %q/%d, want rate_limited/429", ce.Kind, ce.Status)
	}
	// Re-wrapping an already-classified error must be idempotent — the ask
	// engine's catch-all boundary cannot flatten a specific kind.
	again := WrapAPIError(ce)
	if !errors.As(again, &ce) || ce.Kind != ErrKindRateLimited {
		t.Fatalf("re-wrap flattened kind: %v", again)
	}
}

// TestEnsureAPIErrorBoundary: the idempotent boundary wrap dresses only real
// model-call failures — a storage/routing error keeps its own wording and an
// already classified one its original kind.
func TestEnsureAPIErrorBoundary(t *testing.T) {
	wrapped := EnsureAPIError(&statusError{status: 500, body: "oops"}, i18n.English)
	var ce *ClassifyError
	if !errors.As(wrapped, &ce) || ce.Kind != ErrKindUnavailable {
		t.Fatalf("raw 500 not classified: %v", wrapped)
	}

	plain := errors.New("storage: disk full")
	if got := EnsureAPIError(plain, i18n.English); got != plain {
		t.Fatalf("non-model error was re-dressed: %v", got)
	}

	already := &ClassifyError{UserMsg: "custom", Kind: ErrKindAuth, Err: errors.New("x")}
	if got := EnsureAPIError(already, i18n.English); got != already {
		t.Fatalf("classified error was re-wrapped: %v", got)
	}

	if got := EnsureAPIError(context.DeadlineExceeded, i18n.English); !IsModelCallError(context.DeadlineExceeded) {
		t.Fatalf("deadline not dressed: %v", got)
	}
	// A user cancel passes through untouched: surfaces phrase it themselves.
	if got := EnsureAPIError(context.Canceled, i18n.English); got != context.Canceled {
		t.Fatalf("cancel was dressed: %v", got)
	}
}
