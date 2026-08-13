// Package defense implements the task-loop defense chain (design doc §14) and
// the permission model (design doc §16). MVP scope is the deterministic layer:
// command-tier classification and authorization gating. The adversarial model
// review and upper-layer judgment (§14.2 Layer 2+) are later phases.
package defense

import "errors"

// Command tiers (design doc §16). Tier 1 operations are reversible and may be
// auto-approved; Tier 2 operations are irreversible and require explicit user
// authorization before execution.
const (
	TierReversible   = 1
	TierIrreversible = 2
)

// ErrNotAuthorized reports a Tier-2 command executed without authorization.
var ErrNotAuthorized = errors.New("defense: tier-2 command requires authorization")

// Authorize decides whether a command at the given tier may run. Tier 1 always
// passes; Tier 2 passes only when the caller supplies explicit authorization.
// A zero/unknown tier is treated as Tier 1 (reversible) — the safe default is
// to gate privilege escalation, not to reject everything.
func Authorize(tier int, authorized bool) error {
	if tier >= TierIrreversible && !authorized {
		return ErrNotAuthorized
	}
	return nil
}

// TierFromCommand infers a tier from a command's first word, used as a
// backstop when a capability card does not declare one. Privilege-escalating
// or destructive verbs default to Tier 2; everything else is Tier 1. An
// explicit card tier always wins over this inference.
func TierFromCommand(command string) int {
	switch command {
	case "sudo", "su", "doas", "rm", "dd", "mkfs", "shutdown", "reboot",
		"poweroff", "systemctl", "mount", "umount", "iptables", "nft":
		return TierIrreversible
	}
	return TierReversible
}
