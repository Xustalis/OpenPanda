package util

import (
	"sort"
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
	id1, _ := UUIDv7()
	id2, _ := UUIDv7()
	ids := []string{id1, id2}
	sort.Strings(ids)
	// id2 is created no earlier than id1, so lexical order must not be
	// strictly descending.
	if ids[1] < ids[0] {
		t.Fatalf("lexical order violated: %s then %s", id1, id2)
	}
}
