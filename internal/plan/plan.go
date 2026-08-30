// Package plan is the plan plane: the missing third layer between "one task, one
// node" and the scenario this project exists for — develop on the Mac, train on
// the Windows box, report through the Pi.
//
// A single task cannot express that. It has a state machine, a lease and an
// audit chain, but no notion of "this work comes after that work, and consumes
// what it produced". A Plan adds exactly that and nothing else: named stages,
// their dependencies, and per-stage capability and resource requirements. The
// scheduling, routing, retrying and approval of each stage stays with the
// existing task machinery, which is why a stage lands in the same tasks table as
// any other task rather than in a parallel execution model of its own.
//
// The package is pure: no IO, no clock, no database. Everything here is a
// decision about a value the caller already holds, which is what makes the plan
// layer testable without a network of nodes.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// MaxStages bounds a plan's size. Plans are generated from a user's request by a
// model, and every stage becomes a real task row, a real delegation and a real
// lease — so an unbounded plan is an unbounded fan-out of live work across the
// network. Sixty-four is far past any hand-written pipeline and far short of a
// runaway.
const MaxStages = 64

// ValidID reports whether s is a safe stage id. A stage id becomes a path
// segment in the executor's work dir (core.stageWorkDir) and a field on the
// wire, so anything outside [A-Za-z0-9_-] — separators, dots, backslashes —
// is refused at the boundary instead of sanitized after the fact (review
// P0-1: a stage id of "../../.." turned the work dir into an arbitrary
// directory the caller could then pack and exfiltrate).
func ValidID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// Stage is one unit of a plan: work that can be routed to a node on its own.
// Requires and Resources are the same vocabulary the scheduler already routes a
// task by, so a stage is matched to a node by the existing scoring — a stage
// needing a GPU cannot land on the Pi because of what it declares here, not
// because of anything plan-specific.
type Stage struct {
	// ID is the stage's name within the plan ("develop", "train"). It is what
	// Needs refers to, and it is persisted on the task row.
	ID string
	// Title is the human-readable label shown in the task queue.
	Title string
	// Requires is the capability set the executing node must satisfy.
	Requires []string
	// Needs lists the stage IDs that must finish before this one may start. It
	// is also the artifact wiring: a stage consumes the outputs of what it needs.
	Needs []string
	// Resources is the hardware the stage asks for (VRAM for a training run,
	// nothing much for a summary).
	Resources entry.ResourceProfile
	// Intent is what the executing node is actually asked to do.
	Intent string
}

// Plan is a goal decomposed into stages. The stage list carries the dependency
// graph in its Needs fields rather than in an explicit edge list: a stage is
// meaningless without the thing it depends on, so the two travel together.
type Plan struct {
	Goal   string
	Stages []Stage
}

// Validate rejects a plan that cannot be executed. Every check here is a plan
// that would otherwise fail *after* work had already been dispatched — a dangling
// dependency leaves a stage waiting for a stage that will never run, and a cycle
// leaves the whole plan idle with nothing ready and no error anywhere.
func Validate(p Plan) error {
	if strings.TrimSpace(p.Goal) == "" {
		return fmt.Errorf("plan: goal must not be empty")
	}
	if len(p.Stages) == 0 {
		return fmt.Errorf("plan: no stages")
	}
	if len(p.Stages) > MaxStages {
		return fmt.Errorf("plan: %d stages exceeds the limit of %d", len(p.Stages), MaxStages)
	}

	index := make(map[string]int, len(p.Stages))
	for i, s := range p.Stages {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			return fmt.Errorf("plan: stage %d has no id", i)
		}
		if id != s.ID {
			// The id becomes a database key and a wire field; surrounding
			// whitespace would make two visually identical stages distinct.
			return fmt.Errorf("plan: stage id %q has surrounding whitespace", s.ID)
		}
		if !ValidID(id) {
			return fmt.Errorf("plan: stage id %q must be 1-64 chars of [A-Za-z0-9_-]", s.ID)
		}
		if prev, dup := index[id]; dup {
			return fmt.Errorf("plan: stages %d and %d share the id %q", prev, i, id)
		}
		if strings.TrimSpace(s.Intent) == "" {
			return fmt.Errorf("plan: stage %q has no intent", id)
		}
		index[id] = i
	}

	for _, s := range p.Stages {
		seen := make(map[string]bool, len(s.Needs))
		for _, need := range s.Needs {
			if need == s.ID {
				return fmt.Errorf("plan: stage %q depends on itself", s.ID)
			}
			if _, ok := index[need]; !ok {
				return fmt.Errorf("plan: stage %q needs %q, which is not in the plan", s.ID, need)
			}
			if seen[need] {
				return fmt.Errorf("plan: stage %q lists %q twice", s.ID, need)
			}
			seen[need] = true
		}
	}

	if cycle := findCycle(p); len(cycle) > 0 {
		return fmt.Errorf("plan: dependency cycle %s", strings.Join(cycle, " -> "))
	}
	return nil
}

// findCycle returns one cycle as a stage-id path, or nil if the graph is acyclic.
// Reporting the path rather than a bare "has a cycle" is deliberate: a plan comes
// out of a model, and the operator reading the error needs to see which stages
// to break apart.
func findCycle(p Plan) []string {
	needs := make(map[string][]string, len(p.Stages))
	for _, s := range p.Stages {
		needs[s.ID] = s.Needs
	}
	const (
		white = 0 // unvisited
		grey  = 1 // on the current path
		black = 2 // fully explored
	)
	colour := make(map[string]int, len(p.Stages))
	var path []string

	var walk func(id string) []string
	walk = func(id string) []string {
		colour[id] = grey
		path = append(path, id)
		for _, need := range needs[id] {
			switch colour[need] {
			case grey:
				// Cut the path down to where the cycle actually starts.
				for i, on := range path {
					if on == need {
						return append(append([]string{}, path[i:]...), need)
					}
				}
				return append(append([]string{}, path...), need)
			case white:
				if c := walk(need); len(c) > 0 {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		colour[id] = black
		return nil
	}

	for _, s := range p.Stages {
		if colour[s.ID] == white {
			path = path[:0]
			if c := walk(s.ID); len(c) > 0 {
				return c
			}
		}
	}
	return nil
}

// Ready returns the stages whose dependencies are all satisfied and which are not
// themselves done, in plan order.
//
// It returns a slice rather than a single stage because that is where parallelism
// comes from: a linear chain yields one stage at a time, while a plan that fans
// out yields several at once, and the caller hands them all to the queue — so two
// independent stages occupy two nodes without any plan-specific scheduling.
//
// done is keyed by stage ID. Unknown keys are ignored, so a caller may pass the
// completion set of a larger plan.
func Ready(p Plan, done map[string]bool) []Stage {
	var out []Stage
	for _, s := range p.Stages {
		if done[s.ID] {
			continue
		}
		blocked := false
		for _, need := range s.Needs {
			if !done[need] {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, s)
		}
	}
	return out
}

// Complete reports whether every stage of the plan is done.
func Complete(p Plan, done map[string]bool) bool {
	for _, s := range p.Stages {
		if !done[s.ID] {
			return false
		}
	}
	return true
}

// Order returns the stages in an execution order: every stage appears after the
// stages it needs. It is not how the plan runs — execution is driven by Ready, one
// wave at a time, so independent stages occupy several nodes at once — but it is
// how a plan is *read*, which is what a dry run and a review need. Repeatedly
// draining Ready is deliberately how it is computed: one definition of readiness
// is easier to trust than two, and this way the printed order cannot disagree with
// the order the plan actually takes.
//
// A cycle is impossible in a validated plan; the error is here so an
// unvalidated one cannot make this loop forever.
func Order(p Plan) ([]Stage, error) {
	done := make(map[string]bool, len(p.Stages))
	out := make([]Stage, 0, len(p.Stages))
	for len(out) < len(p.Stages) {
		wave := Ready(p, done)
		if len(wave) == 0 {
			return nil, fmt.Errorf("plan: %d stages cannot be ordered (dependency cycle)", len(p.Stages)-len(out))
		}
		// Stable within a wave: Ready preserves declaration order, and a listing
		// that reshuffles between runs is a listing nobody can diff.
		for _, s := range wave {
			done[s.ID] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// Find returns the stage with the given ID.
func Find(p Plan, id string) (Stage, bool) {
	for _, s := range p.Stages {
		if s.ID == id {
			return s, true
		}
	}
	return Stage{}, false
}

// Inputs returns the stage IDs whose outputs the named stage consumes, sorted so
// the artifact wiring is deterministic — a stage with two inputs must extract
// them in the same order on every node, or two runs of the same plan would
// produce differently laid out working trees.
func Inputs(p Plan, id string) []string {
	s, ok := Find(p, id)
	if !ok {
		return nil
	}
	out := append([]string{}, s.Needs...)
	sort.Strings(out)
	return out
}
