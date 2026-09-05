// Package panel serves the legacy PWA control panel (kept frozen as an optional
// webui/ sidecar; the kernel daemon no longer mounts it): the static web app
// under webui/web/pwa plus the JSON API that backs it — task queue, task detail,
// and the human approval of reviewed tasks (design §14.2 Layer 4).
package panel

import (
	"context"
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

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/memory"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/updater"
	"github.com/Xustalis/OpenPanda/webui/push"
)

// Deps wires the panel to its collaborators. Store is required; everything
// else degrades gracefully — Engine nil means /api/ask answers-only mode,
// Projects nil disables the project endpoints, DB nil disables /api/nodes,
// Sessions nil disables the chat-session endpoints, Worktrees nil means
// sessions run without git isolation. EngineHolder, when set, supersedes
// Engine: the handler resolves the engine through it on every request, and
// the model-settings save path can hot-load an engine at runtime (the
// zero-config start → first saved model transition) instead of demanding a
// process restart.
type Deps struct {
	Store        *core.TaskStore
	Engine       *askengine.Engine
	EngineHolder *EngineHolder
	DB           *sql.DB
	Projects     *memory.Projects
	ProjectStore *projectstore.Store // nil falls back to memory-only projects
	Push         *push.Service
	Sessions     *sessions.Store
	Worktrees    *sessions.Worktrees
	SkillStore   *skills.Store    // nil disables the skill approval endpoints
	Reminders    *reminders.Store // nil disables the reminder endpoints
	Cfg          *config.Config
	ConfigPath   string // where PUT /api/settings/model persists ("" = memory only)
	CardPath     string // where the card API reads/edits capabilities.yaml ("" = engine's card)
	StaticDir    string
	Token        string
	Updater      *updater.Manager // nil disables the self-update endpoints
}

// New builds the panel HTTP handler: the static web app under StaticDir plus
// the JSON API that backs it — task queue, task detail, ask (unified entry,
// plain and streaming), chat sessions with worktree isolation, model settings,
// projects, nodes, cancel/logs, the human approval of reviewed tasks (design
// §14.2 Layer 4), and an SSE change feed. Push, when non-nil, additionally
// serves the Web Push subscription endpoints. Token is the Bearer credential
// guarding every /api/* route; the static files under / are served
// unauthenticated (they carry no secrets, the API does). An empty token fails
// closed: /api/* rejects every request until a token is configured.
func New(d Deps) http.Handler {
	h := &handler{
		store:        d.Store,
		engine:       d.Engine,
		engines:      d.EngineHolder,
		db:           d.DB,
		projects:     d.Projects,
		projectStore: d.ProjectStore,
		push:         d.Push,
		sessions:     d.Sessions,
		worktrees:    d.Worktrees,
		skillStore:   d.SkillStore,
		reminders:    d.Reminders,
		cfg:          d.Cfg,
		configPath:   d.ConfigPath,
		cardFilePath: d.CardPath,
		updater:      d.Updater,
	}
	// Session summary finalizer (queue redesign §5): finished tasks fold
	// their result into the linked chat as an assistant turn. Runs for the
	// process lifetime; no-op without the sessions store.
	h.startSessionFinalizer(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks", h.listTasks)
	mux.HandleFunc("POST /api/tasks", h.createTask)
	mux.HandleFunc("DELETE /api/tasks", h.clearTasks)
	mux.HandleFunc("GET /api/tasks/{id}", h.getTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", h.patchTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", h.deleteTask)
	mux.HandleFunc("POST /api/tasks/reorder", h.reorderTasks)
	mux.HandleFunc("POST /api/tasks/{id}/approve", h.approveTask)
	mux.HandleFunc("POST /api/tasks/{id}/reject", h.rejectTask)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", h.cancelTask)
	mux.HandleFunc("GET /api/tasks/{id}/logs", h.taskLogs)
	// The plan plane: read-only views over the tasks that already carry a
	// plan_id. Starting a plan stays on /api/ask, which classifies and returns
	// the id; these two are how the console follows one afterwards.
	mux.HandleFunc("GET /api/plans", h.listPlans)
	mux.HandleFunc("GET /api/plans/{id}", h.getPlan)
	mux.HandleFunc("POST /api/ask", h.ask)
	mux.HandleFunc("GET /api/agents", h.listAgents)
	mux.HandleFunc("POST /api/agents/{name}/test", h.testAgent)
	mux.HandleFunc("GET /api/projects", h.listProjects)
	mux.HandleFunc("POST /api/projects", h.createProject)
	mux.HandleFunc("GET /api/projects/{name}", h.getProject)
	mux.HandleFunc("PATCH /api/projects/{name}", h.patchProject)
	mux.HandleFunc("DELETE /api/projects/{name}", h.deleteProject)
	mux.HandleFunc("POST /api/projects/{name}/enter", h.enterProject)
	mux.HandleFunc("POST /api/projects/exit", h.exitProject)
	mux.HandleFunc("GET /api/projects/{name}/memory", h.getProjectMemory)
	mux.HandleFunc("PUT /api/projects/{name}/memory", h.putProjectMemory)
	mux.HandleFunc("GET /api/nodes", h.listNodes)
	mux.HandleFunc("POST /api/nodes/add", h.addNode)
	mux.HandleFunc("DELETE /api/nodes/{id}", h.removeNode)
	mux.HandleFunc("GET /api/self", h.getSelf)
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /api/settings/model", h.getModelSettings)
	mux.HandleFunc("PUT /api/settings/model", h.putModelSettings)
	mux.HandleFunc("POST /api/settings/model/test", h.testModelSettings)
	mux.HandleFunc("GET /api/version", h.getVersion)
	mux.HandleFunc("GET /api/metrics", h.listMetrics)
	mux.HandleFunc("GET /api/audit", h.verifyAudit)
	mux.HandleFunc("GET /api/audit/entries", h.auditEntries)
	if d.Updater != nil {
		mux.HandleFunc("GET /api/update", h.getUpdate)
		mux.HandleFunc("POST /api/update/check", h.checkUpdate)
		mux.HandleFunc("POST /api/update/download", h.downloadUpdate)
		mux.HandleFunc("POST /api/update/apply", h.applyUpdate)
		mux.HandleFunc("POST /api/update/cancel", h.cancelUpdate)
	}
	if d.SkillStore != nil {
		mux.HandleFunc("GET /api/skills", h.listSkills)
		mux.HandleFunc("POST /api/skills/approve", h.skillAction(true))
		mux.HandleFunc("POST /api/skills/reject", h.skillAction(false))
	}
	if d.Reminders != nil {
		mux.HandleFunc("GET /api/reminders", h.listReminders)
		mux.HandleFunc("POST /api/reminders", h.createReminder)
		mux.HandleFunc("DELETE /api/reminders/{id}", h.deleteReminder)
	}
	if d.Cfg != nil {
		mux.HandleFunc("GET /api/memory", h.getMemory)
		mux.HandleFunc("PUT /api/memory/{file}", h.putMemory)
		mux.HandleFunc("PUT /api/memory/topics/{name}", h.putMemoryTopic)
		mux.HandleFunc("DELETE /api/memory/topics/{name}", h.deleteMemoryTopic)
		mux.HandleFunc("GET /api/settings/app", h.getAppSettings)
		mux.HandleFunc("PUT /api/settings/app", h.putAppSettings)
	}
	mux.HandleFunc("GET /api/settings/mcp", h.getMCPSettings)
	mux.HandleFunc("PUT /api/settings/mcp", h.putMCPSettings)
	// Capability card (stage 6): the console's /card — structured mutations
	// shared with the CLI/REPL (cardmut), plus the raw YAML editor. Every
	// write hot-reloads the live engine's card.
	mux.HandleFunc("GET /api/card", h.getCard)
	mux.HandleFunc("PUT /api/card", h.putCardRaw)
	mux.HandleFunc("POST /api/card/native", h.addNative)
	mux.HandleFunc("DELETE /api/card/native/{id}", h.removeNative)
	mux.HandleFunc("POST /api/card/agents/{name}", h.addAgent)
	mux.HandleFunc("PATCH /api/card/agents/{name}", h.patchAgent)
	mux.HandleFunc("DELETE /api/card/agents/{name}", h.removeAgent)
	mux.HandleFunc("POST /api/card/manual", h.addManual)
	mux.HandleFunc("DELETE /api/card/manual/{id}", h.removeManual)
	mux.HandleFunc("POST /api/dialog/choose-directory", h.chooseDirectoryDialog)
	mux.HandleFunc("GET /api/fs/directories", h.listDirectories)
	if d.Sessions != nil {
		mux.HandleFunc("GET /api/sessions", h.listSessions)
		mux.HandleFunc("POST /api/sessions", h.createSession)
		mux.HandleFunc("GET /api/sessions/{id}", h.getSession)
		mux.HandleFunc("PATCH /api/sessions/{id}", h.patchSession)
		mux.HandleFunc("DELETE /api/sessions/{id}", h.deleteSession)
		mux.HandleFunc("POST /api/sessions/{id}/ask", h.sessionAsk)
		if d.Worktrees != nil {
			mux.HandleFunc("GET /api/sessions/{id}/diff", h.sessionDiff)
			mux.HandleFunc("POST /api/sessions/{id}/merge", h.sessionMerge)
		}
	}
	if d.Push != nil {
		mux.HandleFunc("GET /api/push/key", h.pushKey)
		mux.HandleFunc("POST /api/push/subscribe", h.pushSubscribe)
		mux.HandleFunc("POST /api/push/unsubscribe", h.pushUnsubscribe)
	}
	mux.Handle("/", staticHandler(d.StaticDir))
	return securityHeaders(authMiddleware(d.Token, mux))
}

// securityHeaders adds defense-in-depth response headers to every request
// served by the panel. Even though the panel defaults to loopback binding,
// these headers protect against MIME sniffing, clickjacking, and referrer
// leakage if the panel is ever exposed behind a reverse proxy.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// authMiddleware guards /api/* with a constant-time Bearer comparison. An empty
// token fails closed (every /api/* request is rejected) so the panel can never
// run open by accident; the daemon additionally refuses to start the panel at
// all when no token is configured (see cmd/panda). Static assets under / are
// always served. Failed attempts are rate-limited per client IP (L1), but a
// correct token always passes and resets that budget — the lockout throttles
// brute force, it must never lock out a client holding valid credentials
// (EventSource auto-reconnects with a stale token otherwise locks an IP out
// with the panel left unusable).
func authMiddleware(token string, next http.Handler) http.Handler {
	limiter := &authLimiter{failures: map[string]*authFailure{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			ip := clientIP(r)
			// A correct token clears the failure budget and passes without
			// consulting the limiter — checked first, before any lockout.
			if token != "" {
				got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				// EventSource cannot send headers — accept ?token= too (the
				// same pattern `panda web` already uses for /?token= login).
				if got == "" {
					got = r.URL.Query().Get("token")
				}
				// Hash both sides so the comparison is constant-time regardless
				// of length — ConstantTimeCompare otherwise early-returns on a
				// length mismatch, leaking the token length.
				gotSum := sha256.Sum256([]byte(got))
				wantSum := sha256.Sum256([]byte(token))
				if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1 {
					limiter.reset(ip)
					next.ServeHTTP(w, r)
					return
				}
			}
			if limiter.locked(ip) {
				writeErr(w, http.StatusTooManyRequests, errors.New("too many failed attempts"))
				return
			}
			limiter.fail(ip)
			writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
			return
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
// A locked-out IP is rejected until the window resets, but the token check
// above runs FIRST: a correct token always passes and resets the budget, so
// a client holding valid credentials can never lock itself out (an
// EventSource reconnecting with a stale token merely fails again).
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

// reset clears the failure budget for ip after a successful authentication.
func (l *authLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
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
	engine   *askengine.Engine // static engine; ignored when engines != nil
	engines  *EngineHolder     // reloadable engine source; nil = static
	db       *sql.DB
	projects *memory.Projects
	// projectStore is the project metadata table (work dir, description, the
	// active pointer). nil on a database from before the projects migration, which
	// degrades the console to the memory-only project view instead of erroring.
	projectStore *projectstore.Store
	push         *push.Service
	sessions     *sessions.Store
	worktrees    *sessions.Worktrees
	skillStore   *skills.Store
	reminders    *reminders.Store
	cfg          *config.Config
	configPath   string
	// cardFilePath is where the card API edits capabilities.yaml; empty
	// means "ask the live engine which card it was built from".
	cardFilePath string
	updater      *updater.Manager
}

// taskJSON is the wire form of a task row, with stable snake_case names so the
// PWA does not depend on Go field casing. All visibility-extension fields are
// optional (omitempty): old clients never see them, new clients paint richer
// views when they are present.
type taskJSON struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Owner     string `json:"owner"`
	AttemptID string `json:"attempt_id"`
	Intent    string `json:"intent,omitempty"`
	Spec      string `json:"spec,omitempty"`
	Result    string `json:"result,omitempty"`
	Risk      string `json:"risk,omitempty"`
	// Queue redesign fields: the board sorts by priority, then seq (drag
	// order, 0 = not dragged), and jumps into session_id when set.
	Priority     string      `json:"priority"`
	Seq          int64       `json:"seq"`
	SessionID    string      `json:"session_id,omitempty"`
	ResourceKeys []string    `json:"resource_keys,omitempty"`
	Scheduled    bool        `json:"scheduled"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
	Events       []eventJSON `json:"events,omitempty"`

	// — Decision-orbit visibility (§5.2). traces are the subset of Events
	// that belong to the 8-track set, decoded so the client avoids a second
	// JSON.parse per row. delegation_chain and plan_meta aggregate data
	// already on the task row so the orbit can render without a follow-up
	// round-trip.
	Traces          []traceEventJSON    `json:"traces,omitempty"`
	DelegationChain []delegationHopJSON `json:"delegation_chain,omitempty"`
	PlanMeta        *planMetaJSON       `json:"plan_meta,omitempty"`
	// — Structured extras (§3.3). supervision = the latest supervision_round's
	// counters so the orbit paints round N/Budget badges; tier2_ops = every
	// parked or executed irreversible operation summary (K5 visibility).
	Supervision *supervisionJSON `json:"supervision,omitempty"`
	Tier2Ops    []tier2OpJSON    `json:"tier2_ops,omitempty"`
}

type eventJSON struct {
	TS   int64  `json:"ts"`
	Type string `json:"type"`
	Data string `json:"data"`
}

// traceEventJSON is a task_events row restricted to the 8 visible-track set.
// Data is pre-decoded so the browser reads structured objects; on malformed
// JSON we keep the raw string via the fallback_raw field so users still see
// the data instead of a silent swallow.
type traceEventJSON struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"`
	Type    string `json:"type"`
	Data    any    `json:"data,omitempty"`
	DataRaw string `json:"data_raw,omitempty"`
}

// planMetaJSON surfaces enough data for the orbit to show the "Plan (3 stages)"
// header and the stage-level letters (dev✓ trn⚠ rpt·). The fields are parsed
// from the task row's PlanID / StageID / Needs / Inputs / OutputArtifact so
// the wire does not duplicate the plan engine's internal state.
type planMetaJSON struct {
	PlanID         string   `json:"plan_id,omitempty"`
	StageID        string   `json:"stage_id,omitempty"`
	StageCount     int      `json:"stage_count,omitempty"`
	StageLabels    []string `json:"stage_labels,omitempty"`
	Needs          string   `json:"needs,omitempty"`
	OutputArtifact string   `json:"output_artifact,omitempty"`
}

// delegationHopJSON is one leg of a task's travel — the UI draws these as an
// ordered flow ("macbook-m1 → orangepi3b → win-desktop") with chip glyphs and
// hop numbers. hop=1 is the originator → first executor leg. Accepted=true
// means the peer node's handleTaskAccept wrote this event back via the
// authenticated bus; the absence of an accept leg (only outbound) means the
// hop was attempted but the peer never acknowledged.
type delegationHopJSON struct {
	Hop      int    `json:"hop"`
	FromNode string `json:"from_node"`
	ToNode   string `json:"to_node"`
	Via      string `json:"via,omitempty"` // "direct" | "retarget"
	Accepted bool   `json:"accepted"`
	TS       int64  `json:"ts"`
}

// supervisionJSON rolls up the latest supervision_round event so the orbit's
// progress badge can say "Round 1 / 5" without re-scanning traces on the
// client. latest_verdict is absent until at least one round has completed.
type supervisionJSON struct {
	Round         int    `json:"round"`
	Budget        int    `json:"budget"`
	LatestVerdict string `json:"latest_verdict,omitempty"`
}

// tier2OpJSON is a single irreversible operation surfaced by the defense
// layer's tier2_triggered event. The UI renders these in the review dock so
// users see exactly what they're signing off on before clicking Approve.
type tier2OpJSON struct {
	Op     string `json:"op"`
	Target string `json:"target,omitempty"`
	Risk   string `json:"risk"` // "high" | "medium" | "low"
}

func toTaskJSON(t core.Task) taskJSON {
	out := taskJSON{
		ID:           t.TaskID,
		ParentID:     t.ParentID,
		Project:      t.Project,
		Title:        t.Title,
		State:        t.State,
		Owner:        t.OwnerNode,
		AttemptID:    t.AttemptID,
		Intent:       t.Intent,
		Spec:         t.SpecJSON,
		Result:       t.ResultJSON,
		Risk:         t.Risk,
		Priority:     priorityLabel(t.Priority),
		Seq:          t.Seq,
		SessionID:    t.SessionID,
		ResourceKeys: t.ResourceKeys,
		Scheduled:    t.Scheduled,
		CreatedAt:    ts(t.CreatedAt),
		UpdatedAt:    ts(t.UpdatedAt),
	}
	if t.PlanID != "" || t.StageID != "" || t.OutputArtifact != "" {
		// StageCount/StageLabels are intentionally NOT derived here: Needs is
		// this stage's dependency list, not the plan's stage list, so
		// len(Needs) would label the last stage of a 3-stage plan "2 stages".
		// getTask fills them from the canonical stage rows via enrichPlanMeta.
		out.PlanMeta = &planMetaJSON{
			PlanID:         t.PlanID,
			StageID:        t.StageID,
			Needs:          firstNeed(t.Needs),
			OutputArtifact: t.OutputArtifact,
		}
	}
	return out
}

// enrichPlanMeta fills StageCount/StageLabels from the plan engine's canonical
// stage rows. The detail endpoint pays one indexed query (idx_tasks_plan) to
// get the badge right; listTasks skips this to avoid N+1 on the queue board,
// where plan_meta has no consumer today. On a delegated executor node the
// store holds only the local stage row, so the count degrades to 1 — the
// orchestrator's view is the complete one, and that is where users watch.
func (h *handler) enrichPlanMeta(ctx context.Context, out *taskJSON, t core.Task) {
	if out.PlanMeta == nil || t.PlanID == "" {
		return
	}
	stages, err := h.store.PlanStages(ctx, t.PlanID)
	if err != nil || len(stages) == 0 {
		return // visibility hint only: never fail the endpoint over it
	}
	labels := make([]string, 0, len(stages))
	for _, st := range stages {
		labels = append(labels, st.StageID)
	}
	out.PlanMeta.StageCount = len(stages)
	out.PlanMeta.StageLabels = labels
}

// firstNeed collapses the Needs list (possibly several keys) to a single
// display string for the orbit's "need this" rail. Empty string when nothing
// was declared.
func firstNeed(needs []string) string {
	if len(needs) == 0 {
		return ""
	}
	return needs[0]
}

// priorityLabel maps the stored numeric priority to its wire label; unknown
// values report normal rather than breaking the board.
func priorityLabel(p int) string {
	switch p {
	case core.PriorityHigh:
		return "high"
	case core.PriorityLow:
		return "low"
	default:
		return "normal"
	}
}

// parsePriority is priorityLabel's inverse; ok=false on unknown labels.
func parsePriority(label string) (int, bool) {
	switch label {
	case "high":
		return core.PriorityHigh, true
	case "normal":
		return core.PriorityNormal, true
	case "low":
		return core.PriorityLow, true
	}
	return 0, false
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

// visibleTraceTypes is the 8-track set the decision-orbit component paints.
// Mirrors the map in events.go (same package, kept here so getTask's filter
// is obvious without chasing a separate file).
var visibleTraceTypes = map[string]bool{
	"reasoning":          true,
	"classify_result":    true,
	"route_decision":     true,
	"delegation_hop":     true,
	"exec_agent_start":   true,
	"supervision_round":  true,
	"tier2_triggered":    true,
	"plan_stage_changed": true,
	"artifact_transfer":  true,
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
	h.enrichPlanMeta(r.Context(), &out, t)
	if events, err := h.store.Events(r.Context(), id); err == nil {
		var hops []delegationHopJSON
		hopsSeen := make(map[string]bool) // key = from_node + "|" + to_node
		var latestSupervision *supervisionJSON
		var tierOps []tier2OpJSON
		for _, e := range events {
			out.Events = append(out.Events, eventJSON{TS: e.TS, Type: e.Type, Data: e.DataJSON})
			if visibleTraceTypes[e.Type] {
				te := traceEventJSON{ID: e.ID, TS: e.TS, Type: e.Type}
				if e.DataJSON != "" {
					var decoded any
					if err := json.Unmarshal([]byte(e.DataJSON), &decoded); err == nil {
						te.Data = decoded
					} else {
						te.DataRaw = e.DataJSON
					}
				}
				out.Traces = append(out.Traces, te)
				// — Structured aggregation: each visible track contributes data
				//    to one or more rollup fields. Never fail the endpoint over
				//    bad JSON — these are visibility hints only.
				switch e.Type {
				case "route_decision":
					var payload struct {
						Self  string   `json:"self_id"`
						Chain []string `json:"chain"`
					}
					if json.Unmarshal([]byte(e.DataJSON), &payload) == nil {
						// Route chain represents the visited path BEFORE this
						// decision — render each adjacent pair as a synthetic
						// hop so the delegation trail doesn't have gaps where
						// the route phase skipped the delegation bus.
						prev := payload.Self
						for i, n := range payload.Chain {
							if n == "" || n == prev {
								continue
							}
							key := prev + "|" + n
							if hopsSeen[key] {
								prev = n
								continue
							}
							hopsSeen[key] = true
							hops = append(hops, delegationHopJSON{
								Hop:      len(hops) + 1,
								FromNode: prev,
								ToNode:   n,
								Via:      "route",
								Accepted: true,
								TS:       e.TS,
							})
							_ = i
							prev = n
						}
					}
				case "delegation_hop":
					var payload struct {
						From string `json:"from_node"`
						To   string `json:"to_node"`
						Via  string `json:"via"`
					}
					// Back-compat with legacy rows that still use short names
					// (pre-0.0.7-beta trace shape). Accept both forms.
					if json.Unmarshal([]byte(e.DataJSON), &payload) != nil || (payload.From == "" && payload.To == "") {
						var legacy struct {
							From string `json:"from"`
							To   string `json:"to"`
							Via  string `json:"via"`
						}
						if json.Unmarshal([]byte(e.DataJSON), &legacy) == nil {
							payload.From = legacy.From
							payload.To = legacy.To
							payload.Via = legacy.Via
						}
					}
					if payload.From != "" || payload.To != "" {
						key := payload.From + "|" + payload.To
						if !hopsSeen[key] {
							hopsSeen[key] = true
							via := payload.Via
							if via == "" {
								via = "direct"
							}
							// handleTaskAccept writes on the TO node: it is
							// the acknowledgement leg. All other write sites
							// are outbound dispatches → accepted=false until the
							// accept event rounds the bus back.
							accepted := false
							hops = append(hops, delegationHopJSON{
								Hop:      len(hops) + 1,
								FromNode: payload.From,
								ToNode:   payload.To,
								Via:      via,
								Accepted: accepted,
								TS:       e.TS,
							})
						}
					}
				case "supervision_round":
					var payload struct {
						Round   int    `json:"round"`
						Budget  int    `json:"budget"`
						Verdict string `json:"verdict"`
						Status  string `json:"verdict_status"`
					}
					if json.Unmarshal([]byte(e.DataJSON), &payload) == nil {
						verdict := payload.Verdict
						if verdict == "" {
							verdict = payload.Status
						}
						latestSupervision = &supervisionJSON{
							Round:         payload.Round,
							Budget:        payload.Budget,
							LatestVerdict: verdict,
						}
					}
				case "tier2_triggered":
					var payload struct {
						Operations []struct {
							Op     string `json:"op"`
							Target string `json:"target"`
							Risk   string `json:"risk"`
						} `json:"operations"`
					}
					if json.Unmarshal([]byte(e.DataJSON), &payload) == nil {
						for _, op := range payload.Operations {
							tierOps = append(tierOps, tier2OpJSON{
								Op:     op.Op,
								Target: op.Target,
								Risk:   op.Risk,
							})
						}
					}
				}
			}
		}
		if len(hops) > 0 {
			out.DelegationChain = hops
		}
		if latestSupervision != nil {
			out.Supervision = latestSupervision
		}
		if len(tierOps) > 0 {
			out.Tier2Ops = tierOps
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

// rejectTask rejects a reviewed task (review -> failed). The reason is
// optional and may arrive as a JSON body {reason} (the web form) or the
// legacy ?reason= query parameter (curl one-liners).
func (h *handler) rejectTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	reason := r.URL.Query().Get("reason")
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // empty/invalid body falls back to the query param
		if body.Reason != "" {
			reason = body.Reason
		}
	}
	if err := h.store.Reject(r.Context(), id, reason); err != nil {
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
