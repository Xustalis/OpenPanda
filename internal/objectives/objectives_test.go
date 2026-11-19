// SPDX-License-Identifier: AGPL-3.0-or-later

package objectives

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func policy() *Policy {
	return &Policy{Version: 1, Revision: 1, Dimensions: []Dimension{{Dimension: "quality", Weight: 1, Priority: Max, Requirement: "Verify the delivered behavior", Reason: "The user made this essential", Criteria: []string{"Regression passes with evidence"}}}}
}

func TestValidatePolicyBounds(t *testing.T) {
	if err := Validate(nil); err != nil {
		t.Fatal(err)
	}
	if err := Validate(policy()); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Policy){
		"version":             func(p *Policy) { p.Version = 9 },
		"revision":            func(p *Policy) { p.Revision = 0 },
		"dimension":           func(p *Policy) { p.Dimensions[0].Dimension = "bad key" },
		"duplicate":           func(p *Policy) { p.Dimensions = append(p.Dimensions, p.Dimensions[0]) },
		"unknown priority":    func(p *Policy) { p.Dimensions[0].Priority = "infinite" },
		"NaN":                 func(p *Policy) { p.Dimensions[0].Weight = math.NaN() },
		"infinity":            func(p *Policy) { p.Dimensions[0].Weight = math.Inf(1) },
		"weight":              func(p *Policy) { p.Dimensions[0].Weight = 1.01 },
		"missing requirement": func(p *Policy) { p.Dimensions[0].Requirement = " " },
		"missing reason":      func(p *Policy) { p.Dimensions[0].Reason = "" },
		"missing acceptance":  func(p *Policy) { p.Dimensions[0].Criteria = nil },
		"empty acceptance":    func(p *Policy) { p.Dimensions[0].Criteria = []string{" "} },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			p := policy()
			change(p)
			if Validate(p) == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
}

func TestInheritPolicyCannotWeaken(t *testing.T) {
	parent := policy()
	inherited, err := Inherit(parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	inherited.Dimensions[0].Criteria[0] = "changed"
	if parent.Dimensions[0].Criteria[0] == "changed" {
		t.Fatal("aliased parent")
	}
	tests := map[string]func(*Policy){
		"priority":    func(p *Policy) { p.Dimensions[0].Priority = Normal },
		"weight":      func(p *Policy) { p.Dimensions[0].Weight = 0.5 },
		"requirement": func(p *Policy) { p.Dimensions[0].Requirement = "less work" },
		"criterion":   func(p *Policy) { p.Dimensions[0].Criteria = []string{"different evidence"} },
		"revision":    func(p *Policy) { p.Revision = 2 },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			child := policy()
			change(child)
			if _, err := Inherit(parent, child); err == nil {
				t.Fatal("accepted downgrade")
			}
		})
	}
	child := policy()
	child.Dimensions[0].Criteria = append([]string{"extra verification"}, child.Dimensions[0].Criteria...)
	child.Dimensions = append(child.Dimensions, Dimension{Dimension: "time", Weight: 0.4, Priority: Normal, Requirement: "Finish promptly", Reason: "Secondary preference"})
	out, err := Inherit(parent, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Dimensions) != 2 || len(out.Dimensions[0].Criteria) != 2 || out.Dimensions[0].Criteria[0] != parent.Dimensions[0].Criteria[0] {
		t.Fatalf("lost stable inherited criteria: %+v", out)
	}
}

func TestInheritSpecJSONPreservesChildAndLegacy(t *testing.T) {
	p, _ := json.Marshal(map[string]any{"objectives": policy()})
	out, err := InheritSpecJSON(string(p), `{"tools_policy":"minimal","custom":{"keep":true}}`)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal([]byte(out), &decoded)
	if decoded["tools_policy"] != "minimal" || decoded["custom"] == nil || decoded["objectives"] == nil {
		t.Fatal(out)
	}
	if out, err := InheritSpecJSON("", ""); err != nil || out != "" {
		t.Fatalf("legacy changed: %q %v", out, err)
	}
	if _, err := InheritSpecJSON(string(p), `{"objectives":{"version":2}}`); err == nil {
		t.Fatal("invalid child accepted")
	}
}

func TestMAXRequiresIndependentCriterionAssessments(t *testing.T) {
	p := policy()
	pass := Assessment{Dimension: "quality", Criterion: 0, Status: "passed", Evidence: "Recorded regression command exited zero and asserted the changed behavior"}
	if err := VerifyAssessments(p, []Assessment{pass}, false); err != nil {
		t.Fatal(err)
	}
	for _, as := range [][]Assessment{nil, {pass, pass}, {{Dimension: "quality", Criterion: 9, Status: "passed", Evidence: "unrelated"}}, {{Dimension: "quality", Status: "unmet", Evidence: "test failed"}}, {{Dimension: "quality", Status: "passed"}}} {
		if VerifyAssessments(p, as, false) == nil {
			t.Fatalf("accepted %+v", as)
		}
	}
	deferred := pass
	deferred.Status = "deferred"
	deferred.Evidence = "Training metric is checked by the final integration stage"
	if VerifyAssessments(p, []Assessment{deferred}, false) == nil {
		t.Fatal("final accepted deferred")
	}
	if err := VerifyAssessments(p, []Assessment{deferred}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Prompt(p), "not infinity") {
		t.Fatal("missing budget boundary")
	}
}

func TestMixedAssessmentsDoNotTurnNormalIntoAnAcceptanceGate(t *testing.T) {
	p := policy()
	p.Dimensions = append(p.Dimensions, Dimension{Dimension: "robustness", Priority: Normal,
		Requirement: "Portable output", Reason: "Supplemental quality", Criteria: []string{"No external resources"}})
	max := Assessment{Dimension: "quality", Status: "passed", Evidence: "Independent test output"}
	normal := Assessment{Dimension: "robustness", Status: "unmet", Evidence: "One optional check failed"}
	if err := VerifyAssessments(p, []Assessment{normal, max}, false); err != nil {
		t.Fatalf("optional normal finding blocked delivery: %v", err)
	}
	for _, assessments := range [][]Assessment{{normal}, {max, normal, normal}, {max, {Dimension: "robustness", Criterion: 1, Status: "passed", Evidence: "wrong index"}}} {
		if err := VerifyAssessments(p, assessments, false); err == nil {
			t.Fatalf("accepted invalid evidence: %+v", assessments)
		}
	}
}
