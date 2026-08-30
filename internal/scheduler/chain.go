// Package scheduler implements the P2P per-edge routing decision (design doc
// §6.4, §2.4 DCPS analogy). It is pure: it inspects the local capability match
// and the known peer directory and returns a Decision, leaving all side effects
// (message sends, state transitions) to the core package.
package scheduler

import "errors"

// ErrLoop reports that appending node to the delegation chain would revisit a
// node that already saw the task — a routing cycle.
var ErrLoop = errors.New("delegation loop")

// ErrChainTooDeep reports that the delegation chain has reached MaxChainDepth:
// the task may not revisit any node (ErrLoop already guarantees that), but an
// ever-growing simple path would still bound away — each hop is another copy
// of the task, another lease to renew, another relay for every result.
var ErrChainTooDeep = errors.New("delegation chain too deep")

// MaxChainDepth bounds how many nodes one delegation may traverse. Loop
// detection already terminates cycles; this caps the acyclic depth so a
// misbehaving forward chain (sub-schedulers handing work onward indefinitely)
// cannot grow without bound. 8 hops is far beyond any real network diameter.
const MaxChainDepth = 8

// AppendChain returns chain with node appended, or ErrLoop if node is already
// present (a routing cycle), or ErrChainTooDeep if the chain is already at
// MaxChainDepth. A task must never revisit a node; rejecting the append bounds
// the delegation depth and prevents message loops.
func AppendChain(chain []string, node string) ([]string, error) {
	if len(chain) >= MaxChainDepth {
		return nil, ErrChainTooDeep
	}
	for _, n := range chain {
		if n == node {
			return nil, ErrLoop
		}
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
