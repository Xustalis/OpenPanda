package main

import (
	"reflect"
	"testing"
)

// TestSplitConfig guards the skill subcommand's --config parsing: the flag may
// appear anywhere, and bare/flag-only invocations must not panic (the former
// `args[1:]` in runSkill did).
func TestSplitConfig(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		configPath string
		positional []string
	}{
		{name: "bare", args: nil, configPath: "", positional: nil},
		{name: "list only", args: []string{"list"}, configPath: "", positional: []string{"list"}},
		{name: "config first", args: []string{"--config", "c.yaml"}, configPath: "c.yaml", positional: nil},
		{name: "config last", args: []string{"list", "--config", "c.yaml"}, configPath: "c.yaml", positional: []string{"list"}},
		{name: "config equals", args: []string{"--config=c.yaml", "list"}, configPath: "c.yaml", positional: []string{"list"}},
		{name: "config between positionals", args: []string{"approve", "--config", "c.yaml", "foo"}, configPath: "c.yaml", positional: []string{"approve", "foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, positional := splitConfig(tc.args)
			if path != tc.configPath {
				t.Fatalf("configPath = %q, want %q", path, tc.configPath)
			}
			if !reflect.DeepEqual(positional, tc.positional) {
				t.Fatalf("positional = %v, want %v", positional, tc.positional)
			}
		})
	}
}
