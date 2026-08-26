package main

// Argument-position completion is pure line arithmetic on top of a resolver,
// so it is tested without a terminal: the resolver is a stub and the
// assertions are about which token gets replaced and what the menu offers.

import (
	"reflect"
	"testing"
)

// stubResolver answers for a fictional command set that exercises every shape:
// a single-candidate slot, a many-candidate slot with a common prefix, and a
// position-dependent slot.
func stubResolver(cmd string, args []string) []string {
	switch cmd {
	case "task":
		if len(args) == 1 {
			return []string{"01J8-aaaa", "01J8-bbbb", "02K9-cccc"}
		}
	case "lang":
		if len(args) == 1 {
			return []string{"en", "zh-CN", "ja", "es", "de"}
		}
	case "resume":
		if len(args) == 1 {
			return []string{"sess-1"}
		}
	case "config":
		switch len(args) {
		case 1:
			return []string{"set"}
		case 2:
			return []string{"approval", "injection"}
		case 3:
			if args[1] == "approval" {
				return []string{"always", "on_request", "never"}
			}
		}
	}
	return nil
}

func TestArgCandidatesFor(t *testing.T) {
	cases := []struct {
		line  string
		token string
		want  []string
	}{
		// Still on the command name: not an argument position at all.
		{"/task", "", nil},
		{"/ta", "", nil},
		// A trailing space opens the first argument: everything is offered.
		{"/task ", "", []string{"01J8-aaaa", "01J8-bbbb", "02K9-cccc"}},
		{"/task 01J8", "01J8", []string{"01J8-aaaa", "01J8-bbbb"}},
		{"/task 02", "02", []string{"02K9-cccc"}},
		{"/task zz", "zz", nil},
		// Case folding: locale codes are typed lowercase.
		{"/lang zh", "zh", []string{"zh-CN"}},
		// A candidate the user has already typed in full needs no menu.
		{"/resume sess-1", "sess-1", nil},
		// Position-dependent: the third slot depends on the second.
		{"/config ", "", []string{"set"}},
		{"/config set ", "", []string{"approval", "injection"}},
		{"/config set approval ", "", []string{"always", "never", "on_request"}},
		{"/config set approval al", "al", []string{"always"}},
		// Not a command line.
		{"explain this", "", nil},
		{"/task a\nb", "", nil},
	}
	for _, tc := range cases {
		token, got := argCandidatesFor(tc.line, stubResolver)
		if token != tc.token {
			t.Errorf("argCandidatesFor(%q) token = %q, want %q", tc.line, token, tc.token)
		}
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("argCandidatesFor(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestArgCandidatesForNilResolver(t *testing.T) {
	if _, got := argCandidatesFor("/task 01", nil); got != nil {
		t.Errorf("nil resolver returned candidates: %v", got)
	}
}

func TestReplArgCandidatesEnums(t *testing.T) {
	r := &repl{}
	// The store-backed slots are nil-safe: a REPL without a store completes
	// nothing rather than panicking.
	if got := r.argCandidates("task", []string{""}); got != nil {
		t.Errorf("task ids without a store = %v, want nil", got)
	}
	if got := r.argCandidates("lang", []string{""}); len(got) < 2 {
		t.Errorf("lang candidates = %v, want the locale list", got)
	}
	if got := r.argCandidates("config", []string{"set", "approval", ""}); len(got) != 3 {
		t.Errorf("approval values = %v, want three modes", got)
	}
	if got := r.argCandidates("config", []string{"set", "nope", ""}); got != nil {
		t.Errorf("unknown section = %v, want nil", got)
	}
	// Free-text arguments must stay free text — a wrong completion in the
	// middle of a question is worse than no completion.
	if got := r.argCandidates("ask", []string{"why"}); got != nil {
		t.Errorf("ask completions = %v, want nil", got)
	}
	if got := r.argCandidates("reject", []string{"id", "because"}); got != nil {
		t.Errorf("reject reason completions = %v, want nil", got)
	}
}
