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
		{"zero tier treated as reversible", 0, false, false},
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
