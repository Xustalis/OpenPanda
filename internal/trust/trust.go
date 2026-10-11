// SPDX-License-Identifier: AGPL-3.0-or-later

// Package trust holds the trust-zone model: per-peer trust levels and the
// policy tables that translate a level into (a) how much of an outbound
// delegate may be exposed to that peer and (b) how an inbound delegate
// originating at that level is admitted. Everything here is pure data and
// pure functions — storage, transport and execution all live elsewhere, so
// the whole policy surface is unit-testable in isolation.
//
// The model (docs/plans/design-trust-zones-agent-economy.md §3) hangs three
// levels on a peer's Ed25519 identity. Trust is always asserted locally —
// configured by this node's operator or demoted by this node's key-change
// rule — and a level a peer claims for itself is never honoured.
package trust

import (
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// Level is a peer's trust tier.
type Level string

const (
	// Mesh is the default and the pre-feature behaviour in full: a peer with
	// no recorded level is mesh, and a fleet where every row is mesh behaves
	// exactly as before trust zones existed.
	Mesh Level = "mesh"
	// Guarded is a semi-trusted member: a friend's device, or an own machine
	// on an untrusted segment. Delegates toward it are clamped to a narrower
	// exposure ceiling, and work originating from it runs under the bounded
	// admission policy instead of unconditional acceptance.
	Guarded Level = "guarded"
	// Open is a stranger. The offer/bid market it exists for is not built, so
	// today an open peer may sit in the directory but is never handed a
	// task_delegate — neither as executor nor as relay — and work it
	// originates is declined at intake.
	Open Level = "open"
)

// ParseLevel normalizes a configured/stored level. Empty means mesh: the
// field's absence is the pre-feature default, so an unset level must behave
// as full trust rather than fail closed into a tier that strips fields the
// operator never asked to strip.
func ParseLevel(s string) (Level, error) {
	switch Level(strings.ToLower(strings.TrimSpace(s))) {
	case "", Mesh:
		return Mesh, nil
	case Guarded:
		return Guarded, nil
	case Open:
		return Open, nil
	default:
		return "", fmt.Errorf("invalid trust level %q (want mesh|guarded|open)", s)
	}
}

// rank orders the tiers for min/max comparisons; lower is more trusted.
func (l Level) rank() int {
	switch l {
	case Mesh:
		return 0
	case Guarded:
		return 1
	default:
		return 2
	}
}

// Min returns the less trusted of two levels — the rule that makes a path's
// effective exposure the weakest tier along it.
func Min(a, b Level) Level {
	if a.rank() >= b.rank() {
		return a
	}
	return b
}

// SendPolicy is the outbound exposure ceiling for one tier: what a
// task_delegate may still carry once it has been clamped for that tier.
type SendPolicy struct {
	// Delegate false bars task_delegate toward the tier entirely (open).
	Delegate bool
	// MaxContext caps context_level: "full" carries the snapshot inline,
	// "pointer" lets the executor pull it through the authorized
	// context_fetch instead, "summary" sends no snapshot reference at all.
	// Pointer is weaker than summary but still meaningful: bytes leave only
	// after an auditable pull, never pushed in the delegate itself.
	MaxContext string
	// ProjectPack permits the inline project memory bundle.
	ProjectPack bool
	// Inputs permits artifact/worktree references (the repo tree).
	Inputs bool
	// Consent permits the tier-2 consent grant to ride the delegate.
	Consent bool
}

// AcceptPolicy is the inbound admission rule for delegates whose origin sits
// at a tier. The origin — not the last hop — is judged, because the intent
// and its consent are authored there; a relay is only transport.
type AcceptPolicy struct {
	// DelegateOrigin false declines the delegate at intake: open-tier nodes
	// may not originate push-mode work while the market is unbuilt.
	DelegateOrigin bool
	// Mode is the deliberative-accept form for the tier: "auto" admits as
	// today (with the consent drop applying the restricted tool face),
	// "review" parks the task for this node's operator before it may run,
	// and "refuse" declines outright.
	Mode string
	// PolicyLLM optionally puts a zero-tool classifier in front of the mode:
	// "off" never calls the model, "shadow" records the verdict but lets
	// Mode decide, and "enforce" lets the verdict override Mode. The
	// classifier only ever judges a productivity question (is this work we
	// want); safety stays with the deterministic tier gates.
	PolicyLLM string
}

// Accept modes and the policy-LLM settings.
const (
	AcceptAuto   = "auto"
	AcceptReview = "review"
	AcceptRefuse = "refuse"

	LLMOff     = "off"
	LLMShadow  = "shadow"
	LLMEnforce = "enforce"
)

// contextRank orders the exposure levels of context_level.
func contextRank(level string) int {
	switch level {
	case "full":
		return 2
	case "pointer":
		return 1
	default:
		return 0 // summary or unset
	}
}

// DefaultSend is a tier's built-in outbound ceiling. Mesh imposes nothing
// new — every clamp is a no-op — which is what makes an all-mesh fleet
// byte-identical to the pre-feature behaviour.
func DefaultSend(l Level) SendPolicy {
	switch l {
	case Guarded:
		return SendPolicy{
			Delegate:    true,
			MaxContext:  "pointer",
			ProjectPack: false,
			Inputs:      false,
			Consent:     false,
		}
	case Open:
		return SendPolicy{Delegate: false}
	default:
		return SendPolicy{
			Delegate:    true,
			MaxContext:  "full",
			ProjectPack: true,
			Inputs:      true,
			Consent:     true,
		}
	}
}

// DefaultAccept is a tier's built-in inbound rule. Guarded defaults to auto:
// the work still runs, but the consent drop already applied the restricted
// tool face and parked any irreversible step for this node's operator.
func DefaultAccept(l Level) AcceptPolicy {
	switch l {
	case Guarded:
		return AcceptPolicy{DelegateOrigin: true, Mode: AcceptAuto, PolicyLLM: LLMOff}
	case Open:
		return AcceptPolicy{DelegateOrigin: false, Mode: AcceptRefuse, PolicyLLM: LLMOff}
	default:
		return AcceptPolicy{DelegateOrigin: true, Mode: AcceptAuto, PolicyLLM: LLMOff}
	}
}

// Needs is what a delegate actually carries that a send policy could deny or
// shrink. Eligibility is judged from needs — a task that would run degraded
// under a peer's ceiling is re-routed instead of lobotomized on the wire.
type Needs struct {
	// Pack: the delegate ships (or names) a project — its pack and tree.
	Pack bool
	// Inputs: artifact references ride the payload (worktree, stage inputs).
	Inputs bool
	// FullContext: context_level "full" was requested — the snapshot inline.
	FullContext bool
	// Consent: a tier-2 grant rides the payload.
	Consent bool
}

// NeedsOf derives the needs from the assembled payload — usable wherever a
// delegate exists (submit, relay, re-route) rather than only where the
// originating TaskInput is still in scope.
func NeedsOf(p bus.TaskDelegatePayload) Needs {
	return Needs{
		Pack:        p.Project != "" || len(p.ProjectPack) > 0,
		Inputs:      len(p.Inputs) > 0 || len(p.BundledArtifacts) > 0,
		FullContext: contextRank(p.ContextLevel) >= 2,
		Consent:     p.Authorized,
	}
}

// ExecutorOK reports whether a peer at this policy's tier may EXECUTE a task
// with these needs — i.e. nothing the task cannot do without is denied by
// the ceiling. Consent and context level are clampable (the task still runs,
// narrower), so they never disqualify; a denied pack or inputs would leave
// the executor blind, so those do.
func (p SendPolicy) ExecutorOK(n Needs) bool {
	if !p.Delegate {
		return false
	}
	if n.Pack && !p.ProjectPack {
		return false
	}
	if n.Inputs && !p.Inputs {
		return false
	}
	return true
}

// MayHoldContext reports whether the peer may hold snapshot bytes at all.
// A "summary" ceiling means none may leave — which must also mean none may
// be PULLED: context_fetch serves full blobs, so without the fetch-side
// counterpart the ceiling is an audit note, not a bound. "pointer" answers
// true deliberately — pull-on-demand is its whole semantics (the bytes move
// only on this authenticated, authorized request); it bounds nothing and
// never claimed to.
func (p SendPolicy) MayHoldContext() bool { return contextRank(p.MaxContext) > 0 }

// ClampApplied notes what Apply actually removed, for the audit trail.
type ClampApplied struct {
	ContextLevel bool // context data dropped / level capped
	ProjectPack  bool
	Inputs       bool
	Consent      bool
}

func (c ClampApplied) Any() bool {
	return c.ContextLevel || c.ProjectPack || c.Inputs || c.Consent
}

// Apply clamps a delegate in place toward this policy's ceiling. Clamping is
// monotone — it only ever removes — so a relay can apply its own next-hop
// policy on top of the origin's without resurrecting anything the origin
// already stripped. Callers must run Eligible first: Apply enforces the
// ceiling but does not decide whether the task still means anything once
// clamped.
func (p SendPolicy) Apply(d *bus.TaskDelegatePayload) ClampApplied {
	var out ClampApplied
	if contextRank(d.ContextLevel) > contextRank(p.MaxContext) {
		// Downgrade toward the ceiling. "full" carrying inline bytes becomes
		// "pointer" (the hash stays — the fetch is the authorization point);
		// under a "summary" ceiling the reference itself is stripped so the
		// executor sees intent/spec only.
		out.ContextLevel = true
		d.ContextData = nil
		if contextRank(p.MaxContext) == 0 {
			d.ContextLevel = "summary"
			d.ContextHash = ""
			d.ContextType = ""
		} else {
			d.ContextLevel = p.MaxContext
		}
	}
	if len(d.ProjectPack) > 0 && !p.ProjectPack {
		d.ProjectPack = nil
		out.ProjectPack = true
	}
	if len(d.Inputs) > 0 && !p.Inputs {
		d.Inputs = nil
		out.Inputs = true
	}
	if len(d.BundledArtifacts) > 0 && !p.Inputs {
		d.BundledArtifacts = nil
		out.Inputs = true
	}
	if d.Authorized && !p.Consent {
		d.Authorized = false
		d.AuthSig, d.AuthPub = "", ""
		d.AuthTs, d.AuthHops = 0, 0
		out.Consent = true
	}
	return out
}
