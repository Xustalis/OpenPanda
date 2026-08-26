package ledger

import (
	"fmt"
	"os"
	"os/exec"

	"gopkg.in/yaml.v3"
)

// LoadCard reads and parses a capabilities.yaml file. A missing file is an
// error because a node with no declared capabilities is not useful.
func LoadCard(path string) (Card, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Card{}, fmt.Errorf("read capabilities %s: %w", path, err)
	}
	var c Card
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Card{}, fmt.Errorf("parse capabilities %s: %w", path, err)
	}
	if c.Device == "" {
		return Card{}, fmt.Errorf("capabilities %s: device must not be empty", path)
	}
	if err := validateResourceProfile(c.ResourceProfile); err != nil {
		return Card{}, fmt.Errorf("capabilities %s: %w", path, err)
	}
	return c, nil
}

// lookPath reports whether an executable resolves on this host. It is a var so
// PruneUnavailableNative can be tested without depending on what the test
// machine happens to have installed.
var lookPath = func(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// PruneUnavailableNative drops the native abilities whose command does not exist
// on this host and returns the ids it dropped, so the caller can log them.
//
// Cards travel between machines — the shipped examples declare uname/df/ping,
// and an operator copying the desktop card onto a Windows node inherits three
// commands that host has never had. A native ability that cannot run is worse
// than one that was never declared: routing puts native ahead of agent, so the
// undeliverable ability wins the plan and the task dies at exec with 127 instead
// of falling through to an agent or to another node that can do the work. Worse,
// the card is advertised in the hello handshake, so peers route to the phantom
// ability too.
//
// This runs once at load rather than inside MatchNative: PATH does not change
// under a running daemon, and a LookPath per routing decision would put a
// filesystem walk in the hot path of every task.
func (c *Card) PruneUnavailableNative() []string {
	if len(c.Native) == 0 {
		return nil
	}
	kept := make([]NativeAbility, 0, len(c.Native))
	var dropped []string
	for _, ab := range c.Native {
		if ab.Command != "" && lookPath(ab.Command) {
			kept = append(kept, ab)
			continue
		}
		dropped = append(dropped, ab.ID)
	}
	if len(dropped) == 0 {
		return nil
	}
	c.Native = kept
	return dropped
}

// validateResourceProfile sanity-checks a hand-declared resource profile. A zero
// profile is allowed (the field is optional in MVP); a declared one must not
// contain negative resource counts or an unknown duration hint. The sole
// negative allowed is gpu_vram_gb: GPUVRAMUnknown (-1), which a rescan writes
// for a card whose size no probe could read.
func validateResourceProfile(r ResourceProfile) error {
	if r.CPU < 0 || r.RAMGB < 0 {
		return fmt.Errorf("resource_profile: cpu/ram_gb must be non-negative")
	}
	if r.GPUVRAMGB < 0 && r.GPUVRAMGB != GPUVRAMUnknown {
		return fmt.Errorf("resource_profile: gpu_vram_gb must be non-negative or %d (present, size unknown)", GPUVRAMUnknown)
	}
	switch r.DurationHint {
	case "", "short", "long":
		return nil
	default:
		return fmt.Errorf("resource_profile: duration_hint %q invalid (short|long)", r.DurationHint)
	}
}
