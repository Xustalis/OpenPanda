// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// Discovery reports what a machine is *configured* to use, which is not the
// same as what an endpoint will actually serve. Two real cases from one
// machine, both of which produced a row the endpoint rejected:
//
//   - Codex's root `model` with no `model_provider`: the model belongs to the
//     CLI's default route, not to the single relay the file happens to declare.
//   - Claude Code's CLAUDE_CODE_SUBAGENT_MODEL: an alias for the CLI's own
//     subagents, not a model id the relay publishes.
//
// Both are fixed at the source. This file is the net under them: before a row
// is written, ask the endpoint once whether it will answer for that model, and
// refuse to register one that says no.
//
// The check is deliberately narrow:
//
//   - Loopback only. A local proxy answers in milliseconds and costs the person
//     nothing; verifying fifty remote catalogue rows would make "import" a
//     minute-long action and spend real quota.
//   - Definite refusals only. A 404/400/401/403 means the row cannot work as
//     written. A timeout, a 429 or a 5xx means the endpoint is busy — the row is
//     imported anyway, because hiding a good model behind a transient fault is
//     worse than the fault itself.

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// complete runs one one-word turn and hands back whatever the client said.
func complete(ctx context.Context, mc config.ModelConfig, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	client, err := entry.NewClient(mc)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err = client.Complete(callCtx, "You are a connectivity test.", "Reply with exactly: OK")
	return err
}

// Verify is the import gate: it returns the endpoint's own refusal only when the
// answer is a definite no. A timeout, a 429 or a 5xx returns nil — hiding a good
// model behind a transient fault is worse than the fault itself.
func Verify(ctx context.Context, mc config.ModelConfig, timeout time.Duration) error {
	if !IsLoopback(mc.BaseURL) {
		return nil
	}
	if err := complete(ctx, mc, timeout); err != nil {
		// A caller that went away (browser closed, command cancelled) has not
		// learned anything about the row: reporting nil would import it as if
		// the endpoint had answered, so the cancellation is surfaced and the
		// caller decides to skip.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if definiteRejection(err) {
			return err
		}
	}
	return nil
}

// Answer is the scan badge: nil only when the endpoint actually answered. A
// model that was busy, queued or slow is reported as unverified with the
// endpoint's own words, because a green tick must mean "it works", not "we did
// not hear a refusal".
func Answer(ctx context.Context, mc config.ModelConfig, timeout time.Duration) error {
	if !IsLoopback(mc.BaseURL) {
		return errString("not checked: remote endpoint")
	}
	return complete(ctx, mc, timeout)
}

// IsLoopback reports whether a base URL points at this machine. A LAN address
// is not loopback on purpose: a vLLM box on another host is exactly as remote
// as a vendor, and gets the same "do not spend a round trip on it" treatment.
func IsLoopback(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// rejectionMarkers are the answers that mean "this row will never work", as
// opposed to "not right now".
var rejectionMarkers = []string{
	// The entry client renders a status as "api status 404: …", so the prefix
	// matters: a bare "400" would also match a message about 4000 tokens.
	"status 400", "status 401", "status 403", "status 404",
	"model_not_found", "model not found",
	"does not exist", "unknown model", "no such model",
	"invalid api key", "unauthorized",
}

// looksLikeCredential separates a real secret from a literal placeholder such as
// a local proxy's "PROXY_MANAGED". The distinction matters twice: the console
// must not label a placeholder as a key, and it must not mark the row no-auth —
// the proxy rejects a request without that header.
func looksLikeCredential(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	for _, prefix := range []string{"sk-", "oc_sk_", "ghp_", "xoxb-", "ak-", "aiza", "bearer "} {
		if strings.HasPrefix(low, prefix) {
			return true
		}
	}
	// A long unbroken run is a credential; a short word is a placeholder.
	return len(s) >= 32 && !strings.ContainsAny(s, " \t")
}

func definiteRejection(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range rejectionMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
