// Package panel serves the legacy PWA control panel (kept frozen as an optional
// webui/ sidecar; the kernel daemon no longer mounts it): the static web app
// under webui/web/pwa plus the JSON API that backs it — task queue, task detail,
// and the human approval of reviewed tasks (design §14.2 Layer 4).
package panel

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xenith/openpanda/internal/askengine"
	"github.com/xenith/openpanda/internal/core"
	"github.com/xenith/openpanda/internal/memory"
	"github.com/xenith/openpanda/webui/push"
)

// Deps wires the panel to its collaborators. Store is required; everything
// else degrades gracefully — Engine nil means /api/ask answers-only mode,
// Projects nil disables the project endpoints, DB nil disables /api/nodes.
type Deps struct {
	Store     *core.TaskStore
	Engine    *askengine.Engine
	DB        *sql.DB
	Projects  *memory.Projects
	Push      *push.Service
	StaticDir string
	Token     string
}

// New builds the panel HTTP handler: the static web app under StaticDir plus
// the JSON API that backs it — task queue, task detail, ask (unified entry),
// projects, nodes, cancel/logs, the human approval of reviewed tasks (design
// §14.2 Layer 4), and an SSE change feed. Push, when non-nil, additionally
// serves the Web Push subscription endpoints. Token is the Bearer credential
// guarding every /api/* route; the static files under / are served
// unauthenticated (they carry no secrets, the API does). An empty token fails
// closed: /api/* rejects every request until a token is configured.
func New(d Deps) http.Handler {
	h := &handler{
		store:    d.Store,
		engine:   d.Engine,
		db:       d.DB,
		projects: d.Projects,
		push:     d.Push,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks", h.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", h.getTask)
	mux.HandleFunc("POST /api/tasks/{id}/approve", h.approveTask)
	mux.HandleFunc("POST /api/tasks/{id}/reject", h.rejectTask)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", h.cancelTask)
	mux.HandleFunc("GET /api/tasks/{id}/logs", h.taskLogs)
	mux.HandleFunc("POST /api/ask", h.ask)
	mux.HandleFunc("GET /api/projects", h.listProjects)
	mux.HandleFunc("POST /api/projects", h.createProject)
	mux.HandleFunc("GET /api/nodes", h.listNodes)
	mux.HandleFunc("GET /api/events", h.events)
	if d.Push != nil {
		mux.HandleFunc("GET /api/push/key", h.pushKey)
		mux.HandleFunc("POST /api/push/subscribe", h.pushSubscribe)
		mux.HandleFunc("POST /api/push/unsubscribe", h.pushUnsubscribe)
	}
	mux.Handle("/", staticHandler(d.StaticDir))
	return authMiddleware(d.Token, mux)
}

// authMiddleware guards /api/* with a constant-time Bearer comparison. An empty
// token fails closed (every /api/* request is rejected) so the panel can never
// run open by accident; the daemon additionally refuses to start the panel at
// all when no token is configured (see cmd/panda). Static assets under / are
// always served. Failed attempts are rate-limited per client IP (L1).
func authMiddleware(token string, next http.Handler) http.Handler {
	limiter := &authLimiter{failures: map[string]*authFailure{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			ip := clientIP(r)
			if limiter.locked(ip) {
				writeErr(w, http.StatusTooManyRequests, errors.New("too many failed attempts"))
				return
			}
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			// Hash both sides so the comparison is constant-time regardless of
			// length — ConstantTimeCompare otherwise early-returns on a length
			// mismatch, leaking the token length.
			gotSum := sha256.Sum256([]byte(got))
			wantSum := sha256.Sum256([]byte(token))
			if token == "" || subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
				limiter.fail(ip)
				writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// maxAuthFailures is the number of failed Bearer attempts one client IP may
// make within authFailWindow before it is locked out with 429 (L1). Without a
// limit the constant-time comparison would still permit full-speed brute force.
const maxAuthFailures = 5

// authFailWindow is a var so tests can shrink it.
var authFailWindow = time.Minute

// authLimiter tracks failed Bearer attempts per client IP in a fixed window.
// A locked-out IP is rejected even with a correct token until the window
// resets — a deliberate trade-off that keeps the limiter trivial.
type authLimiter struct {
	mu       sync.Mutex
	failures map[string]*authFailure
}

type authFailure struct {
	count int
	reset time.Time
}

// locked reports whether ip has exhausted its failure budget for the current
// window.
func (l *authLimiter) locked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.failures[ip]
	return f != nil && time.Now().Before(f.reset) && f.count >= maxAuthFailures
}

// fail records one failed attempt for ip, starting a fresh window when the
// previous one expired.
func (l *authLimiter) fail(ip string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.failures[ip]
	if f == nil || now.After(f.reset) {
		f = &authFailure{reset: now.Add(authFailWindow)}
		l.failures[ip] = f
	}
	f.count++
}

// clientIP extracts the IP part of the request's RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type handler struct {
	store    *core.TaskStore
	engine   *askengine.Engine
	db       *sql.DB
	projects *memory.Projects
	push     *push.Service
}

// taskJSON is the wire form of a task row, with stable snake_case names so the
// PWA does not depend on Go field casing.
type taskJSON struct {
	ID        string      `json:"id"`
	ParentID  string      `json:"parent_id"`
	Project   string      `json:"project"`
	Title     string      `json:"title"`
	State     string      `json:"state"`
	Owner     string      `json:"owner"`
	AttemptID string      `json:"attempt_id"`
	Intent    string      `json:"intent,omitempty"`
	Spec      string      `json:"spec,omitempty"`
	Result    string      `json:"result,omitempty"`
	Risk      string      `json:"risk,omitempty"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
	Events    []eventJSON `json:"events,omitempty"`
}

type eventJSON struct {
	TS   int64  `json:"ts"`
	Type string `json:"type"`
	Data string `json:"data"`
}

func toTaskJSON(t core.Task) taskJSON {
	return taskJSON{
		ID:        t.TaskID,
		ParentID:  t.ParentID,
		Project:   t.Project,
		Title:     t.Title,
		State:     t.State,
		Owner:     t.OwnerNode,
		AttemptID: t.AttemptID,
		Intent:    t.Intent,
		Spec:      t.SpecJSON,
		Result:    t.ResultJSON,
		Risk:      t.Risk,
		CreatedAt: ts(t.CreatedAt),
		UpdatedAt: ts(t.UpdatedAt),
	}
}

// listTasks serves the queue, optionally filtered by state and project.
func (h *handler) listTasks(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	project := r.URL.Query().Get("project")

	tasks, err := h.store.ListByState(r.Context(), state)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("list tasks failed"))
		return
	}
	var filtered []core.Task
	for _, t := range tasks {
		if project == "" || t.Project == project {
			filtered = append(filtered, t)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].UpdatedAt > filtered[j].UpdatedAt })

	out := make([]taskJSON, 0, len(filtered))
	for _, t := range filtered {
		out = append(out, toTaskJSON(t))
	}
	writeJSON(w, out)
}

// getTask serves one task's full row plus its event timeline.
func (h *handler) getTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, errors.New("no such task"))
			return
		}
		writeErr(w, http.StatusInternalServerError, errors.New("load task failed"))
		return
	}
	out := toTaskJSON(t)
	if events, err := h.store.Events(r.Context(), id); err == nil {
		for _, e := range events {
			out.Events = append(out.Events, eventJSON{TS: e.TS, Type: e.Type, Data: e.DataJSON})
		}
	}
	writeJSON(w, out)
}

// approveTask accepts a reviewed task (review -> done).
func (h *handler) approveTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.Approve(r.Context(), id); err != nil {
		if errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrIllegal) {
			writeErr(w, http.StatusConflict, errors.New("task is not awaiting approval"))
			return
		}
		writeErr(w, http.StatusInternalServerError, errors.New("approve failed"))
		return
	}
	writeJSON(w, map[string]string{"id": id, "status": "approved"})
}

// rejectTask rejects a reviewed task (review -> failed).
func (h *handler) rejectTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.Reject(r.Context(), id, r.URL.Query().Get("reason")); err != nil {
		if errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrIllegal) {
			writeErr(w, http.StatusConflict, errors.New("task is not awaiting approval"))
			return
		}
		writeErr(w, http.StatusInternalServerError, errors.New("reject failed"))
		return
	}
	writeJSON(w, map[string]string{"id": id, "status": "rejected"})
}

// pushKey serves the VAPID applicationServerKey the browser subscribes with.
func (h *handler) pushKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"key": h.push.PublicKey()})
}

// pushSubscribe stores a browser PushSubscription.
func (h *handler) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	var sub push.Subscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.push.Subscribe(r.Context(), sub); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"status": "subscribed"})
}

// pushUnsubscribe removes a browser PushSubscription by endpoint.
func (h *handler) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.push.Unsubscribe(r.Context(), req.Endpoint); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"status": "unsubscribed"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	http.Error(w, err.Error(), code)
}

func ts(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).Format(time.RFC3339)
}
