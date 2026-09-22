// Package scheduler implements the P2P per-edge routing decision (design doc
// §6.4, §2.4 DCPS analogy). It is pure: it inspects the local capability match
// and the known peer directory and returns a Decision, leaving all side effects
// (message sends, state transitions) to the core package.
package scheduler

import "errors"

// ErrLoop reports that appending node to the delegation chain would revisit a
// node that already saw the task — a routing cycle.
var ErrLoop = errors.New("delegation loop")

// ErrChainTooDeep reports that the delegation chain has reached MaxChainDepth.
var ErrChainTooDeep = errors.New("delegation chain too deep")

// ErrBudgetExceeded reports that the task delegation budget has been exhausted.
var ErrBudgetExceeded = errors.New("delegation budget exceeded")

// MaxChainDepth bounds how many nodes one delegation may traverse.
const MaxChainDepth = 8

// MaxVisitsPerNode bounds how many times any single node may appear in a delegation chain.
const MaxVisitsPerNode = 2

// MaxDelegationBudget is the default global maximum budget of total delegations across the mesh.
const MaxDelegationBudget = 20

// Budget tracks the remaining delegation and resource budgets for a task in the mesh.
type Budget struct {
	MaxDelegations int   `json:"max_delegations"`
	TokenBudget    int64 `json:"token_budget,omitempty"`
	DeadlineUnix   int64 `json:"deadline_unix,omitempty"`
}

// DefaultBudget returns standard initial budget constraints.
func DefaultBudget() Budget {
	return Budget{
		MaxDelegations: MaxDelegationBudget,
	}
}

// DecrementDelegation checks and deducts one delegation hop from the budget.
func (b *Budget) DecrementDelegation() error {
	if b.MaxDelegations <= 0 {
		return ErrBudgetExceeded
	}
	b.MaxDelegations--
	return nil
}

// AppendChain returns chain with node appended, or ErrLoop if appending node
// creates an immediate self-hop, direct 2-node ping-pong, or exceeds
// MaxVisitsPerNode. It returns ErrChainTooDeep if the chain is already at
// MaxChainDepth. Legitimate multi-node revisits across the graph DAG are allowed.
func AppendChain(chain []string, node string) ([]string, error) {
	if len(chain) >= MaxChainDepth {
		return nil, ErrChainTooDeep
	}
	// Immediate self-loop: a node delegating directly to itself.
	if len(chain) > 0 && chain[len(chain)-1] == node {
		return nil, ErrLoop
	}
	// Immediate 2-node ping-pong: delegating straight back to immediate sender.
	if len(chain) >= 2 && chain[len(chain)-2] == node {
		return nil, ErrLoop
	}
	visits := 0
	for _, n := range chain {
		if n == node {
			visits++
		}
	}
	if visits >= MaxVisitsPerNode {
		return nil, ErrLoop
	}
	return append(chain, node), nil
}

// Predecessor returns the node this node should relay results back to: the
// element immediately before the last occurrence of self. It returns "" when
// self is the root (nothing to relay to) or absent from the chain.
func Predecessor(chain []string, self string) string {
	idx := -1
	for i, n := range chain {
		if n == self {
			idx = i
		}
	}
	if idx <= 0 {
		return ""
	}
	return chain[idx-1]
}
