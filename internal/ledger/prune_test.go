package ledger

import "testing"

// A card that travels to another machine brings commands that machine may not
// have. The pruning has to remove exactly those and leave the rest — including
// the abilities that are still runnable — because dropping too much silently
// downgrades the node and dropping too little makes routing pick a plan that
// dies at exec with 127.
func TestPruneUnavailableNativeDropsOnlyMissingCommands(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(name string) bool { return name == "uname" || name == "ping" }

	c := Card{Device: "d", Native: []NativeAbility{
		{ID: "sys:info", Command: "uname", Args: []string{"-a"}},
		{ID: "disk:usage", Command: "df"},
		{ID: "temp:read", Command: "vcgencmd"},
		{ID: "net:ping", Command: "ping"},
		{ID: "broken", Command: ""}, // no command at all is unrunnable too
	}}

	dropped := c.PruneUnavailableNative()
	want := map[string]bool{"disk:usage": true, "temp:read": true, "broken": true}
	if len(dropped) != len(want) {
		t.Fatalf("dropped %v, want the 3 unavailable ids", dropped)
	}
	for _, id := range dropped {
		if !want[id] {
			t.Errorf("dropped %q, which is runnable here", id)
		}
	}
	if len(c.Native) != 2 || c.Native[0].ID != "sys:info" || c.Native[1].ID != "net:ping" {
		t.Fatalf("kept %+v, want sys:info and net:ping in order", c.Native)
	}
	// The surviving ability keeps its args: pruning filters, it never rewrites.
	if len(c.Native[0].Args) != 1 || c.Native[0].Args[0] != "-a" {
		t.Errorf("args mangled: %+v", c.Native[0].Args)
	}
}

// Nothing to prune must be a no-op that reports nothing, so the caller's log
// line only ever appears when a real ability was removed.
func TestPruneUnavailableNativeIsSilentWhenEverythingResolves(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(string) bool { return true }

	c := Card{Device: "d", Native: []NativeAbility{{ID: "sys:info", Command: "uname"}}}
	if dropped := c.PruneUnavailableNative(); dropped != nil {
		t.Fatalf("dropped %v from a fully available card", dropped)
	}
	if len(c.Native) != 1 {
		t.Fatalf("native mutated: %+v", c.Native)
	}

	empty := Card{Device: "d"}
	if dropped := empty.PruneUnavailableNative(); dropped != nil {
		t.Fatalf("dropped %v from a card with no native block", dropped)
	}
}
