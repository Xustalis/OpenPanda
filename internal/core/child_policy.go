// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// ChildGrant is a bounded execution grant, bound to the causal source attempt.
// It is included in the signed task spec on the existing authenticated bus.
// Personal memory is deliberately not a delegable capability.
type ChildGrant struct {
	Version       int      `json:"version"`
	SourceTask    string   `json:"source_task"`
	SourceAttempt string   `json:"source_attempt"`
	Project       string   `json:"project"`
	Paths         []string `json:"paths,omitempty"`
	Abilities     []string `json:"abilities"`
	Operations    []string `json:"operations"`
	InputVersion  string   `json:"input_version,omitempty"`
}

func constrainChildGrant(parent Task, in TaskInput) (TaskInput, error) {
	var parentSpec map[string]json.RawMessage
	if parent.SpecJSON != "" {
		if err := json.Unmarshal([]byte(parent.SpecJSON), &parentSpec); err != nil {
			return in, fmt.Errorf("invalid parent execution policy: %w", err)
		}
	}
	// Reject escaping/absolute paths before normalization; normalization is for
	// resource lock identities and intentionally clamps malformed legacy input.
	for _, p := range in.Paths {
		p = strings.ReplaceAll(p, "\\", "/")
		cleaned := path.Clean(p)
		if strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, ":") {
			return in, fmt.Errorf("child path escapes project: %q", p)
		}
	}
	in.Paths = normalizeScopePaths(in.Paths)
	ceiling := parent.ScopePaths
	if len(ceiling) == 0 {
		var scope string
		if err := json.Unmarshal(parentSpec["scope"], &scope); err == nil && strings.TrimSpace(scope) != "" {
			// The model schema defines scope as comma-separated relative paths.
			// Carry it into the machine-enforced grant even for legacy roots
			// that did not populate scope_paths_json.
			for _, p := range strings.Split(scope, ",") {
				p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
				cleaned := path.Clean(p)
				if strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, ":") || strings.ContainsAny(cleaned, "*?[]") {
					return in, fmt.Errorf("parent scope must contain bounded relative paths before delegation")
				}
			}
			if len(strings.Split(scope, ",")) > maxScopePaths {
				return in, fmt.Errorf("parent scope exceeds delegation path limit")
			}
			ceiling = normalizeScopePaths(strings.Split(scope, ","))
		}
	}
	if len(ceiling) > 0 {
		if len(in.Paths) == 0 {
			in.Paths = append([]string(nil), ceiling...)
		}
		for _, p := range in.Paths {
			allowed := false
			for _, base := range ceiling {
				if base == "" || p == base || strings.HasPrefix(p, base+"/") {
					allowed = true
					break
				}
			}
			if !allowed {
				return in, fmt.Errorf("child scope %q exceeds parent grant", p)
			}
		}
	}
	abilities := append([]string(nil), parent.Requires...)
	var inherited ChildGrant
	if json.Unmarshal(parentSpec["child_grant"], &inherited) == nil && inherited.Version == 1 {
		abilities = append([]string(nil), inherited.Abilities...)
	}
	canExecute := parent.Authorized && in.inheritExecutionGrant
	for _, ability := range in.Requires {
		if !slices.Contains(abilities, ability) {
			canExecute = false
		}
	}
	// A marker/programmatic spawn never inherits blanket consent. Explicit
	// caller consent is still honored locally, bounded by the parent's scope.
	in.Authorized = in.Authorized || canExecute
	ops := []string{"read", "reversible"}
	if in.Authorized {
		ops = append(ops, "execute")
	}
	grant := ChildGrant{Version: 1, SourceTask: parent.TaskID, SourceAttempt: parent.AttemptID, Project: parent.Project, Paths: in.Paths, Abilities: abilities, Operations: ops, InputVersion: in.childInputVersion}
	var spec map[string]any
	if in.SpecJSON != "" {
		if err := json.Unmarshal([]byte(in.SpecJSON), &spec); err != nil {
			return in, err
		}
	}
	if spec == nil {
		spec = make(map[string]any)
	}
	// Preserve the parent's policy envelope as well as its structured paths.
	// Text constraints are inherited verbatim; they are never interpreted as
	// permission grants or converted into scores by the control plane.
	for _, key := range []string{"scope", "permission_mode"} {
		if raw := parentSpec[key]; len(raw) > 0 && string(raw) != "null" && string(raw) != "\"\"" {
			spec[key] = raw
		}
	}
	if len(in.Paths) > 0 {
		// Execution drift checks and the signed path grant must describe the
		// same narrowed scope, rather than retaining a broader ancestor scope.
		spec["scope"] = strings.Join(in.Paths, ",")
	}
	var constraints []string
	_ = json.Unmarshal(parentSpec["constraints"], &constraints)
	if child, ok := spec["constraints"].([]any); ok {
		for _, v := range child {
			if text, ok := v.(string); ok && !slices.Contains(constraints, text) {
				constraints = append(constraints, text)
			}
		}
	}
	if len(constraints) > 0 {
		spec["constraints"] = constraints
	}
	var agents []string
	_ = json.Unmarshal(parentSpec["allow_agents"], &agents)
	if len(agents) > 0 {
		if child, ok := spec["allow_agents"].([]any); ok && len(child) > 0 {
			for _, v := range child {
				name, ok := v.(string)
				if !ok || !slices.Contains(agents, name) {
					return in, fmt.Errorf("child agent exceeds parent grant")
				}
			}
		} else {
			spec["allow_agents"] = agents
		}
	}
	var tools string
	_ = json.Unmarshal(parentSpec["tools_policy"], &tools)
	if tools == "minimal" {
		spec["tools_policy"] = tools
	}
	var turns int
	_ = json.Unmarshal(parentSpec["max_turns"], &turns)
	if turns > 0 {
		child, _ := spec["max_turns"].(float64)
		if child <= 0 || child > float64(turns) {
			spec["max_turns"] = turns
		}
	}
	spec["child_grant"] = grant
	raw, err := json.Marshal(spec)
	in.SpecJSON = string(raw)
	return in, err
}

func (s *TaskStore) childRequest(ctx context.Context, parent, attempt, request, digest string) (Task, bool, error) {
	var hash, child string
	err := s.db.QueryRowContext(ctx, `SELECT input_hash,child_id FROM task_child_requests WHERE parent_id=? AND attempt_id=? AND request_id=?`, parent, attempt, request).Scan(&hash, &child)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	if hash != digest {
		return Task{}, true, fmt.Errorf("request_id reused with different input")
	}
	t, err := s.Get(ctx, child)
	return t, true, err
}

// Creation, policy, quota reservation and idempotency association are atomic.
// No partially initialized submitted task can escape through crash recovery.
func (s *TaskStore) createChildTask(ctx context.Context, parent Task, in TaskInput, chain []string, depth, cap int, limits config.DelegationConfig) (Task, error) {
	id, err := util.UUIDv7()
	if err != nil {
		return Task{}, err
	}
	attempt, err := util.UUIDv7()
	if err != nil {
		return Task{}, err
	}
	d := in.detail()
	encode := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var currentAttempt, state, waiting string
		var tokens int64
		var hops int
		if err := tx.QueryRowContext(ctx, `SELECT attempt_id,state,token_budget,delegation_budget,children_wait_attempt FROM tasks WHERE task_id=?`, parent.TaskID).Scan(&currentAttempt, &state, &tokens, &hops, &waiting); err != nil {
			return err
		}
		if currentAttempt != parent.AttemptID || Terminal(state) || (in.childRequestID != "" && (state != StateRunning || waiting == currentAttempt)) {
			return fmt.Errorf("parent attempt no longer permits coordination")
		}
		if in.childRequestID != "" {
			var child, digest string
			err := tx.QueryRowContext(ctx, `SELECT child_id,input_hash FROM task_child_requests WHERE parent_id=? AND attempt_id=? AND request_id=?`, parent.TaskID, parent.AttemptID, in.childRequestID).Scan(&child, &digest)
			if err == nil {
				if digest != in.childRequestHash {
					return fmt.Errorf("request_id reused with different input")
				}
				id = child
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var total, active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN state NOT IN ('done','failed','cancelled') THEN 1 ELSE 0 END),0) FROM tasks WHERE parent_id=?`, parent.TaskID).Scan(&total, &active); err != nil {
			return err
		}
		if total >= limits.MaxChildren || active >= limits.FanOut {
			return fmt.Errorf("child fanout or submission limit reached")
		}
		if in.TokenAllocation < 0 || in.DelegationAllocation < 0 {
			return fmt.Errorf("child allocations must be non-negative")
		}
		childTokens := in.TokenAllocation
		if tokens < 0 {
			return fmt.Errorf("parent token budget exhausted")
		}
		if tokens > 0 {
			if childTokens == 0 {
				childTokens = max(int64(1), tokens/int64(limits.FanOut+1))
			}
			if childTokens >= tokens {
				return fmt.Errorf("child token allocation must leave a parent resume reserve")
			}
			left := tokens - childTokens
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET token_budget=? WHERE task_id=? AND attempt_id=?`, left, parent.TaskID, parent.AttemptID); err != nil {
				return err
			}
		}
		if hops == 0 && len(parent.Chain) <= 1 {
			hops = scheduler.MaxDelegationBudget
		}
		if hops <= 0 {
			return fmt.Errorf("parent delegation budget exhausted")
		}
		childHops := in.DelegationAllocation
		if childHops == 0 {
			childHops = max(1, hops/(limits.FanOut+1))
		}
		if childHops > hops {
			return fmt.Errorf("child delegation allocation exceeds parent remainder")
		}
		remainingHops := hops - childHops
		if remainingHops == 0 {
			remainingHops = -1
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET delegation_budget=? WHERE task_id=? AND attempt_id=?`, remainingHops, parent.TaskID, parent.AttemptID); err != nil {
			return err
		}
		now := s.now()
		_, err := tx.ExecContext(ctx, `INSERT INTO tasks(task_id,parent_id,project,title,state,owner_node,attempt_id,state_version,chain_json,created_at,updated_at,spawn_depth,spawn_cap,scope_paths_json,isolate,work_dir,session_id,authorized,context_type,context_hash,intent,spec_json,complexity,risk,resource_json,requires_json,transport,deadline_unix,delegation_budget,token_budget)
   VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, parent.TaskID, in.Project, in.Title, StateSubmitted, parent.OwnerNode, attempt, 0, encode(chain), now, now, depth, cap, encode(in.Paths), in.Isolate, in.WorkDir, in.SessionID, in.Authorized, d.ContextType, d.ContextHash, d.Intent, d.SpecJSON, d.Complexity, d.Risk, d.ResourceJSON, encode(d.Requires), d.Transport, d.DeadlineUnix, childHops, childTokens)
		if err != nil {
			return err
		}
		if _, err := s.attachAskRoundTx(ctx, tx, id); err != nil {
			return err
		}
		if in.childRequestID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO task_child_requests(parent_id,attempt_id,request_id,input_hash,child_id,created_at) VALUES(?,?,?,?,?,?)`, parent.TaskID, parent.AttemptID, in.childRequestID, in.childRequestHash, id, now); err != nil {
				return err
			}
		}
		if err := s.recordEventTx(ctx, tx, id, EvSubmit, map[string]any{"parent": parent.TaskID, "owner": parent.OwnerNode}); err != nil {
			return err
		}
		return s.recordEventTx(ctx, tx, id, "spawn_child_task", map[string]any{"parent_id": parent.TaskID, "source_attempt": parent.AttemptID, "sub_main": parent.OwnerNode, "chain": chain, "token_allocation": childTokens, "delegation_allocation": childHops, "input_version": in.childInputVersion})
	})
	if err != nil {
		return Task{}, err
	}
	return s.Get(ctx, id)
}

// Version describes the actual execution tree, excluding the same private
// metadata and secrets as artifact packing. Hash map JSON has stable key order.
func (c *Core) childWorkspaceVersion(ctx context.Context, id string) (string, error) {
	dir, err := c.childWorkspaceSource(ctx, id)
	if err != nil || dir == "" {
		return "", err
	}
	hashes, err := c.hashWorkTree(dir)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(hashes)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func objectivePolicyForView(t Task) any {
	var spec map[string]any
	_ = json.Unmarshal([]byte(t.SpecJSON), &spec)
	return spec["objectives"]
}
