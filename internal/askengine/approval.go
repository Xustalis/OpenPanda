package askengine

// Remembered approval decisions and their resolution into the tier-2 gate.
//
// An approval card answers two questions, not one: the choice (approve/deny)
// and the scope it applies to (this prompt only, this session, or the whole
// project). A remembered answer is replayed by the gate instead of re-prompting
// — "the user's choice becomes the default for what comes after" — until it is
// cleared or the project changes its policy.
//
// Resolution order, from most specific to least:
//
//  1. a session-scoped remembered decision (this conversation only, in memory);
//  2. the project row's remembered decision (persists across sessions);
//  3. an explicit standing grant (--authorize / /authorize / the panel's
//     authorize checkbox);
//  4. nothing — the task parks in review for a foreground answer.
//
// The effective mode resolves alongside: the project's approval_mode overrides
// the global approval.mode when set. Two modes deliberately ignore remembered
// decisions: "never" needs no answer (consent is implied) and "always" exists
// to force a foreground choice every time, so a stored answer must not satisfy
// it — the prompt still comes up.

import (
	"errors"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/config"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
)

// ErrApprovalDenied marks a refusal that came from a remembered deny — the
// gate answered "no" before a task existed, so callers can surface it as a
// policy refusal (HTTP 409, a denied turn) instead of a generic failure.
var ErrApprovalDenied = errors.New("tier-2 execution denied by a remembered approval decision")

// approvalVerdict is the resolved tier-2 gate context for one ask.
type approvalVerdict struct {
	// mode is the effective approval mode: the project's override when it
	// sets one, else the global approval.mode.
	mode string
	// scope is where a "remember" writes by default — the project's
	// configured scope, reading as session when unset.
	scope string
	// decision is a remembered answer (approve|deny) or "".
	decision string
	// decisionScope records which store supplied decision (session|project),
	// so surfaces can say "remembered for this session" honestly.
	decisionScope string
}

// decisionKey scopes a session decision to its conversation AND its project:
// a bare REPL's remembered answer must not leak into a named session or a
// different project, and a panel chat's must not follow the user elsewhere.
func decisionKey(sessionID, project string) string {
	return sessionID + "\x00" + project
}

// scopeProject resolves the project an ask lands in — the scope's explicit one,
// or the ambient entered project when the caller asked for the fallback. It is
// the same resolution submitTask performs, kept in one place so the tool gate
// and the task gate cannot disagree about which project's policy applies.
func (e *Engine) scopeProject(scope AskScope) (project, projectDir string) {
	project, projectDir = scope.Project, ""
	if scope.AmbientProject {
		project, projectDir = e.Project()
	}
	return project, projectDir
}

// approvalFor resolves the gate context for (sessionID, project). A session
// decision wins over the project row's because the more specific context owns
// the more recent choice; a project-less ask reads only its session bucket.
func (e *Engine) approvalFor(sessionID, project string) approvalVerdict {
	v := approvalVerdict{
		mode:  config.ApprovalModeOnRequest,
		scope: projectstore.ScopeSession,
	}
	if e.cfg != nil {
		v.mode = e.cfg.Approval.NormalizedMode()
	}
	if project != "" && e.projStore != nil {
		if p, err := e.projStore.Get(project); err == nil {
			if p.ApprovalMode != "" {
				v.mode = config.ApprovalConfig{Mode: p.ApprovalMode}.NormalizedMode()
			}
			v.scope = p.NormalizedScope()
			v.decision = p.ApprovalDecision
			if v.decision != "" {
				v.decisionScope = projectstore.ScopeProject
			}
		}
	}
	e.decisionsMu.RLock()
	d := e.decisions[decisionKey(sessionID, project)]
	e.decisionsMu.RUnlock()
	if d != "" {
		v.decision = d
		v.decisionScope = projectstore.ScopeSession
	}
	return v
}

// gate resolves a verdict plus an explicit grant into the effective consent:
// (authorized, denied). never auto-consents; always withholds — a remembered
// decision must not silently satisfy a mode that exists to force a foreground
// choice. In on-request a remembered approve consents standing; a remembered
// deny refuses before any task exists, so the loop cannot burn rounds
// re-asking a question the user already answered.
func (v approvalVerdict) gate(grant bool) (authorized, denied bool) {
	switch v.mode {
	case config.ApprovalModeNever:
		return true, false
	case config.ApprovalModeAlways:
		return false, false
	default:
		switch v.decision {
		case projectstore.DecisionApprove:
			return true, false
		case projectstore.DecisionDeny:
			return false, true
		}
		return grant, false
	}
}

// ApprovalState is the effective approval policy for (sessionID, project) as a
// surface sees it — what the /approval command and the panel render.
type ApprovalState struct {
	Mode          string `json:"mode"`           // effective normalized mode
	Scope         string `json:"scope"`          // default remember scope
	Decision      string `json:"decision"`       // remembered answer, "" when none
	DecisionScope string `json:"decision_scope"` // where the decision lives: session|project|""
	Project       string `json:"project"`
}

// ApprovalState resolves the gate context for one (session, project) pair.
func (e *Engine) ApprovalState(sessionID, project string) ApprovalState {
	v := e.approvalFor(sessionID, project)
	return ApprovalState{
		Mode:          v.mode,
		Scope:         v.scope,
		Decision:      v.decision,
		DecisionScope: v.decisionScope,
		Project:       project,
	}
}

// RememberApproval stores a tier-2 answer at the given scope. "session" keeps
// it in memory, dying with the process (a REPL's lifetime, a panel chat's);
// "project" persists it on the project row where every later session finds it;
// "once" writes nothing. decision is approve|deny.
func (e *Engine) RememberApproval(sessionID, project, scope, decision string) error {
	if err := projectstore.ValidateApprovalDecision(decision); err != nil {
		return err
	}
	if decision == "" {
		return e.ClearApproval(sessionID, project)
	}
	switch scope {
	case projectstore.ScopeProject:
		if project == "" {
			return fmt.Errorf("askengine: project-scope approval needs a project context")
		}
		if e.projStore == nil {
			return fmt.Errorf("askengine: project store unavailable")
		}
		return e.projStore.SetApprovalDecision(project, decision)
	case projectstore.ScopeOnce:
		return nil
	default: // session (and any unlabeled remember)
		e.decisionsMu.Lock()
		if e.decisions == nil {
			e.decisions = make(map[string]string)
		}
		e.decisions[decisionKey(sessionID, project)] = decision
		e.decisionsMu.Unlock()
		return nil
	}
}

// ClearApproval forgets remembered decisions for (sessionID, project): the
// session bucket plus the project row's stored answer. Both, so "forget"
// really forgets no matter which scope the user last remembered into.
func (e *Engine) ClearApproval(sessionID, project string) error {
	e.decisionsMu.Lock()
	delete(e.decisions, decisionKey(sessionID, project))
	e.decisionsMu.Unlock()
	if project != "" && e.projStore != nil {
		return e.projStore.SetApprovalDecision(project, "")
	}
	return nil
}

// SetProjectApprovalPolicy writes a project's mode/scope overrides through the
// engine's own project store, so CLI and panel callers do not need a second
// handle to the same table. Empty fields keep their current values — a caller
// setting only the scope must not wipe the mode.
func (e *Engine) SetProjectApprovalPolicy(project, mode, scope string) error {
	if e.projStore == nil {
		return fmt.Errorf("askengine: project store unavailable")
	}
	p, err := e.projStore.Get(project)
	if err != nil {
		return err
	}
	if mode == "" {
		mode = p.ApprovalMode
	}
	if scope == "" {
		scope = p.ApprovalScope
	}
	return e.projStore.SetApprovalPolicy(project, mode, scope)
}

// scopeLabel renders a scope/decision pair for user-facing notes. It is kept
// deliberately terse — it rides along inside a longer line.
func scopeLabel(scope string) string {
	switch scope {
	case projectstore.ScopeProject:
		return "project"
	case projectstore.ScopeOnce:
		return "once"
	default:
		return "session"
	}
}

// NeedsForeground reports whether the effective mode still wants a per-task
// decision even when a remembered approve exists — mode=always is the only
// policy that deliberately outranks a stored answer. Surfaces use it to keep
// showing the approval card instead of trusting ConsentSource blindly.
func (v approvalVerdict) NeedsForeground() bool {
	return v.mode == config.ApprovalModeAlways
}
