// SPDX-License-Identifier: AGPL-3.0-or-later

package objectives

import (
	"fmt"
	"strings"
)

// Assessment is supplied by the independent reviewing model against actual
// command, artifact or inspection evidence, never by the executing agent alone.
type Assessment struct {
	Dimension string `json:"dimension"`
	Criterion int    `json:"criterion"`
	Status    string `json:"status"`
	Evidence  string `json:"evidence"`
}

// VerifyAssessments does not interpret evidence semantically. The reviewer
// judges it; this boundary rejects omissions, duplicate claims and unmet goals.
// A nonfinal stage/child may explicitly defer global criteria to its integrator.
func VerifyAssessments(p *Policy, assessments []Assessment, allowDeferred bool) error {
	if err := Validate(p); err != nil {
		return err
	}
	if !HasMax(p) {
		return nil
	}
	expected := map[string]bool{}
	for _, d := range p.Dimensions {
		if d.Priority == Max {
			for i := range d.Criteria {
				expected[fmt.Sprintf("%s:%d", d.Dimension, i)] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, a := range assessments {
		key := fmt.Sprintf("%s:%d", a.Dimension, a.Criterion)
		if !expected[key] || seen[key] || strings.TrimSpace(a.Evidence) == "" || len(a.Evidence) > 8192 {
			return fmt.Errorf("objectives: invalid or duplicate assessment %s", key)
		}
		if a.Status != "passed" && !(allowDeferred && a.Status == "deferred") {
			return fmt.Errorf("objectives: MAX criterion %s is %s", key, a.Status)
		}
		seen[key] = true
	}
	for key := range expected {
		if !seen[key] {
			return fmt.Errorf("objectives: MAX criterion %s lacks independent acceptance evidence", key)
		}
	}
	return nil
}
