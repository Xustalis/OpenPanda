package defense

import "testing"

// TestTierCrossPlatformDestructiveForms pins the tier inference for the command
// shapes an audit expects to be irreversible. Every case here graded Tier 1
// before v0.0.6: the table knew only POSIX verbs, three interpreters were
// missing entirely, and the attached code-flag form ("python -cCODE") slipped
// past the code scanner because codeArg only ever read the *next* argument.
//
// The Windows rows are not hypothetical — a Windows node executes tasks in this
// network, and "irreversible operations enter pending-approval" is only true if
// `powershell -Command "Remove-Item -Recurse -Force"` is classified as one.
func TestTierCrossPlatformDestructiveForms(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		args []string
	}{
		// Attached code flags: valid for python and pwsh, and previously unscanned.
		{"python -c attached", "python3", []string{"-cimport os; os.remove('/tmp/x')"}},
		{"node --eval=", "node", []string{"--eval=require('fs').rmSync('/tmp/x')"}},
		// Interpreters that were absent from the table.
		{"awk system", "awk", []string{`BEGIN{system("rm -rf /tmp/x")}`}},
		{"lua os.execute", "lua", []string{"-e", "os.execute('rm -rf /tmp/x')"}},
		{"osascript shell", "osascript", []string{"-e", `do shell script "rm -rf /tmp/x"`}},
		{"powershell -Command", "powershell", []string{"-Command", `Remove-Item -Recurse -Force C:\x`}},
		{"powershell.exe path", `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
			[]string{"-command", "Remove-Item x"}},
		{"pwsh -c", "pwsh", []string{"-c", "rm -r -fo /x"}},
		{"pwsh -EncodedCommand", "pwsh", []string{"-EncodedCommand", "cm0gLXJmIC8="}},
		{"cmd /c del", "cmd", []string{"/c", `del /f /s /q C:\x`}},
		{"cmd /k reg", "cmd", []string{"/k", "reg delete HKLM\\Software\\x /f"}},
		// Opaque wrappers: the payload is not the first positional argument.
		{"flock", "flock", []string{"/tmp/l", "rm", "-rf", "/tmp/x"}},
		{"script -c", "script", []string{"-c", "rm -rf /tmp/x", "/dev/null"}},
		{"runuser -c", "runuser", []string{"-c", "rm -rf /tmp/x"}},
		{"watch", "watch", []string{"rm", "-rf", "/tmp/x"}},
		{"taskset", "taskset", []string{"0x1", "rm", "-rf", "/tmp/x"}},
		// Pass-through wrappers whose inner command is positional.
		{"setsid", "setsid", []string{"rm", "-rf", "/tmp/x"}},
		{"ionice value flag", "ionice", []string{"-c", "2", "rm", "-rf", "/tmp/x"}},
		// Destructive verbs the POSIX-only table missed.
		{"truncate", "truncate", []string{"-s", "0", "/tmp/x"}},
		{"shred", "shred", []string{"-u", "/tmp/x"}},
		{"chown", "chown", []string{"-R", "root", "/"}},
		{"ln -sf", "ln", []string{"-sf", "/etc/passwd", "/tmp/x"}},
		{"curl -o", "curl", []string{"-fsSL", "http://x/y.sh", "-o", "/tmp/y.sh"}},
		{"wget", "wget", []string{"http://x/y"}},
		{"crontab", "crontab", []string{"/tmp/evil"}},
		{"launchctl", "launchctl", []string{"load", "/tmp/e.plist"}},
		{"docker run -v /", "docker", []string{"run", "-v", "/:/host", "alpine", "rm", "-rf", "/host"}},
		{"tee", "tee", []string{"/etc/hosts"}},
		{"rsync --delete", "rsync", []string{"-a", "--delete", "/tmp/a/", "/tmp/b/"}},
		{"pip install", "pip", []string{"install", "evil"}},
		{"npm install", "npm", []string{"install", "evil"}},
		{"apt install", "apt", []string{"install", "-y", "evil"}},
		{"winget install", "winget", []string{"install", "evil"}},
		// Windows verbs as bare executables.
		{"taskkill", "taskkill", []string{"/F", "/IM", "x.exe"}},
		{"reg delete", "reg", []string{"delete", `HKLM\Software\x`, "/f"}},
		{"icacls", "icacls", []string{"C:\\x", "/grant", "everyone:F"}},
		// Argument-gated forms.
		{"sed -i", "sed", []string{"-i", "s/a/b/", "/etc/hosts"}},
		{"sed -i.bak", "sed", []string{"-i.bak", "s/a/b/", "/etc/hosts"}},
		{"go install", "go", []string{"install", "example.com/x@latest"}},
		// git subcommands that discard uncommitted work or run remote hooks.
		{"git clone", "git", []string{"clone", "http://x/y"}},
		{"git checkout .", "git", []string{"checkout", "--", "."}},
		{"git switch", "git", []string{"switch", "main"}},
		{"git stash", "git", []string{"stash"}},
		// Controls that were already correct.
		{"plain rm", "rm", []string{"-rf", "/tmp/x"}},
		{"bash -c separated", "bash", []string{"-c", "rm -rf /tmp/x"}},
	}
	for _, c := range cases {
		if got := TierFromCommand(c.cmd, c.args...); got != TierIrreversible {
			t.Errorf("%s: tier = %d, want %d (irreversible)", c.name, got, TierIrreversible)
		}
	}
}

// TestTierStillPassesBenignForms guards the other direction: fail-closed is only
// affordable because ordinary read-only work stays Tier 1 and runs unattended.
// A node that needed approval to read a temperature would defeat the point of
// autonomous scheduling.
func TestTierStillPassesBenignForms(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		args []string
	}{
		{"uname", "uname", []string{"-a"}},
		{"df", "df", []string{"-h", "."}},
		{"vcgencmd", "vcgencmd", []string{"measure_temp"}},
		{"ping", "ping", []string{"-c", "4", "1.1.1.1"}},
		{"git status", "git", []string{"status", "--short"}},
		{"git log", "git", []string{"log", "--oneline", "-5"}},
		{"go build", "go", []string{"build", "./..."}},
		{"go test", "go", []string{"test", "./..."}},
		{"sed to stdout", "sed", []string{"s/a/b/", "file"}},
		{"echo via sh", "sh", []string{"-c", "echo hello"}},
		{"printf via python attached", "python3", []string{"-cprint('hi')"}},
		{"find plain", "find", []string{".", "-name", "*.go"}},
		{"tar list", "tar", []string{"-tf", "a.tar"}},
	}
	for _, c := range cases {
		if got := TierFromCommand(c.cmd, c.args...); got != TierReversible {
			t.Errorf("%s: tier = %d, want %d (reversible)", c.name, got, TierReversible)
		}
	}
}
