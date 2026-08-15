package ledger

import (
	"fmt"
	"os"

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

// validateResourceProfile sanity-checks a hand-declared resource profile. A zero
// profile is allowed (the field is optional in MVP); a declared one must not
// contain negative resource counts or an unknown duration hint.
func validateResourceProfile(r ResourceProfile) error {
	if r.CPU < 0 || r.RAMGB < 0 || r.GPUVRAMGB < 0 {
		return fmt.Errorf("resource_profile: cpu/ram_gb/gpu_vram_gb must be non-negative")
	}
	switch r.DurationHint {
	case "", "short", "long":
		return nil
	default:
		return fmt.Errorf("resource_profile: duration_hint %q invalid (short|long)", r.DurationHint)
	}
}
