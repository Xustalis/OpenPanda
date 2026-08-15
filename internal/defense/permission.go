// Package defense implements the task-loop defense chain (design doc §14) and
// the permission model (design doc §16). MVP scope is the deterministic layer:
// command-tier classification and authorization gating. The adversarial model
// review and upper-layer judgment (§14.2 Layer 2+) are later phases.
package defense

import (
	"errors"
	"strings"
)

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

// TierFromCommand infers a tier from a command and its args, used as a
// backstop when a capability card does not declare one. Privilege-escalating
// or destructive verbs default to Tier 2; everything else is Tier 1. An
// explicit card tier always wins over this inference.
//
// The inference unwraps the common forms that first-word matching would
// otherwise miss: "sudo"/"doas"/"su" force Tier 2 on their own, and an
// interpreter invoked with a code flag ("bash -c", "sh -c", "python -c", …)
// is judged by the code it runs rather than by the interpreter's name.
func TierFromCommand(command string, args ...string) int {
	if destructiveVerbs[command] {
		return TierIrreversible
	}
	// Pass-through wrappers run a later argument as the real command (env VAR=x
	// cmd, timeout 5 cmd, busybox cmd, …). Classify the inner command, not the
	// wrapper's name, so a destructive payload cannot hide behind the wrapper.
	if passThroughVerbs[command] {
		if inner, innerArgs := unwrapPassThrough(args); inner != "" {
			return TierFromCommand(inner, innerArgs...)
		}
		return TierReversible
	}
	if flag, ok := interpreterCodeFlag[command]; ok {
		if code := codeArg(flag, args); code != "" && codeEscalates(code) {
			return TierIrreversible
		}
	}
	return TierReversible
}

// destructiveVerbs are command names treated as irreversible (Tier 2) by
// default: privilege escalation and destructive filesystem/network/system
// operations (design doc §16).
var destructiveVerbs = map[string]bool{
	"sudo": true, "su": true, "doas": true,
	"rm": true, "dd": true, "mkfs": true,
	"shutdown": true, "reboot": true, "poweroff": true,
	"systemctl": true, "mount": true, "umount": true,
	"iptables": true, "nft": true,
}

// interpreterCodeFlag maps an interpreter executable to the flag that means
// "the next argument is a program/script to execute" (-c for shells, -c/-e for
// scripting runtimes). Such a wrapper runs arbitrary code, so its tier is
// decided by scanning that code rather than by the interpreter's name.
var interpreterCodeFlag = map[string]string{
	"bash": "-c", "sh": "-c", "zsh": "-c", "dash": "-c", "ksh": "-c", "fish": "-c",
	"python": "-c", "python2": "-c", "python3": "-c",
	"perl": "-e", "ruby": "-e", "node": "-e", "nodejs": "-e", "deno": "-e",
}

// shellEscapes are substrings that indicate code is spawning a subprocess or
// shell, which would run commands outside first-word classification.
var shellEscapes = []string{
	"os.system", "subprocess", "popen", "exec(", "eval(", "system(",
	"child_process", "spawn(", "curl ", "wget ", "| sh", "| bash", "| sudo",
}

// shellComposition are substrings that let code run further commands beyond
// what first-word classification can see: command substitution, command
// chaining, and pipelines. When present, the code is treated as destructive —
// the conservative default, since the full command graph is not visible.
var shellComposition = []string{"$(", "`", ";", "&&", "||", "|"}

// passThroughVerbs are commands that execute a later argument as the real
// command rather than doing the work themselves. First-word matching on these
// would classify the wrapper (benign) instead of the payload (possibly
// destructive), so the real command is located and classified instead.
var passThroughVerbs = map[string]bool{
	"env": true, "nohup": true, "timeout": true, "nice": true,
	"busybox": true, "xargs": true, "command": true, "stdbuf": true,
}

// unwrapPassThrough returns the first argument that names a command (not a
// flag, a KEY=VALUE assignment, or a bare number) together with the arguments
// that follow it, so the caller can classify the real command. It returns
// ("", nil) when args contains no command.
func unwrapPassThrough(args []string) (string, []string) {
	for i, a := range args {
		if a == "" || a[0] == '-' {
			continue
		}
		if strings.Contains(a, "=") {
			continue
		}
		if isNumeric(a) {
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

// isNumeric reports whether s is a run of ASCII digits (a bare duration like
// "5" that timeout/nice take before the real command).
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// codeArg returns the argument immediately following flag (e.g. the code
// string after "-c"), or "" when flag is absent. It also recognizes the flag
// letter inside a combined short-flag cluster ("-ec" carries -c alongside -e),
// which a bare first-word match would otherwise miss.
func codeArg(flag string, args []string) string {
	if len(flag) < 2 {
		return ""
	}
	r := rune(flag[1])
	for i, a := range args {
		if a == flag || (len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], r)) {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
	}
	return ""
}

// codeEscalates reports whether interpreter code is destructive enough to
// warrant Tier 2. It looks for destructive verbs as whole words, common
// subprocess/shell-escape primitives, and shell composition (substitution,
// chaining, pipelines). The check is deliberately conservative: when a wrapper
// runs composed or unclassifiable code, it is assumed destructive.
func codeEscalates(code string) bool {
	lower := strings.ToLower(code)
	for _, tok := range strings.Fields(lower) {
		if destructiveVerbs[tok] {
			return true
		}
	}
	for _, esc := range shellEscapes {
		if strings.Contains(lower, esc) {
			return true
		}
	}
	for _, c := range shellComposition {
		if strings.Contains(lower, c) {
			return true
		}
	}
	return false
}
