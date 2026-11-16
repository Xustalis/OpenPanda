// SPDX-License-Identifier: AGPL-3.0-or-later

// Package objectives validates and propagates model-produced execution goals.
// It deliberately performs no natural-language classification or weighting.
package objectives

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const (
	Version    = 1
	Normal     = "NORMAL"
	Max        = "MAX"
	Capability = "objective-policy-v1"
)

// Policy is immutable for an execution revision. Only a new owner request may
// replace root requirements; delegates can retain or strengthen this contract.
type Policy struct {
	Version    int         `json:"version" yaml:"version"`
	Revision   int         `json:"revision" yaml:"revision"`
	Dimensions []Dimension `json:"dimensions" yaml:"dimensions"`
}

type Dimension struct {
	Dimension   string   `json:"dimension" yaml:"dimension"`
	Weight      float64  `json:"weight" yaml:"weight"`
	Priority    string   `json:"priority" yaml:"priority"`
	Requirement string   `json:"requirement" yaml:"requirement"`
	Reason      string   `json:"reason" yaml:"reason"`
	Criteria    []string `json:"criteria" yaml:"criteria"`
}

// Validate checks shape and bounds, never what the user meant. Missing policies
// are retained for old tasks rather than inventing requirements in code.
func Validate(p *Policy) error {
	if p == nil {
		return nil
	}
	if p.Version != Version || p.Revision < 1 {
		return fmt.Errorf("objectives: unsupported version or invalid revision")
	}
	if len(p.Dimensions) == 0 || len(p.Dimensions) > 16 {
		return fmt.Errorf("objectives: expected 1..16 dimensions")
	}
	seen := map[string]bool{}
	for _, d := range p.Dimensions {
		if !validID(d.Dimension) || seen[d.Dimension] {
			return fmt.Errorf("objectives: invalid or duplicate dimension %q", d.Dimension)
		}
		seen[d.Dimension] = true
		if math.IsNaN(d.Weight) || math.IsInf(d.Weight, 0) || d.Weight < 0 || d.Weight > 1 {
			return fmt.Errorf("objectives: %s weight outside 0..1", d.Dimension)
		}
		if d.Priority != Normal && d.Priority != Max {
			return fmt.Errorf("objectives: %s priority must be NORMAL or MAX", d.Dimension)
		}
		if strings.TrimSpace(d.Requirement) == "" || len(d.Requirement) > 4096 || strings.TrimSpace(d.Reason) == "" || len(d.Reason) > 4096 {
			return fmt.Errorf("objectives: %s needs a bounded source requirement and reason", d.Dimension)
		}
		if len(d.Criteria) > 16 || (d.Priority == Max && len(d.Criteria) == 0) {
			return fmt.Errorf("objectives: %s MAX needs acceptance criteria (maximum 16)", d.Dimension)
		}
		criteria := map[string]bool{}
		for _, c := range d.Criteria {
			if strings.TrimSpace(c) == "" || len(c) > 4096 || criteria[c] {
				return fmt.Errorf("objectives: %s has invalid or duplicate criteria", d.Dimension)
			}
			criteria[c] = true
		}
	}
	return nil
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Inherit returns an independent policy. Omitting a dimension means inheritance;
// an explicit downgrade is an error. Ancestor criteria retain stable indexes.
func Inherit(parent, child *Policy) (*Policy, error) {
	if err := Validate(parent); err != nil {
		return nil, err
	}
	if err := Validate(child); err != nil {
		return nil, err
	}
	if parent == nil {
		return clone(child), nil
	}
	if child == nil {
		return clone(parent), nil
	}
	if child.Revision != parent.Revision {
		return nil, fmt.Errorf("objectives: delegates cannot replace root revision")
	}
	out := clone(parent)
	positions := map[string]int{}
	for i, d := range out.Dimensions {
		positions[d.Dimension] = i
	}
	for _, d := range child.Dimensions {
		i, exists := positions[d.Dimension]
		if !exists {
			out.Dimensions = append(out.Dimensions, d)
			continue
		}
		ancestor := out.Dimensions[i]
		if (ancestor.Priority == Max && d.Priority != Max) || d.Weight < ancestor.Weight || d.Requirement != ancestor.Requirement {
			return nil, fmt.Errorf("objectives: cannot weaken inherited dimension %s", d.Dimension)
		}
		for _, criterion := range ancestor.Criteria {
			if !contains(d.Criteria, criterion) {
				return nil, fmt.Errorf("objectives: cannot remove inherited %s criterion", d.Dimension)
			}
		}
		ordered := append([]string(nil), ancestor.Criteria...)
		for _, criterion := range d.Criteria {
			if !contains(ordered, criterion) {
				ordered = append(ordered, criterion)
			}
		}
		d.Criteria = ordered
		out.Dimensions[i] = d
	}
	if err := Validate(out); err != nil {
		return nil, err
	}
	return clone(out), nil
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func clone(p *Policy) *Policy {
	if p == nil {
		return nil
	}
	b, _ := json.Marshal(p)
	var out Policy
	_ = json.Unmarshal(b, &out)
	return &out
}

func FromSpecJSON(raw string) (*Policy, error) {
	if raw == "" {
		return nil, nil
	}
	var spec struct {
		Objectives *Policy `json:"objectives"`
	}
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, fmt.Errorf("objectives: invalid spec: %w", err)
	}
	return spec.Objectives, Validate(spec.Objectives)
}

// InheritSpecJSON preserves every unrelated child spec field.
func InheritSpecJSON(parent, child string) (string, error) {
	p, err := FromSpecJSON(parent)
	if err != nil {
		return "", err
	}
	c, err := FromSpecJSON(child)
	if err != nil {
		return "", err
	}
	merged, err := Inherit(p, c)
	if err != nil {
		return "", err
	}
	if merged == nil {
		return child, nil
	}
	spec := map[string]json.RawMessage{}
	if child != "" {
		if err = json.Unmarshal([]byte(child), &spec); err != nil {
			return "", err
		}
	}
	if spec == nil {
		spec = map[string]json.RawMessage{}
	}
	spec["objectives"], err = json.Marshal(merged)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(spec)
	return string(b), err
}

func HasMax(p *Policy) bool {
	if p != nil {
		for _, d := range p.Dimensions {
			if d.Priority == Max {
				return true
			}
		}
	}
	return false
}

// Prompt is decision context, never an authorization or permission.
func Prompt(p *Policy) string {
	if p == nil {
		return ""
	}
	if err := Validate(p); err != nil {
		return "\n[Invalid execution objectives: refuse completion and report the contract error.]\n"
	}
	b, _ := json.Marshal(p)
	return "\n[Execution objectives]\n" + string(b) + "\nHonor inherited requirements and criteria. MAX is a highest-priority acceptance obligation, not infinity, unlimited spend, authorization, or proof of quality. Optimize other goals only among compliant choices. Do not replace conflicts with an averaged score. State unknown capability/quality/cost estimates honestly. If requirements conflict with budgets, permissions or available capabilities, report the blocker instead of downgrading. Only the user may revise root requirements. Delegates may refine without weakening.\n[/Execution objectives]\n"
}
