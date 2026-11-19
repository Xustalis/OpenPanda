// SPDX-License-Identifier: AGPL-3.0-or-later

package objectives

import (
	"errors"
	"fmt"
	"strings"
)

var ErrAssessment = errors.New("objectives: invalid acceptance evidence")

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
	expected := map[string]bool{} // value says whether the criterion is required
	for _, d := range p.Dimensions {
		for i := range d.Criteria {
			expected[fmt.Sprintf("%s:%d", d.Dimension, i)] = d.Priority == Max
		}
	}
	seen := map[string]bool{}
	for _, a := range assessments {
		key := fmt.Sprintf("%s:%d", a.Dimension, a.Criterion)
		required, known := expected[key]
		if !known {
			return fmt.Errorf("%w: assessment references an unknown criterion", ErrAssessment)
		}
		if seen[key] {
			return fmt.Errorf("%w: duplicate assessment %s", ErrAssessment, key)
		}
		if strings.TrimSpace(a.Evidence) == "" || len(a.Evidence) > 8192 {
			return fmt.Errorf("%w: criterion %s needs a nonempty, bounded evidence record", ErrAssessment, key)
		}
		switch a.Status {
		case "passed", "deferred", "unmet", "blocked":
		default:
			return fmt.Errorf("%w: criterion %s has an unknown status", ErrAssessment, key)
		}
		if required && a.Status != "passed" && !(allowDeferred && a.Status == "deferred") {
			return fmt.Errorf("%w: MAX criterion %s is %s", ErrAssessment, key, a.Status)
		}
		seen[key] = true
	}
	for _, d := range p.Dimensions {
		if d.Priority != Max {
			continue
		}
		for i := range d.Criteria {
			key := fmt.Sprintf("%s:%d", d.Dimension, i)
			if !seen[key] {
				return fmt.Errorf("%w: MAX criterion %s lacks independent acceptance evidence", ErrAssessment, key)
			}
		}
	}
	return nil
}
