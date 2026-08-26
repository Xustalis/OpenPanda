package main

import "testing"

func TestSuggestTypos(t *testing.T) {
	cmds := []string{"status", "queue", "task", "tasks", "ask", "web", "session", "help"}
	cases := map[string]string{
		"statsu":  "status", // transposition
		"stauts":  "status",
		"tsaks":   "tasks",
		"quue":    "queue", // deletion
		"webb":    "web",   // insertion
		"hepl":    "help",
		"stat":    "status", // truncation → prefix match
		"":        "",
		"xyzzy":   "",     // nothing close
		"cluster": "",     // real word, no neighbour
		"task":    "task", // exact still returns itself
	}
	for in, want := range cases {
		if got := suggest(in, cmds); got != want {
			t.Errorf("suggest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestPrefersShortestPrefix(t *testing.T) {
	// "task" and "tasks" both start with "tas"; the shorter completion is the
	// one a user typing a prefix most likely meant.
	if got := suggest("tas", []string{"tasks", "task"}); got != "task" {
		t.Errorf("suggest(tas) = %q, want task", got)
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"abc", "acb", 1}, // transposition
		{"ab", "ba", 1},   // transposition at the head
		{"kitten", "sitting", 3},
		{"状态", "状况", 1}, // multi-byte runes count as one edit, not three
	}
	for _, c := range cases {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSuggestSlashNames(t *testing.T) {
	// The REPL passes bare names (no leading slash) so the caller can format
	// the answer as "/tasks" itself.
	names := make([]string, 0, len(replCommands))
	for _, c := range replCommands {
		names = append(names, c.name)
	}
	if got := suggest("tsaks", names); got != "tasks" {
		t.Errorf("suggest(tsaks) = %q, want tasks", got)
	}
	if got := suggest("mem", names); got != "memory" {
		t.Errorf("suggest(mem) = %q, want memory", got)
	}
}
