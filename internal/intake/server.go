// SPDX-License-Identifier: AGPL-3.0-or-later

package intake

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

// maxBodyBytes bounds one delivery. GitHub caps a webhook payload well below
// this; the limit exists so an unauthenticated caller cannot make the daemon
// allocate arbitrarily before the signature check runs.
const maxBodyBytes = 1 << 20

// Sink turns an admitted event into queued work and returns its task id.
type Sink interface {
	IntakeTask(ctx context.Context, ev Event) (taskID string, err error)
}

// Seen is the idempotence ledger. ClaimIntake atomically records the key and
// reports whether this caller recorded it first; a nil Seen disables the
// check, which is only right for a source that never retries.
type Seen interface {
	ClaimIntake(ctx context.Context, source, key string) (first bool, err error)
}

// Server answers source webhooks. One server serves one source shape today;
// the mux is the seam a second source plugs into without touching the gate.
type Server struct {
	Policy Policy
	// Secret is the HMAC key shared with the source. Empty refuses every
	// delivery — see VerifyGitHubSignature.
	Secret string
	Seen   Seen
	Sink   Sink
	Logger *slog.Logger
}

// ErrNoSink is the wiring mistake a listener should never start with.
var ErrNoSink = errors.New("intake: no sink wired")

// Handler returns the HTTP surface. Only POST is accepted; the mux answers
// 405 for anything else without reaching the gate.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/github", s.handleGitHub)
	return mux
}

type response struct {
	OK      bool   `json:"ok"`
	Task    string `json:"task,omitempty"`
	Ignored string `json:"ignored,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, response{Error: "body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, response{Error: "unreadable body"})
		return
	}
	if !VerifyGitHubSignature(s.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		s.log().Warn("intake: rejected delivery with a bad signature",
			"delivery", r.Header.Get("X-GitHub-Delivery"),
			"event", r.Header.Get("X-GitHub-Event"))
		writeJSON(w, http.StatusUnauthorized, response{Error: "signature mismatch"})
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	ev, modeled, err := ParseGitHub(eventType, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, response{Error: err.Error()})
		return
	}
	if !modeled {
		writeJSON(w, http.StatusOK, response{OK: true, Ignored: CodeUnsupported})
		return
	}
	ev.Delivery = r.Header.Get("X-GitHub-Delivery")

	decision := s.Policy.Admit(ev)
	if !decision.Admit {
		s.log().Info("intake: event not admitted",
			"repo", ev.Repo, "thread", ev.Thread, "actor", ev.Actor,
			"kind", ev.Kind, "reason", decision.Code)
		writeJSON(w, http.StatusOK, response{OK: true, Ignored: decision.Code})
		return
	}

	if s.Seen != nil && ev.Delivery != "" {
		first, err := s.Seen.ClaimIntake(r.Context(), ev.Source, ev.Delivery)
		if err != nil {
			s.log().Error("intake: claim delivery", "delivery", ev.Delivery, "err", err)
			writeJSON(w, http.StatusInternalServerError, response{Error: "could not record delivery"})
			return
		}
		if !first {
			writeJSON(w, http.StatusOK, response{OK: true, Ignored: CodeDuplicate})
			return
		}
	}

	if s.Sink == nil {
		s.log().Error("intake: admitted an event with no sink wired", "sink", ErrNoSink)
		writeJSON(w, http.StatusInternalServerError, response{Error: ErrNoSink.Error()})
		return
	}
	taskID, err := s.Sink.IntakeTask(r.Context(), ev)
	if err != nil {
		s.log().Error("intake: submit task",
			"repo", ev.Repo, "thread", ev.Thread, "err", err)
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not create task"})
		return
	}
	s.log().Info("intake: task queued",
		"task", taskID, "repo", ev.Repo, "thread", ev.Thread, "actor", ev.Actor, "kind", ev.Kind)
	writeJSON(w, http.StatusAccepted, response{OK: true, Task: taskID})
}

func (s *Server) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func writeJSON(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
