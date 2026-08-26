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
	if flags, ok := interpreterCodeFlags[command]; ok {
		if code := codeArg(flags, args); code != "" {
			if codeEscalates(code) {
				return TierIrreversible
			}
		} else if hasPositionalArg(args) {
			// An interpreter invoked with a script file ("bash evil.sh") runs
			// code whose content is not visible here; fail closed to Tier 2.
			return TierIrreversible
		}
	}
	// Wrappers whose payload cannot be located positionally (flock takes a lock
	// file first, watch/time take a whole command line, script/runuser take code
	// behind -c). Rather than guess which argument is the command, judge the
	// joined argv the way interpreter code is judged: only a provably pure-output
	// line stays Tier 1.
	if opaqueWrappers[command] && len(args) > 0 {
		if codeEscalates(strings.Join(args, " ")) {
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
//
// The table is deliberately cross-platform. A Windows node is a first-class
// executor in this network, and a table that only knows POSIX verbs classifies
// `del /f /s /q` as reversible — so the Windows and macOS verbs are listed
// beside their POSIX equivalents rather than left to a later port. Package
// managers are here because installing a dependency mutates the machine outside
// the task's working directory and cannot be undone by discarding the workspace.
//
// Over-inclusion is cheap: this table is only a backstop for a native ability
// whose capability card omits `tier:` (commander.Router.Route). A card that
// declares its own tier always wins, so a node that genuinely wants unattended
// `git checkout` says so in the card.
var destructiveVerbs = map[string]bool{
	"sudo": true, "su": true, "doas": true, "runas": true,
	"rm": true, "dd": true, "mkfs": true,
	"mv": true, "cp": true, "chmod": true,
	"kill": true, "pkill": true, "killall": true,
	"shutdown": true, "reboot": true, "poweroff": true, "halt": true,
	"systemctl": true, "mount": true, "umount": true,
	"iptables": true, "nft": true,
	// Remote execution and arbitrary-recipe execution (P1-13): ssh runs a
	// command on another machine; make runs whatever the Makefile says.
	"ssh": true, "make": true,
	// Destructive or ownership-changing filesystem verbs the first table missed.
	// truncate/shred destroy file contents in place; chown/chgrp/chflags/chattr
	// and the ACL tools hand control of a path to another principal; ln -sf
	// rewrites a path to point somewhere else; tee writes to whatever it is
	// given; rsync --delete mirrors a deletion.
	"truncate": true, "shred": true, "chown": true, "chgrp": true,
	"chflags": true, "chattr": true, "setfacl": true, "ln": true,
	"tee": true, "rsync": true, "scp": true, "sftp": true,
	// Network fetches write a file from an untrusted source and are the first
	// half of every curl-pipe-shell chain.
	"curl": true, "wget": true,
	// Scheduling and service management install work that outlives the task.
	"crontab": true, "at": true, "batch": true, "launchctl": true,
	"systemd-run": true, "service": true, "schtasks": true,
	// Kernel/disk/firmware level state.
	"insmod": true, "rmmod": true, "modprobe": true, "sysctl": true,
	"fdisk": true, "parted": true, "mkswap": true, "swapoff": true,
	"diskutil": true, "hdiutil": true, "tmutil": true,
	// macOS security and system posture.
	"csrutil": true, "spctl": true, "softwareupdate": true, "defaults": true,
	// Containers and cluster control planes: a container can mount the host,
	// and an apply/destroy reshapes infrastructure.
	"docker": true, "podman": true, "nerdctl": true, "kubectl": true,
	"helm": true, "terraform": true, "ansible": true, "ansible-playbook": true,
	// Firewalls beyond iptables/nft.
	"ufw": true, "firewall-cmd": true,
	// Package managers: installing mutates the machine outside the workspace,
	// and the package's own install scripts run arbitrary code. pacman, apk-tools
	// and dpkg are driven by flags rather than subcommands, so they are graded
	// wholesale; the subcommand-driven managers are gated in commandArgRisks so
	// that a plain `npm test` still runs unattended.
	"pacman": true, "dpkg": true, "rpm": true,
	// Windows verbs. cmd builtins (del/rd/copy/move/ren) are unreachable as
	// executables, but codeEscalates scans `cmd /c "…"` token by token against
	// this same table, so listing them is what classifies the payload.
	"del": true, "erase": true, "rd": true, "rmdir": true, "format": true,
	"reg": true, "regedit": true, "taskkill": true, "diskpart": true,
	"bcdedit": true, "netsh": true, "sc": true, "wmic": true, "attrib": true,
	"icacls": true, "cacls": true, "takeown": true, "robocopy": true,
	"xcopy": true, "copy": true, "move": true, "ren": true, "rename": true,
	"mklink": true, "vssadmin": true, "cipher": true, "fsutil": true,
}

// interpreterCodeFlags maps an interpreter executable to the flags that mean
// "the next argument is a program to execute". Such a wrapper runs arbitrary
// code, so its tier is decided by scanning that code rather than by the
// interpreter's name.
//
// Several interpreters accept more than one spelling (pwsh takes -Command and
// -c; cmd takes /c and /k), which is why the value is a list: matching only the
// canonical flag would let the other spelling through unscanned.
var interpreterCodeFlags = map[string][]string{
	"bash": {"-c"}, "sh": {"-c"}, "zsh": {"-c"}, "dash": {"-c"},
	"ksh": {"-c"}, "fish": {"-c"}, "csh": {"-c"}, "tcsh": {"-c"},
	"python": {"-c"}, "python2": {"-c"}, "python3": {"-c"},
	"perl": {"-e"}, "ruby": {"-e"}, "node": {"-e", "-p", "--eval"},
	"nodejs": {"-e"}, "deno": {"-e"}, "php": {"-r"},
	"lua": {"-e"}, "luajit": {"-e"}, "tclsh": {"-c"},
	"rscript": {"-e"}, "julia": {"-e"}, "groovy": {"-e"},
	// awk's program is a positional argument, so hasPositionalArg is what
	// classifies `awk 'BEGIN{system("…")}'`; -f names a program file.
	"awk": {"-f"}, "gawk": {"-f"}, "mawk": {"-f"},
	// macOS: osascript -e runs AppleScript, which reaches the shell through
	// `do shell script`.
	"osascript": {"-e"},
	// Windows shells. normalizeCommand has already stripped ".exe", and codeArg
	// matches flags case-insensitively, so -Command and -command both scan.
	"powershell": {"-Command", "-EncodedCommand", "-File", "-c"},
	"pwsh":       {"-Command", "-EncodedCommand", "-File", "-c"},
	"cmd":        {"/c", "/k"},
}

// opaqueWrappers run a command that cannot be located by position: flock takes
// a lock file before the command, watch/time take a whole command line, script
// and runuser hide it behind -c. The joined argv is judged as interpreter code
// instead of guessing which token is the payload.
var opaqueWrappers = map[string]bool{
	"flock": true, "watch": true, "time": true, "script": true,
	"runuser": true, "taskset": true, "chrt": true, "setarch": true,
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
	// sed -i edits files in place; without it sed only writes to stdout. The
	// suffix form (-i.bak) is the same flag, so this is a prefix test.
	"sed": hasAnyShortFlagPrefix("-i", "--in-place"),
	// Toolchains that are read-only for build/list but mutate the machine or run
	// downloaded code for install/publish/run.
	"go":     subcommandIn("install", "run", "generate", "clean", "get"),
	"cargo":  subcommandIn("install", "publish", "run"),
	"dotnet": subcommandIn("run", "publish", "tool"),
}

// pkgMutatingSubcommands are the subcommands that make a package manager change
// the machine: they install, remove or upgrade software, or run code shipped in
// a package. Listing (list/search/info/outdated) and test/build scripts are
// absent on purpose — a node that needed approval to run `npm test` could not
// work unattended, which is the whole point of the scheduler.
var pkgMutatingSubcommands = []string{
	"install", "i", "add", "ci", "reinstall", "update", "upgrade", "up",
	"dist-upgrade", "remove", "rm", "uninstall", "erase", "purge",
	"autoremove", "publish", "link", "unlink", "exec", "dlx", "run",
	"create", "init", "sync", "bootstrap",
}

// pkgManagers are the subcommand-driven package managers gated by
// pkgMutatingSubcommands.
var pkgManagers = []string{
	"apt", "apt-get", "aptitude", "yum", "dnf", "zypper", "apk",
	"snap", "flatpak", "brew", "port", "nix-env",
	"pip", "pip3", "pipx", "uv", "poetry", "conda", "mamba",
	"npm", "pnpm", "yarn", "bun", "gem", "composer", "cpan",
	"choco", "winget", "scoop",
}

func init() {
	mutating := subcommandIn(pkgMutatingSubcommands...)
	for _, m := range pkgManagers {
		// A manager that already has a bespoke scanner keeps it.
		if _, exists := commandArgRisks[m]; !exists {
			commandArgRisks[m] = mutating
		}
	}
}

// hasAnyShortFlagPrefix returns a scanner that is true when an argument is one
// of flags, or is that flag with a value attached ("-i.bak", "--in-place=.bak").
func hasAnyShortFlagPrefix(flags ...string) func(args []string) bool {
	return func(args []string) bool {
		for _, a := range args {
			for _, f := range flags {
				if a == f || strings.HasPrefix(a, f+"=") ||
					(!strings.HasPrefix(f, "--") && strings.HasPrefix(a, f)) {
					return true
				}
			}
		}
		return false
	}
}

// subcommandIn returns a scanner that is true when the first positional
// argument is one of subs.
func subcommandIn(subs ...string) func(args []string) bool {
	set := make(map[string]bool, len(subs))
	for _, s := range subs {
		set[s] = true
	}
	return func(args []string) bool { return set[firstPositional(args)] }
}

// gitRiskySubcommands run hooks or have irreversible/shared-state effects.
// checkout/switch/restore/stash discard uncommitted work in the tree — the most
// common way an agent destroys work that was never committed anywhere — and
// clone runs the remote's hooks and config on first checkout.
var gitRiskySubcommands = map[string]bool{
	"push": true, "commit": true, "merge": true, "rebase": true,
	"reset": true, "clean": true, "filter-branch": true, "update-ref": true,
	"checkout": true, "switch": true, "restore": true, "stash": true,
	"clone": true, "am": true, "cherry-pick": true, "revert": true,
	"apply": true, "gc": true, "prune": true, "worktree": true,
	"submodule": true, "remote": true, "config": true, "tag": true,
	"branch": true, "mv": true, "rm": true,
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
	// Same shape (wrapper [flags] cmd args…): detaching, unbuffering and
	// re-prioritising a command all leave the payload as the first positional.
	"setsid": true, "unbuffer": true, "ionice": true, "eatmydata": true,
	"proxychains": true, "proxychains4": true,
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
	"ionice":  {"-c", "--class", "-n", "--classdata", "-p", "--pid"},
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

// codeArg returns the code an interpreter was asked to run, trying each flag
// the interpreter accepts, or "" when none of them appears. Separated, attached
// and long-option spellings all resolve (see codeArgFor).
func codeArg(flags []string, args []string) string {
	for _, flag := range flags {
		if len(flag) < 2 {
			continue
		}
		if code, ok := codeArgFor(flag, args); ok {
			return code
		}
	}
	return ""
}

// codeArgFor locates one flag's value, in all three spellings an interpreter
// accepts: separated ("-c CODE"), attached ("-cCODE", which python and pwsh both
// take), and long-option ("--eval=CODE"). It reports whether the flag was found
// at all, so an empty-but-present value is not retried as a different flag.
//
// The attached form used to fall through this function entirely: the cluster
// test below matched "-cimport os; os.remove('x')" as a short-flag group, then
// read the *next* argument — which does not exist — and returned "". The code
// was never scanned and the command graded Tier 1. Returning the remainder after
// the flag letter is what closes that.
func codeArgFor(flag string, args []string) (string, bool) {
	long := strings.HasPrefix(flag, "--") || flag[0] == '/' && len(flag) > 2
	for i, a := range args {
		if strings.EqualFold(a, flag) {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if len(a) <= len(flag) {
			continue
		}
		// "--eval=CODE" / "/c:CODE": value after the separator.
		if strings.EqualFold(a[:len(flag)], flag) && (a[len(flag)] == '=' || a[len(flag)] == ':') {
			return a[len(flag)+1:], true
		}
		if long {
			continue
		}
		// Attached short form: "-cCODE", or a short-flag cluster ending in the
		// code flag ("-uc CODE" → the cluster carries the letter, the value is
		// the next argument).
		if a[0] != flag[0] || a[1] == '-' {
			continue
		}
		if rest, ok := attachedShortValue(a, rune(flag[1])); ok {
			if rest != "" {
				return rest, true
			}
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

// attachedShortValue splits a short-flag argument at the code flag's letter and
// returns whatever follows it. "-cCODE" with letter 'c' yields "CODE"; "-uc"
// yields "" (the value is the next argument); an argument without the letter
// yields ok=false.
func attachedShortValue(arg string, letter rune) (string, bool) {
	for i, r := range arg[1:] {
		if r == letter {
			return arg[1+i+len(string(letter)):], true
		}
	}
	return "", false
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
