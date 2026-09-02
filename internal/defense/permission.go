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
	if irreversibleVerbs[command] {
		return TierIrreversible
	}
	// Filesystem builders ship as one binary per filesystem — mkfs.ext4,
	// mkfs.vfat, newfs_hfs, newfs_msdos — so the family prefix is the verb. A
	// table keyed on "mkfs" alone never matched the name anyone actually runs.
	if strings.HasPrefix(command, "mkfs.") || strings.HasPrefix(command, "newfs_") {
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
		// An encoded program cannot be read, so it is graded by what it could
		// hold. Checked before codeArg because the value that would return is the
		// base64 blob itself, which scans clean.
		if hasEncodedCodeFlag(args) {
			return TierIrreversible
		}
		if code := codeArg(flags, args); code != "" {
			if codeEscalates(code) {
				return TierIrreversible
			}
		} else if pos := firstPositional(args); pos != "" {
			// No code flag, but a positional argument. For awk and friends that
			// argument *is* the program, so scan it; for a shell it is a script
			// path, which scans clean and stays Tier 1.
			//
			// This used to fail closed on the mere presence of a positional,
			// which made every `bash scripts/build.sh` an approval prompt. A
			// script path is not evidence of irreversibility, and treating it as
			// evidence is what stopped a node from running its own build.
			if codeEscalates(pos) {
				return TierIrreversible
			}
		}
	}
	// Wrappers whose payload cannot be located positionally (flock takes a lock
	// file first, watch/time take a whole command line, script/runuser take code
	// behind -c). Rather than guess which argument is the command, judge the
	// joined argv the way interpreter code is judged.
	if opaqueWrappers[command] && len(args) > 0 {
		if codeEscalates(strings.Join(args, " ")) {
			return TierIrreversible
		}
	}
	// Commands that are Tier 1 with ordinary arguments and irreversible in one
	// specific form (git push --force, rsync --delete, sed -i).
	if risk := commandArgRisks[command]; risk != nil && risk(args) {
		return TierIrreversible
	}
	return TierReversible
}

// hasEncodedCodeFlag reports whether an interpreter was handed a base64-encoded
// program (PowerShell's -EncodedCommand and its abbreviations). Matched
// case-insensitively and by prefix, since -enc, -encod and -EncodedCommand are
// all accepted by pwsh.
func hasEncodedCodeFlag(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") {
			if strings.HasPrefix(strings.ToLower(strings.TrimLeft(a, "-/")), "enc") {
				return true
			}
		}
	}
	return false
}

// irreversibleVerbs are the command names that cannot be undone, and are the
// only names that classify as Tier 2 on their own.
//
// The policy this table encodes: **Tier 2 is for an irreversible loss of data or
// of the machine's availability — something no later command can put back.**
// Everything else is Tier 1 and runs unattended, because a node that has to ask
// before it can copy a file, install a dependency or restart a service cannot do
// the work it exists to do. That is the whole point of the scheduler.
//
// It used to be a ~90-entry "destructive" table that graded cp, mv, chmod, kill,
// curl, wget, make, ssh, mount, systemctl, every package manager and every
// container/infra CLI as Tier 2. All of those are recoverable — you can delete a
// downloaded file, uninstall a package, restart a service — so all of them now
// pass. What is left is deletion, disk/partition/firmware, power state, and
// privilege escalation (which is a blank cheque for all three).
//
// The table stays cross-platform: a Windows node is a first-class executor here,
// and `del /f /s /q` destroys exactly as much as `rm -rf`. codeEscalates scans
// interpreter code against this same table, so the classification of a verb is
// the same whether it is the command or is buried in `cmd /c "…"`.
//
// Anything a particular node wants gated beyond this list says so in its
// capability card: an explicit `tier: 2` on a native ability always wins over
// this inference.
var irreversibleVerbs = map[string]bool{
	// Privilege escalation: whatever follows can do everything below.
	"sudo": true, "su": true, "doas": true, "runas": true,
	// Data destruction. rm/dd overwrite or unlink; shred and truncate destroy
	// contents in place with no copy left anywhere.
	"rm": true, "dd": true, "shred": true, "truncate": true,
	// Filesystem creation wipes whatever the target held.
	"mkfs": true, "mkswap": true, "newfs": true,
	// Disk, partition and firmware level state: a mistake here can leave the
	// machine unable to boot, which no later command can fix from inside it.
	"fdisk": true, "parted": true, "sfdisk": true, "swapoff": true,
	"diskutil": true, "hdiutil": true, "tmutil": true, "asr": true,
	// Power state: the task's own machine stops answering.
	"shutdown": true, "reboot": true, "poweroff": true, "halt": true,
	// Windows equivalents of everything above. cmd builtins are not reachable as
	// executables, but codeEscalates scans `cmd /c "…"` against this table, so
	// listing them is what classifies the payload.
	"del": true, "erase": true, "rd": true, "rmdir": true,
	"format": true, "diskpart": true, "bcdedit": true,
	"vssadmin": true, "cipher": true, "fsutil": true,
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
	// awk's program is its first positional argument, so the positional scan in
	// TierFromCommand is what classifies `awk 'BEGIN{system("…")}'`; -f names a
	// program file instead.
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

// commandArgRisks are per-command argument scanners: the command is Tier 1 with
// ordinary arguments and Tier 2 only in the form that cannot be undone. This is
// where the policy gets its precision — `git push` is routine, `git push --force`
// overwrites history; `rsync` copies, `rsync --delete` mirrors a deletion.
//
// The list used to include every package manager (install/remove/run), plus
// go/cargo/dotnet run|install|generate, and tar/find -exec on the grounds that
// they execute code. Executing code is not the test; irreversibility is. A build
// or an install can be repeated or undone, so those are gone: `npm install`,
// `npm run build` and `go run ./...` now run unattended.
var commandArgRisks = map[string]func(args []string) bool{
	// find -delete removes every match, with no copy left behind. -exec is not
	// here: what it runs is classified on its own merits when it runs.
	"find": hasAnyArg("-delete"),
	// sed -i rewrites the file in place; without a backup suffix the original
	// content is gone. Plain sed only writes to stdout.
	"sed": hasAnyShortFlagPrefix("-i", "--in-place"),
	// rsync --delete makes the destination mirror the source, deleting whatever
	// the source no longer has.
	"rsync": hasAnyArg("--delete", "--delete-after", "--delete-before", "--delete-during", "--del"),
	// git: only the forms that destroy work no reflog or remote still holds.
	"git": func(args []string) bool { return gitArgsIrreversible(args) },
}

// gitArgsIrreversible reports whether a git invocation is one of the forms that
// loses work permanently. Everything else about git — commit, merge, rebase,
// clone, fetch, switching branches — is either recoverable from the reflog or
// not a mutation at all, and gating it made an agent ask permission to do version
// control.
func gitArgsIrreversible(args []string) bool {
	sub := firstPositional(args)
	force := hasAnyArg("-f", "--force")(args)
	switch sub {
	case "push":
		// A force push overwrites a branch other machines have; their commits are
		// only in their own reflogs, if they still exist at all.
		return force
	case "reset":
		// --hard discards uncommitted work in the tree; nothing holds a copy.
		return hasAnyArg("--hard")(args)
	case "clean":
		// -f/-x removes untracked files, which by definition are in no commit.
		return force || hasAnyArg("-x", "-fd", "-fdx", "-ffd")(args)
	case "checkout", "restore":
		// Path forms ("git checkout -- .", "git restore src/") throw away
		// uncommitted edits. Switching branches does not, and is the common case.
		return force || hasAnyArg("--", ".")(args)
	case "branch":
		// -D deletes an unmerged branch: its commits become unreachable.
		return hasAnyArg("-D")(args)
	case "stash":
		return subcommandIn("drop", "clear")(args[1:])
	case "filter-branch":
		// Rewrites every commit in place.
		return true
	}
	return false
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

// irreversibleCodePatterns are substrings in interpreter code that reach an
// irreversible operation the token scan cannot see. Two kinds:
//
//   - Language-level deletion. `os.remove(...)`, `fs.rmSync(...)` and
//     `Remove-Item` destroy files without any token named "rm" appearing.
//   - Opacity. Decoded, encoded or piped-into-a-shell code is not readable here
//     at all, so it is graded by what it could be rather than by what it says.
//     This is the one place the classifier still fails closed, and it is narrow
//     on purpose: an ordinary pipeline is fine, a base64 blob fed to a shell is
//     not.
//
// Composition itself is deliberately absent. `$(`, `|`, `;`, `&&`, `>` used to
// escalate on sight, which made `bash -c "ls | wc -l"` a Tier 2 operation
// requiring human approval. The token scan reads every stage of a pipeline and
// both sides of a `&&`, so it no longer needs the blunt instrument.
var irreversibleCodePatterns = []string{
	// Deletion through a language runtime rather than a command.
	"os.remove", "os.unlink", "os.rmdir", "os.removedirs", "shutil.rmtree",
	"removeall", "rmsync", "unlinksync", "rmdirsync", "unlink(",
	"remove-item", "fileutils.rm", "file.delete", "clear-disk", "format-volume",
	// Opacity: code that is decoded, or handed to a shell, at run time.
	"base64 -d", "base64 --decode", "-encodedcommand", "frombase64string",
	"eval ", "eval(", "| sh", "|sh", "| bash", "|bash", "| zsh", "|zsh",
	"| sudo", "|sudo", "iex ", "invoke-expression",
}

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

// codeEscalates reports whether interpreter code reaches an irreversible
// operation. It is the classifier for anything that runs code rather than doing
// work itself: `bash -c`, `python -c`, `cmd /c`, an opaque wrapper's joined argv.
//
// The question it answers changed with the policy. It used to end in
// `return !isPureOutput(lower)` — anything that was not literally an `echo` was
// Tier 2, so `bash -c "ls"` needed human approval. Now it asks whether the code
// names something that cannot be undone, or hides what it runs.
func codeEscalates(code string) bool {
	lower := strings.ToLower(code)
	for _, tok := range codeTokens(lower) {
		if irreversibleVerbs[tok] {
			return true
		}
	}
	for _, pat := range irreversibleCodePatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// codeTokens splits code into command-position candidates. It breaks on shell
// metacharacters as well as whitespace, so every stage of a pipeline and both
// sides of a `;` or `&&` are scanned — `ls;rm -rf /` yields "rm", where a plain
// whitespace split yields "ls;rm" and matches nothing. Quotes and parentheses
// are separators too, which is what finds the verb inside `os.execute('rm -rf x')`
// and `do shell script "rm -rf x"`.
func codeTokens(lower string) []string {
	return strings.FieldsFunc(lower, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '|', '&', ';', '(', ')', '`', '<', '>',
			'"', '\'', '{', '}', '[', ']', ',', '=':
			return true
		}
		return false
	})
}
