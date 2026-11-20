// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/defense"
)

// ChildRPCRequest is a task-attempt capability protocol, exposed only on a
// random loopback port for the lifetime of the executing parent. No database
// mutation fallback may substitute for this live orchestration endpoint.
type ChildRPCRequest struct {
	InputVersion     string   `json:"input_version,omitempty"`
	TokenBudget      int64    `json:"token_budget,omitempty"`
	DelegationBudget int      `json:"delegation_budget,omitempty"`
	Operation        string   `json:"operation"`
	RequestID        string   `json:"request_id,omitempty"`
	ParentID         string   `json:"parent_id,omitempty"`
	AttemptID        string   `json:"attempt_id,omitempty"`
	TaskID           string   `json:"task_id,omitempty"`
	Title            string   `json:"title,omitempty"`
	Prompt           string   `json:"prompt,omitempty"`
	Requires         []string `json:"requires,omitempty"`
	Project          string   `json:"project,omitempty"`
	Preferred        string   `json:"preferred,omitempty"`
	Node             string   `json:"node,omitempty"`
	Paths            []string `json:"paths,omitempty"`
	Isolate          bool     `json:"isolate,omitempty"`
	TimeoutS         int      `json:"timeout_s,omitempty"`
}

type childRPCServer struct {
	c      *Core
	ctx    context.Context
	parent Task
	token  string
}

func (s *childRPCServer) decisionLock() *sync.Mutex {
	hash := sha256.Sum256([]byte(s.parent.TaskID))
	return &s.c.childDecisionLocks[int(hash[0])%len(s.c.childDecisionLocks)]
}

func (c *Core) bindChildRPC(ctx context.Context, parent Task) (context.Context, func(), error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return ctx, nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		_ = listener.Close()
		return ctx, nil, err
	}
	s := &childRPCServer{c: c, ctx: ctx, parent: parent, token: hex.EncodeToString(secret)}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = server.Serve(listener) }()
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	cleanup := func() { stop(); _ = server.Close() }
	return commander.WithChildRPC(ctx, "http://"+listener.Addr().String(), s.token, parent.AttemptID), cleanup, nil
}

func (s *childRPCServer) current() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	parent, err := s.c.store.Get(s.ctx, s.parent.TaskID)
	if err != nil {
		return err
	}
	if parent.AttemptID != s.parent.AttemptID || parent.OwnerNode != s.c.nodeID || parent.State != StateRunning {
		return fmt.Errorf("parent attempt is no longer executing")
	}
	return nil
}

func (s *childRPCServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/children" {
		http.NotFound(w, r)
		return
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(auth), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req ChildRPCRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid child request", http.StatusBadRequest)
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid trailing child request", http.StatusBadRequest)
		return
	}
	if (req.ParentID != "" && req.ParentID != s.parent.TaskID) || req.AttemptID != s.parent.AttemptID {
		http.Error(w, "task attempt mismatch", http.StatusConflict)
		return
	}
	if err := s.current(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	var out any
	var err error
	switch req.Operation {
	case "spawn":
		out, err = s.spawn(r.Context(), req)
	case "await":
		out, err = s.await(r.Context(), req)
	case "cancel":
		lock := s.decisionLock()
		lock.Lock()
		defer lock.Unlock()
		if err := s.current(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var child Task
		child, err = s.c.store.Get(r.Context(), req.TaskID)
		if err == nil && child.ParentID != s.parent.TaskID {
			err = fmt.Errorf("task is not a child of this parent")
		}
		if err == nil {
			out, err = s.c.CancelTree(r.Context(), child.TaskID)
		}
	default:
		err = fmt.Errorf("unknown child operation")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *childRPCServer) spawn(ctx context.Context, req ChildRPCRequest) (any, error) {
	lock := s.decisionLock()
	lock.Lock()
	defer lock.Unlock()
	if err := s.current(); err != nil {
		return nil, err
	}
	var waiting string
	if err := s.c.store.db.QueryRowContext(ctx, "SELECT children_wait_attempt FROM tasks WHERE task_id=?", s.parent.TaskID).Scan(&waiting); err != nil {
		return nil, err
	}
	if waiting == s.parent.AttemptID {
		return nil, fmt.Errorf("parent is awaiting children")
	}
	if taskPermissionMode(s.parent.SpecJSON) == defense.ModePlan || commander.Restricted(s.ctx) || (s.parent.Remote && !s.parent.Authorized) {
		return nil, fmt.Errorf("read-only parent cannot spawn execution children")
	}
	if req.RequestID == "" || len(req.RequestID) > 200 {
		return nil, fmt.Errorf("bounded request_id is required")
	}
	if strings.TrimSpace(req.Title) == "" {
		return nil, fmt.Errorf("title required")
	}
	if req.Project != "" && req.Project != s.parent.Project {
		return nil, fmt.Errorf("child cannot change parent project")
	}
	fingerprint := req
	fingerprint.RequestID = ""
	raw, _ := json.Marshal(fingerprint)
	digest := sha256.Sum256(raw)
	old, found, err := s.c.store.childRequest(ctx, s.parent.TaskID, s.parent.AttemptID, req.RequestID, hex.EncodeToString(digest[:]))
	if err != nil {
		return nil, err
	}
	if found {
		// A crash between the atomic creation and dispatch leaves a fully
		// initialized submitted row. Its CAS transition safely completes dispatch.
		if old.State == StateSubmitted {
			in := TaskInput{Title: old.Title, Intent: old.Intent, Requires: old.Requires, SpecJSON: old.SpecJSON, TargetNode: req.Node, PreferredNode: req.Preferred, Isolate: old.Isolate}
			if err := s.c.DispatchChild(s.ctx, old, in); err != nil {
				return nil, err
			}
		}
		latest, err := s.c.store.Get(ctx, old.TaskID)
		return childRPCView(latest), err
	}
	version, err := s.c.childWorkspaceVersion(ctx, s.parent.TaskID)
	if err != nil {
		return nil, err
	}
	if req.InputVersion != "" && req.InputVersion != version {
		return nil, fmt.Errorf("parent input version changed; refresh inputs before coordination")
	}
	children, err := s.c.store.Children(ctx, s.parent.TaskID)
	if err != nil {
		return nil, err
	}
	limits := s.c.delegateLimits()
	active := 0
	for _, child := range children {
		if child.State != StateDone && child.State != StateFailed && child.State != StateCancelled && child.State != StateExpired {
			active++
		}
	}
	if len(children) >= limits.MaxChildren || active >= limits.FanOut {
		return nil, fmt.Errorf("child fanout or submission limit reached")
	}
	intent := req.Prompt
	if strings.TrimSpace(intent) == "" {
		intent = req.Title
	}
	requires := req.Requires
	if len(requires) == 0 {
		requires = []string{"coding"}
	}
	in := TaskInput{Title: req.Title, Intent: intent, Requires: requires, Project: s.parent.Project, SessionID: s.parent.SessionID, Transport: s.parent.Transport, DeadlineUnix: s.parent.DeadlineUnix, PreferredNode: req.Preferred, TargetNode: req.Node, Paths: req.Paths, Isolate: req.Isolate, UserLocale: s.parent.GetUserLocale(), TokenAllocation: req.TokenBudget, DelegationAllocation: req.DelegationBudget, childRequestID: req.RequestID, childRequestHash: hex.EncodeToString(digest[:]), childInputVersion: version, inheritExecutionGrant: true}
	child, err := s.c.SpawnChildTask(s.ctx, s.parent.TaskID, in)
	if err != nil {
		return nil, err
	}
	if err := s.c.DispatchChild(s.ctx, child, in); err != nil {
		s.c.failLocal(context.WithoutCancel(s.ctx), child.TaskID, err, child.AttemptID)
		return nil, err
	}
	s.c.EvTrace(context.WithoutCancel(s.ctx), s.parent.TaskID, "child_spawned", map[string]any{"child_id": child.TaskID, "attempt_id": s.parent.AttemptID, "request_id": req.RequestID})
	return childRPCView(child), nil
}

func childRPCView(t Task) map[string]any {
	return map[string]any{"task_id": t.TaskID, "parent_id": t.ParentID, "title": t.Title, "state": t.State, "owner": t.OwnerNode, "attempt": t.AttemptID, "result": t.ResultJSON, "objectives": objectivePolicyForView(t), "output_artifact": t.OutputArtifact, "verification": t.Verification}
}

func (s *childRPCServer) await(ctx context.Context, req ChildRPCRequest) (result any, retErr error) {
	child, err := s.c.store.Get(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	if child.ParentID != s.parent.TaskID {
		return nil, fmt.Errorf("task is not a child of this parent")
	}
	if Terminal(child.State) && !s.c.childOutputDelivery(ctx, child) {
		return childRPCView(child), nil
	}
	lock := s.decisionLock()
	lock.Lock()
	if err := s.current(); err != nil {
		lock.Unlock()
		return nil, err
	}
	resume, err := s.c.yieldForChildren(s.ctx, s.parent, true)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	resumeAgent := s.c.agentConcurrency.YieldTask(s.ctx, s.parent.TaskID)
	// Reacquire in execution order: core capacity/workspace, then adapter.
	// The inverse can hold the last adapter while waiting on a task that
	// already holds the last execution slot and is waiting for that adapter.
	defer resumeAgent()
	defer func() {
		// Child delivery may legitimately advance the input tree. Capture that
		// accepted version, then verify it after reacquiring the parent's locks.
		expected, err := s.c.childWorkspaceVersion(s.ctx, s.parent.TaskID)
		if resumeErr := resume(); resumeErr != nil {
			retErr = resumeErr
			return
		}
		if retErr != nil {
			return
		}
		if err != nil {
			retErr = err
			return
		}
		actual, err := s.c.childWorkspaceVersion(s.ctx, s.parent.TaskID)
		if err != nil {
			retErr = err
			return
		}
		if actual != expected {
			retErr = fmt.Errorf("parent inputs changed while reacquiring execution resources; refresh before resuming")
			return
		}
		if err := s.current(); err != nil {
			retErr = err
			return
		}
		if view, ok := result.(map[string]any); ok {
			view["input_version"] = actual
		}
	}()
	seconds := req.TimeoutS
	if seconds <= 0 {
		seconds = 60
	}
	if seconds > 300 {
		seconds = 300
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := s.current(); err != nil {
			return nil, err
		}
		child, err = s.c.store.Get(ctx, req.TaskID)
		if err != nil {
			return nil, err
		}
		if Terminal(child.State) || child.State == StateReview {
			if err := s.c.deliverChildOutput(ctx, child, childStoredOutput(child)); err != nil {
				return nil, err
			}
			// Delivery persists the artifact hash. The completion row may have
			// been read before the wire handler recorded that separate field.
			child, err = s.c.store.Get(ctx, req.TaskID)
			if err != nil {
				return nil, err
			}
			return childRPCView(child), nil
		}
		select {
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			out := childRPCView(child)
			out["status"] = "await_timeout"
			return out, nil
		case <-tick.C:
		}
	}
}
