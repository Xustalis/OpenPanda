// SPDX-License-Identifier: AGPL-3.0-or-later

package util

import (
	"strings"
	"testing"
)

func TestUUIDv7Format(t *testing.T) {
	id, err := UUIDv7()
	if err != nil {
		t.Fatalf("UUIDv7: %v", err)
	}
	if len(id) != 36 {
		t.Fatalf("expected 36 chars, got %q (%d)", id, len(id))
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("expected 5 dash groups, got %v", parts)
	}
	if len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("bad group lengths: %v", parts)
	}
	// version nibble must be 7
	if parts[2][0] != '7' {
		t.Fatalf("version nibble = %c, want 7", parts[2][0])
	}
	// variant nibble must be 8, 9, a, or b
	switch parts[3][0] {
	case '8', '9', 'a', 'b':
	default:
		t.Fatalf("variant nibble = %c, want 8/9/a/b", parts[3][0])
	}
}

func TestUUIDv7Uniqueness(t *testing.T) {
	n := 1000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id, err := UUIDv7()
		if err != nil {
			t.Fatalf("UUIDv7: %v", err)
		}
		if seen[id] {
			t.Fatalf("duplicate uuid: %s", id)
		}
		seen[id] = true
	}
}

func TestUUIDv7SortOrder(t *testing.T) {
	previous := ""
	for range 10000 {
		id, err := UUIDv7()
		if err != nil {
			t.Fatal(err)
		}
		if id <= previous {
			t.Fatalf("generation order violated: %s then %s", previous, id)
		}
		previous = id
	}
}
