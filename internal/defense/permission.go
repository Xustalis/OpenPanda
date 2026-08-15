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
// A zero/unknown/negative tier is treated as Tier 2 (fail-closed): an
// unclassified command must not run by omission.
func Authorize(tier int, authorized bool) error {
	if tier != TierReversible && !authorized {
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
	command = normalizeCommand(command)
	if destructiveVerbs[command] {
		return TierIrreversible
	}
	// Pass-through wrappers run a later argument as the real command (env VAR=x
	// cmd, timeout 5 cmd, busybox cmd, …). Classify the inner command, not the
	// wrapper's name, so a destructive payload cannot hide behind the wrapper.
	if passThroughVerbs[command] {
		// env -S/--split-string is special (P1-12): its value is not a flag
		// argument to skip but a command line to split and classify. Treating
		// it as a skipped value let `env -S "rm -rf /"` unwrap to nothing and
		// auto-pass as Tier 1.
		if command == "env" {
			if inner, innerArgs, ok := unwrapEnvSplitString(args); ok {
				return TierFromCommand(inner, innerArgs...)
			}
		}
		if inner, innerArgs := unwrapPassThrough(command, args); inner != "" {
			return TierFromCommand(inner, innerArgs...)
		}
		return TierReversible
	}
	if flag, ok := interpreterCodeFlag[command]; ok {
		if code := codeArg(flag, args); code != "" {
			if codeEscalates(code) {
				return TierIrreversible
			}
		} else if hasPositionalArg(args) {
			// An interpreter invoked with a script file ("bash evil.sh") runs
			// code whose content is not visible here; fail closed to Tier 2.
			return TierIrreversible
		}
	}
	// Commands that are benign with plain arguments but run arbitrary code or
	// destroy state when specific flags/subcommands appear (P1-13): fail
	// closed to Tier 2 when the risky form is present.
	if risk := commandArgRisks[command]; risk != nil && risk(args) {
		return TierIrreversible
	}
	return TierReversible
}

// destructiveVerbs are command names treated as irreversible (Tier 2) by
// default: privilege escalation and destructive filesystem/network/system
// operations (design doc §16).
var destructiveVerbs = map[string]bool{
	"sudo": true, "su": true, "doas": true,
	"rm": true, "dd": true, "mkfs": true,
	"mv": true, "cp": true, "chmod": true,
	"kill": true, "pkill": true,
	"shutdown": true, "reboot": true, "poweroff": true,
	"systemctl": true, "mount": true, "umount": true,
	"iptables": true, "nft": true,
	// Remote execution and arbitrary-recipe execution (P1-13): ssh runs a
	// command on another machine; make runs whatever the Makefile says.
	"ssh": true, "make": true,
}

// interpreterCodeFlag maps an interpreter executable to the flag that means
// "the next argument is a program/script to execute" (-c for shells, -c/-e for
// scripting runtimes). Such a wrapper runs arbitrary code, so its tier is
// decided by scanning that code rather than by the interpreter's name.
var interpreterCodeFlag = map[string]string{
	"bash": "-c", "sh": "-c", "zsh": "-c", "dash": "-c", "ksh": "-c", "fish": "-c",
	"python": "-c", "python2": "-c", "python3": "-c",
	"perl": "-e", "ruby": "-e", "node": "-e", "nodejs": "-e", "deno": "-e",
	"php": "-r",
}

// commandArgRisks are per-command argument scanners (P1-13): the command is
// Tier 1 with plain arguments but fails closed to Tier 2 when a flag or
// subcommand that executes code or destroys state is present. These are
// backstops; an explicit capability-card tier always wins.
var commandArgRisks = map[string]func(args []string) bool{
	// find -exec/-execdir/-ok/-okdir run a command per match; -delete removes
	// every match.
	"find": hasAnyArg("-exec", "-execdir", "-ok", "-okdir", "-delete"),
	// tar --checkpoint-action=exec=CMD and --use-compress-program/-I run an
	// external command mid-archive.
	"tar": func(args []string) bool {
		return hasAnyArgPrefix(args, "--checkpoint-action", "--use-compress-program") ||
			hasAnyArg("-I")(args)
	},
	// git subcommands that run hooks (arbitrary code from .git/hooks) or push
	// to / rewrite shared state.
	"git": func(args []string) bool {
		sub := firstPositional(args)
		return gitRiskySubcommands[sub]
	},
}

// gitRiskySubcommands run hooks or have irreversible/shared-state effects.
var gitRiskySubcommands = map[string]bool{
	"push": true, "commit": true, "merge": true, "rebase": true,
	"reset": true, "clean": true, "filter-branch": true, "update-ref": true,
}

// hasAnyArg reports a scanner that is true when any argument equals one of the
// given flags exactly.
func hasAnyArg(flags ...string) func(args []string) bool {
	return func(args []string) bool {
		for _, a := range args {
			for _, f := range flags {
				if a == f {
					return true
				}
			}
		}
		return false
	}
}

// hasAnyArgPrefix reports whether any argument starts with one of the given
// prefixes (matching both "--flag" and "--flag=value" forms).
func hasAnyArgPrefix(args []string, prefixes ...string) bool {
	for _, a := range args {
		for _, p := range prefixes {
			if a == p || strings.HasPrefix(a, p+"=") {
				return true
			}
		}
	}
	return false
}

// firstPositional returns the first argument that is not a flag (and not a
// value of git's global value-flags like -C/-c), which for git is the
// subcommand.
func firstPositional(args []string) string {
	valueFlags := map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--exec-path": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" {
			continue
		}
		if a[0] == '-' {
			if valueFlags[a] && i+1 < len(args) {
				i++ // skip the flag's value
			}
			continue
		}
		return a
	}
	return ""
}

// unwrapEnvSplitString handles `env -S "cmd args..."`: the flag's value is a
// command line, so it is split on whitespace and the first word is returned as
// the real command. ok is false when no -S flag with a value is present.
func unwrapEnvSplitString(args []string) (string, []string, bool) {
	for i, a := range args {
		var v string
		switch {
		case a == "-S" || a == "--split-string":
			if i+1 >= len(args) {
				return "", nil, false
			}
			v = args[i+1]
		case strings.HasPrefix(a, "--split-string="):
			v = strings.TrimPrefix(a, "--split-string=")
		default:
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			return "", nil, false
		}
		return fields[0], fields[1:], true
	}
	return "", nil, false
}

// shellEscapes are substrings that indicate code is spawning a subprocess or
// shell, which would run commands outside first-word classification.
var shellEscapes = []string{
	"os.system", "subprocess", "popen", "exec(", "eval(", "system(",
	"child_process", "spawn(", "curl ", "wget ", "| sh", "| bash", "| sudo",
}

// shellComposition are substrings that let code run further commands beyond
// what first-word classification can see: command substitution, command
// chaining, pipelines, redirection, backgrounding, and newline-separated
// commands. When present, the code is treated as destructive — the conservative
// default, since the full command graph is not visible.
var shellComposition = []string{"$(", "`", ";", "&&", "||", "|", ">", "&", "\n"}

// passThroughVerbs are commands that execute a later argument as the real
// command rather than doing the work themselves. First-word matching on these
// would classify the wrapper (benign) instead of the payload (possibly
// destructive), so the real command is located and classified instead.
var passThroughVerbs = map[string]bool{
	"env": true, "nohup": true, "timeout": true, "nice": true,
	"busybox": true, "xargs": true, "command": true, "stdbuf": true,
}

// passThroughValueFlags lists, per pass-through wrapper, the flags that take a
// separate argument. That argument belongs to the flag, not the wrapper, so it
// must be skipped rather than classified as the real command — "timeout -s KILL
// 5 rm" runs rm, not KILL.
var passThroughValueFlags = map[string][]string{
	"timeout": {"-s", "--signal", "-k", "--kill-after"},
	"xargs":   {"-I", "--replace", "-E", "--eof", "-n", "--max-args", "-P", "--max-procs", "-L", "--max-lines", "-a", "--arg-file"},
	"nice":    {"-n", "--adjustment"},
	"env":     {"-u", "--unset", "-C", "--chdir", "-S", "--split-string"},
	"stdbuf":  {"-i", "--input", "-o", "--output", "-e", "--error"},
}

// unwrapPassThrough returns the first argument that names a command (not a
// flag, a flag's value, a KEY=VALUE assignment, or a bare number) together with
// the arguments that follow it, so the caller can classify the real command. It
// returns ("", nil) when args contains no command.
func unwrapPassThrough(command string, args []string) (string, []string) {
	valueFlags := passThroughValueFlags[command]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" {
			continue
		}
		if a[0] == '-' {
			if takesValue(a, valueFlags) && i+1 < len(args) {
				i++ // skip the flag's value argument
			}
			continue
		}
		if strings.Contains(a, "=") || isNumeric(a) {
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

// takesValue reports whether a is one of the wrapper's value-taking flags.
func takesValue(a string, valueFlags []string) bool {
	for _, f := range valueFlags {
		if a == f {
			return true
		}
	}
	return false
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

// normalizeCommand reduces a command to its bare executable name: strips a
// directory path and a Windows ".exe" suffix, so "/bin/rm" and "rm.exe" match
// the same verb tables as "rm".
func normalizeCommand(command string) string {
	if i := strings.LastIndexAny(command, `/\`); i >= 0 {
		command = command[i+1:]
	}
	if len(command) > 4 && strings.EqualFold(command[len(command)-4:], ".exe") {
		command = command[:len(command)-4]
	}
	return command
}

// hasPositionalArg reports whether args contains a non-flag argument. For an
// interpreter invoked without a code flag, that argument is a script file (or
// module) whose content cannot be inspected here, so the caller treats it as
// Tier 2.
func hasPositionalArg(args []string) bool {
	for _, a := range args {
		if a != "" && a[0] != '-' {
			return true
		}
	}
	return false
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
// warrant Tier 2. The model is whitelist-first (P1-14): only code recognized
// as a pure-output statement stays Tier 1; anything else — including forms no
// blacklist token matches, like os.remove('x') — fails closed to Tier 2. The
// blacklist scans (destructive words, subprocess/shell escapes, shell
// composition) are kept as a fast, explanatory path.
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
	return !isPureOutput(lower)
}

// pureOutputPrefixes are the statement openings considered provably benign:
// printing a value cannot modify state. Anything else an interpreter runs is
// treated as potentially destructive.
var pureOutputPrefixes = []string{
	"echo ", "print(", "print ", "printf(", "printf ",
	"console.log(", "puts ", "say ",
}

// isPureOutput reports whether the (already lower-cased) code is a single
// pure-output statement. The caller has already verified the code contains no
// shell composition or escape tokens, so "echo $(rm -rf /)" never reaches
// here as safe.
func isPureOutput(lower string) bool {
	trimmed := strings.TrimSpace(lower)
	for _, p := range pureOutputPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}
