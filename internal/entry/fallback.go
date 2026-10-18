// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// ClassifyError wraps a model/parse failure with a user-friendly message. The
// entry layer must never fail silently — an API error surfaces a clear
// explanation, and a malformed structured output degrades to an answer rather
// than erroring out (see ParseOutput).
type ClassifyError struct {
	UserMsg string // what to show the user
	Err     error  // underlying cause

	// Kind is the machine-readable failure class (the ErrKind* constants).
	// Surfaces map it to their own troubleshooting copy — the TUI's foldable
	// hint block, the console's <details> section — instead of parsing
	// UserMsg back apart.
	Kind string
	// Status is the HTTP status the provider answered with, when the failure
	// was a response (429, 400, 5xx…). 0 for transport-level failures that
	// never got an answer.
	Status int
	// Detail carries the provider's own rejection reason when one rode along
	// in the response body — bounded like UserMsg's appended detail.
	Detail string
}

// Error-kind labels a ClassifyError (and APIErrorKind's verdicts) carries.
// They double as i18n suffixes: entry.hints.<kind> is the troubleshooting
// text a surface expands under the error.
const (
	ErrKindNoKey       = "no_key"       // no credential configured
	ErrKindCanceled    = "canceled"     // caller aborted the call
	ErrKindTimeout     = "timeout"      // deadline hit
	ErrKindAuth        = "auth"         // 401/403 — key rejected
	ErrKindNotFound    = "not_found"    // 404 — wrong endpoint/model id
	ErrKindBadRequest  = "bad_request"  // 400 — provider rejected the payload
	ErrKindRateLimited = "rate_limited" // 429 after retries
	ErrKindUnavailable = "unavailable"  // 5xx after retries
	ErrKindUnreachable = "unreachable"  // transport failure after retries
	ErrKindGeneric     = "generic"      // anything else
)

func (e *ClassifyError) Error() string { return e.UserMsg }
func (e *ClassifyError) Unwrap() error { return e.Err }

// WrapAPIError converts a model-call error into a user-friendly ClassifyError.
// The message is actionable, not generic: a rejected key says to fix the key,
// a wrong endpoint says to fix base_url/model, rate limits and outages say to
// wait — so the user knows what to do instead of retrying blind. loc is the
// reply locale of the ask that failed (PromptOptions.UserLocale); omitting it
// falls back to the process default, matching the pre-locale behaviour.
// Already-classified input is returned untouched so a boundary re-wrap (the
// ask engine's catch-all) cannot flatten a specific kind into "generic".
func WrapAPIError(err error, locs ...i18n.Locale) error {
	var ce *ClassifyError
	if errors.As(err, &ce) {
		return err
	}
	loc := i18n.Locale("")
	if len(locs) > 0 {
		loc = locs[0]
	}
	switch {
	case errors.Is(err, ErrNoKey) || errors.Is(err, ErrNoModel):
		return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.noKey"), Err: err, Kind: ErrKindNoKey}
	case errors.Is(err, context.Canceled):
		return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.canceled"), Err: err, Kind: ErrKindCanceled}
	case errors.Is(err, context.DeadlineExceeded):
		return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.timeout"), Err: err, Kind: ErrKindTimeout}
	}

	// A definitive provider rejection (the request was answered, not lost).
	var se *statusError
	if errors.As(err, &se) {
		switch {
		case se.status == http.StatusUnauthorized || se.status == http.StatusForbidden:
			return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.unauthorized"), Err: err, Kind: ErrKindAuth, Status: se.status}
		case se.status == http.StatusNotFound:
			return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.notFound"), Err: err, Kind: ErrKindNotFound, Status: se.status}
		case se.status == http.StatusBadRequest:
			// The stock hint names the two most common causes, but a 400 can be
			// anything the provider dislikes (a retired model name, an oversized
			// context) — the provider's own reason rides along so the user fixes
			// the real cause instead of chasing the hint.
			msg := i18n.T(loc, "entry.err.badRequest")
			detail := providerDetail(se.body)
			if detail != "" {
				msg += "：" + detail
			}
			return &ClassifyError{UserMsg: msg, Err: err, Kind: ErrKindBadRequest, Status: se.status, Detail: detail}
		default:
			// A non-OK answer the retry ladder never saw (no budget left, or
			// a path without retries): a definitive 429/5xx still deserves the
			// precise kind — same verdict APIErrorKind returns — while truly
			// odd statuses (413, 422…) fall to generic with theirs intact.
			switch {
			case se.status == http.StatusTooManyRequests:
				return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.rateLimited"), Err: err, Kind: ErrKindRateLimited, Status: se.status}
			case se.status >= 500:
				return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.unavailable"), Err: err, Kind: ErrKindUnavailable, Status: se.status}
			default:
				return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.generic"), Err: err, Kind: ErrKindGeneric, Status: se.status}
			}
		}
	}

	// A retryable failure that survived its retries: the provider was
	// rate-limiting or down the whole time.
	var re *retryableError
	if errors.As(err, &re) {
		if re.status == http.StatusTooManyRequests {
			return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.rateLimited"), Err: err, Kind: ErrKindRateLimited, Status: re.status}
		}
		return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.unavailable"), Err: err, Kind: ErrKindUnavailable, Status: re.status}
	}

	// A transport-level failure that survived its retries: the endpoint was
	// never reached.
	var te *transientError
	if errors.As(err, &te) {
		return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.unreachable"), Err: err, Kind: ErrKindUnreachable}
	}

	return &ClassifyError{UserMsg: i18n.T(loc, "entry.err.generic"), Err: err, Kind: ErrKindGeneric}
}

// APIErrorKind classifies any model-call error — raw (statusError family) or
// wrapped (ClassifyError) — into the ErrKind* vocabulary plus the HTTP status
// the provider answered, if one exists. Surfaces that only see the error
// (rather than the ClassifyError wrapper) get the same mapping WrapAPIError
// applied. The second result is 0 when no response arrived.
func APIErrorKind(err error) (kind string, status int) {
	if err == nil {
		return "", 0
	}
	var ce *ClassifyError
	if errors.As(err, &ce) && ce.Kind != "" {
		return ce.Kind, ce.Status
	}
	switch {
	case errors.Is(err, ErrNoKey), errors.Is(err, ErrNoModel):
		return ErrKindNoKey, 0
	case errors.Is(err, context.Canceled):
		return ErrKindCanceled, 0
	case errors.Is(err, context.DeadlineExceeded):
		return ErrKindTimeout, 0
	}
	var se *statusError
	if errors.As(err, &se) {
		switch {
		case se.status == http.StatusUnauthorized || se.status == http.StatusForbidden:
			return ErrKindAuth, se.status
		case se.status == http.StatusNotFound:
			return ErrKindNotFound, se.status
		case se.status == http.StatusBadRequest:
			return ErrKindBadRequest, se.status
		case se.status == http.StatusTooManyRequests:
			return ErrKindRateLimited, se.status
		case se.status >= 500:
			return ErrKindUnavailable, se.status
		default:
			return ErrKindGeneric, se.status
		}
	}
	var re *retryableError
	if errors.As(err, &re) {
		if re.status == http.StatusTooManyRequests {
			return ErrKindRateLimited, re.status
		}
		return ErrKindUnavailable, re.status
	}
	var te *transientError
	if errors.As(err, &te) {
		return ErrKindUnreachable, 0
	}
	return ErrKindGeneric, 0
}

// IsModelCallError reports whether err came out of a model call (provider
// answer, transport failure, missing credential, deadline) rather than from
// the machinery around it — the boundary wrap uses it to re-dress only model
// failures, so a storage or routing error keeps its own message instead of
// being relabeled "entry model call failed".
func IsModelCallError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoKey) || errors.Is(err, ErrNoModel) {
		return true
	}
	// A deadline that expired mid-call is a model failure worth dressing; a
	// user cancel is not — surfaces already phrase it as "interrupted" via
	// errors.Is, and re-dressing it would only attach irrelevant hints.
	if errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return true
	}
	var se *statusError
	if errors.As(err, &se) {
		return true
	}
	var re *retryableError
	if errors.As(err, &re) {
		return true
	}
	var te *transientError
	return errors.As(err, &te)
}

// EnsureAPIError applies WrapAPIError only to genuine model-call failures that
// are not already classified — the idempotent form an outer boundary calls
// without knowing which inner layer the error escaped from.
func EnsureAPIError(err error, locs ...i18n.Locale) error {
	if err == nil {
		return nil
	}
	var ce *ClassifyError
	if errors.As(err, &ce) || !IsModelCallError(err) {
		return err
	}
	return WrapAPIError(err, locs...)
}

// providerDetail extracts the provider's rejection reason from a response
// body for display: the message field of the usual {"error":{"message":…}}
// envelope when the body is JSON, the first line otherwise. Bounded, so a
// verbose provider cannot flood the chat bubble.
func providerDetail(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var envelope struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &envelope) == nil {
		if envelope.Error.Message != "" {
			return truncate(envelope.Error.Message, 200)
		}
		if envelope.Message != "" {
			return truncate(envelope.Message, 200)
		}
	}
	line := body
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return truncate(strings.TrimSpace(line), 200)
}

// IsModelConfigError reports whether err names a problem the user can fix in
// the model settings — no key, a rejected key, a bad endpoint, a retired
// model id — rather than a transient outage worth waiting out. Surfaces that
// offer a "fix your model config" affordance (the web console's error bubble)
// key on this so the CTA lands exactly where it helps; a ClassifyError
// unwraps to the same verdict, so raw and user-facing errors classify alike.
func IsModelConfigError(err error) bool {
	if err == nil {
		return false
	}
	var ce *ClassifyError
	if errors.As(err, &ce) && ce.Err != nil {
		err = ce.Err
	}
	if errors.Is(err, ErrNoKey) || errors.Is(err, ErrNoModel) {
		return true
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.status == http.StatusUnauthorized ||
			se.status == http.StatusForbidden ||
			se.status == http.StatusNotFound ||
			se.status == http.StatusBadRequest
	}
	return false
}

// IsFatalModelError reports whether an error from the model client indicates
// that the provider or endpoint failed in an unrecoverable manner (unauthorized 401,
// forbidden/quota 403, not found 404, rate limited 429 after retries, 5xx server outage,
// or connection unreachable), making fallback to an alternate model candidate desirable.
// User context cancellation (context.Canceled) is NOT considered a fatal model error.
func IsFatalModelError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrNoKey) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.status == http.StatusUnauthorized ||
			se.status == http.StatusForbidden ||
			se.status == http.StatusNotFound ||
			se.status == http.StatusTooManyRequests ||
			se.status >= 500
	}
	var re *retryableError
	if errors.As(err, &re) {
		return true
	}
	var te *transientError
	if errors.As(err, &te) {
		return true
	}
	var ce *ClassifyError
	if errors.As(err, &ce) {
		return IsFatalModelError(ce.Err)
	}
	return false
}
