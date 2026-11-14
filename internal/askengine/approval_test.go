// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

// newApprovalTestEngine builds an engine with a real project store over
// throwaway storage — the shape approvalFor resolves against. No scheduler,
// no model: the gate is pure policy.
func newApprovalTestEngine(t *testing.T, mode string) *Engine {
	t.Helper()
	db := testdb.OpenFile(t, filepath.Join(t.TempDir(), "t.db"))
	return &Engine{
		cfg:       &config.Config{Approval: config.ApprovalConfig{Mode: mode}},
		projStore: projectstore.NewStore(db),
		decisions: map[string]string{},
	}
}

// TestVerdictGateMatrix pins resolved-mode × remembered-decision semantics:
// never consents even against a remembered deny (the mode is "don't gate"),
// always withholds even with a remembered approve or an explicit grant (the
// mode exists to force a foreground choice), on-request is where a remembered
// answer replays — approve consents, deny refuses before a task exists.
func TestVerdictGateMatrix(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		decision string
		grant    bool
		wantAuth bool
		wantDeny bool
	}{
		{"never auto-consents, nothing remembered", config.ApprovalModeNever, "", false, true, false},
		{"never outranks a remembered deny", config.ApprovalModeNever, projectstore.DecisionDeny, false, true, false},
		{"always ignores a remembered approve", config.ApprovalModeAlways, projectstore.DecisionApprove, false, false, false},
		{"always ignores an explicit grant", config.ApprovalModeAlways, "", true, false, false},
		{"on-request remembered approve consents", config.ApprovalModeOnRequest, projectstore.DecisionApprove, false, true, false},
		{"on-request remembered deny refuses even with a grant", config.ApprovalModeOnRequest, projectstore.DecisionDeny, true, false, true},
		{"on-request explicit grant consents", config.ApprovalModeOnRequest, "", true, true, false},
		{"on-request bare fails closed", config.ApprovalModeOnRequest, "", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := approvalVerdict{mode: tc.mode, decision: tc.decision}
			auth, deny := v.gate(tc.grant)
			if auth != tc.wantAuth || deny != tc.wantDeny {
				t.Fatalf("gate(mode=%q decision=%q grant=%v) = (%v,%v), want (%v,%v)",
					tc.mode, tc.decision, tc.grant, auth, deny, tc.wantAuth, tc.wantDeny)
			}
		})
	}
}

// TestApprovalForSessionOverridesProject is the precedence rule: a decision
// remembered for this conversation beats the project row for this session —
// and changes nothing for other sessions.
func TestApprovalForSessionOverridesProject(t *testing.T) {
	e := newApprovalTestEngine(t, config.ApprovalModeOnRequest)
	if _, err := e.projStore.Create("proj", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := e.RememberApproval("s1", "proj", projectstore.ScopeProject, projectstore.DecisionApprove); err != nil {
		t.Fatalf("project remember: %v", err)
	}
	// The write landed on the project row, so a different session sees it.
	if v := e.approvalFor("s2", "proj"); v.decision != projectstore.DecisionApprove || v.decisionScope != projectstore.ScopeProject {
		t.Fatalf("s2 verdict = %+v, want approve/project", v)
	}
	// A session-scope deny in s1 outranks the project's approve for s1 only.
	if err := e.RememberApproval("s1", "proj", projectstore.ScopeSession, projectstore.DecisionDeny); err != nil {
		t.Fatalf("session remember: %v", err)
	}
	if v := e.approvalFor("s1", "proj"); v.decision != projectstore.DecisionDeny || v.decisionScope != projectstore.ScopeSession {
		t.Fatalf("s1 verdict = %+v, want deny/session", v)
	}
	if v := e.approvalFor("s2", "proj"); v.decision != projectstore.DecisionApprove {
		t.Fatalf("s1's session decision leaked into s2: %+v", v)
	}
	// Clear drops both buckets for (s1, proj) — the row's decision too.
	if err := e.ClearApproval("s1", "proj"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if v := e.approvalFor("s1", "proj"); v.decision != "" {
		t.Fatalf("cleared verdict still remembers %q", v.decision)
	}
	p, _ := e.projStore.Get("proj")
	if p.ApprovalDecision != "" {
		t.Fatalf("project row still remembers %q", p.ApprovalDecision)
	}
}

// TestApprovalForProjectPolicy covers the project override half: the row's
// mode replaces the global one and its scope preselects the remember target;
// a project-less ask inherits the global mode and the session default scope.
func TestApprovalForProjectPolicy(t *testing.T) {
	e := newApprovalTestEngine(t, config.ApprovalModeOnRequest)
	if _, err := e.projStore.Create("strict", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := e.projStore.SetApprovalPolicy("strict", config.ApprovalModeAlways, projectstore.ScopeProject); err != nil {
		t.Fatalf("policy: %v", err)
	}
	v := e.approvalFor("s", "strict")
	if v.mode != config.ApprovalModeAlways || v.scope != projectstore.ScopeProject {
		t.Fatalf("strict verdict = %+v, want always/project", v)
	}
	if !v.NeedsForeground() {
		t.Fatal("mode always must report NeedsForeground")
	}
	v = e.approvalFor("s", "")
	if v.mode != config.ApprovalModeOnRequest || v.scope != projectstore.ScopeSession {
		t.Fatalf("project-less verdict = %+v, want on-request/session", v)
	}
	if v.NeedsForeground() {
		t.Fatal("on-request must not report NeedsForeground")
	}
}

// TestRememberApprovalScopeRules pins the safety edges: a project remember
// needs a project, "once" writes nothing, a session remember stays in its
// (session, project) bucket, and ApprovalState reports what the gate sees.
func TestRememberApprovalScopeRules(t *testing.T) {
	e := newApprovalTestEngine(t, config.ApprovalModeOnRequest)
	if err := e.RememberApproval("s", "", projectstore.ScopeProject, projectstore.DecisionApprove); err == nil {
		t.Fatal("project-scope remember without a project was accepted")
	}
	if err := e.RememberApproval("s", "", projectstore.ScopeOnce, projectstore.DecisionApprove); err != nil {
		t.Fatalf("once remember: %v", err)
	}
	if v := e.approvalFor("s", ""); v.decision != "" {
		t.Fatalf("once wrote a decision: %+v", v)
	}
	if err := e.RememberApproval("s", "", projectstore.ScopeSession, "bogus"); err == nil {
		t.Fatal("invalid decision accepted")
	}
	if _, err := e.projStore.Create("pa", "", ""); err != nil {
		t.Fatalf("create pa: %v", err)
	}
	if _, err := e.projStore.Create("pb", "", ""); err != nil {
		t.Fatalf("create pb: %v", err)
	}
	if err := e.RememberApproval("s1", "pa", projectstore.ScopeSession, projectstore.DecisionApprove); err != nil {
		t.Fatalf("session remember: %v", err)
	}
	if v := e.approvalFor("s2", "pa"); v.decision != "" {
		t.Fatalf("s1's remember leaked to s2: %+v", v)
	}
	if v := e.approvalFor("s1", "pb"); v.decision != "" {
		t.Fatalf("pa's remember leaked to pb: %+v", v)
	}
	st := e.ApprovalState("s1", "pa")
	if st.Decision != projectstore.DecisionApprove || st.DecisionScope != projectstore.ScopeSession ||
		st.Mode != config.ApprovalModeOnRequest || st.Scope != projectstore.ScopeSession || st.Project != "pa" {
		t.Fatalf("ApprovalState = %+v", st)
	}
}

func TestPermissionModeOverridesTheGate(t *testing.T) {
	// The conversation's mode is the most recent intent and outranks a
	// remembered answer, a standing grant and the global approval mode. This
	// is the property that makes the switch honest: flipping to bypass must
	// not require also clearing a remembered deny, and flipping to manual must
	// not be defeated by a remembered approve.
	e := newApprovalTestEngine(t, config.ApprovalModeOnRequest)
	v := e.approvalFor("s1", "").withPermission(defense.ModeBypass)
	if ok, denied := v.gate(false); !ok || denied {
		t.Fatalf("bypass: gate = (%v,%v), want (true,false)", ok, denied)
	}
	if err := e.RememberApproval("s1", "", projectstore.ScopeSession, projectstore.DecisionApprove); err != nil {
		t.Fatalf("remember: %v", err)
	}
	for _, mode := range []string{defense.ModeManual, defense.ModePlan} {
		v := e.approvalFor("s1", "").withPermission(mode)
		if ok, denied := v.gate(true); ok || denied {
			t.Fatalf("%s: gate = (%v,%v), want (false,false) — a remembered approve "+
				"must not satisfy a mode that exists to ask", mode, ok, denied)
		}
		if !v.NeedsForeground() {
			t.Fatalf("%s: NeedsForeground = false, want true", mode)
		}
	}
	// Auto leaves the old semantics untouched.
	v = e.approvalFor("s1", "").withPermission(defense.ModeAuto)
	if ok, _ := v.gate(false); !ok {
		t.Fatal("auto: a remembered session approve must still consent")
	}
	// A project-level deny is a decision, not a mode; it still refuses.
	if err := e.projStore.SetApprovalPolicy("p", config.ApprovalModeOnRequest, projectstore.ScopeSession); err != nil {
		t.Fatalf("policy: %v", err)
	}
	if err := e.projStore.SetApprovalDecision("p", projectstore.DecisionDeny); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if ok, denied := e.approvalFor("s1", "p").withPermission(defense.ModeAuto).gate(true); ok || !denied {
		t.Fatalf("project deny: gate = (%v,%v), want (false,true)", ok, denied)
	}
}
