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
	return c, nil
}
