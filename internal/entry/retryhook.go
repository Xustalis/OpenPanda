// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"time"
)

// RetryNote describes one transport retry the client is about to sleep
// through: which retry this is inside the call's budget, how long the backoff
// lasts, and why the previous attempt failed. It exists so a surface can show
// "rate limited, retrying in Ns" on its status line — without it a provider
// answering 429/5xx sits silently behind the spinner for the whole backoff
// ladder, which reads as a hang rather than a retry.
type RetryNote struct {
	// Attempt is the upcoming retry's 1-based index inside the budget (1 is
	// the first retry after the initial attempt failed).
	Attempt int
	// MaxAttempts is the retry budget for this call — Attempt counts to it.
	MaxAttempts int
	// Delay is the backoff about to be slept before the retry fires.
	Delay time.Duration
	// Err is the failure being retried — a *statusError/*retryableError for a
	// provider answer (429/5xx), a *transientError for a transport drop.
	Err error
}

// retryHookKey carries a RetryNote sink through the ask's context down to the
// client's retry loops. Context rather than a Client field: one Client serves
// many concurrent asks, and each ask's surface (TUI card, SSE stream, CLI
// status line) wants its own progress path — a shared callback would leak one
// ask's retries into another's display.
type retryHookKey struct{}

// WithRetryHook attaches fn to ctx. The retry loops in streamWithRetry,
// completeWithRetry and completeOpenAI call it once per scheduled backoff,
// before sleeping. A nil fn is a no-op — most callers (tests, batch tools)
// have nothing to report to.
func WithRetryHook(ctx context.Context, fn func(RetryNote)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, retryHookKey{}, fn)
}

// notifyRetry delivers one note to the hook ctx carries, if any. Kept as the
// single call-site helper so the three retry loops stay one-liners and the
// hook's signature lives in exactly one place.
func notifyRetry(ctx context.Context, attempt, maxAttempts int, delay time.Duration, err error) {
	fn, _ := ctx.Value(retryHookKey{}).(func(RetryNote))
	if fn == nil {
		return
	}
	fn(RetryNote{Attempt: attempt, MaxAttempts: maxAttempts, Delay: delay, Err: err})
}
