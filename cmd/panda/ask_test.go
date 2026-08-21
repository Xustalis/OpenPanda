package main

import (
	"reflect"
	"testing"
)

func TestParseSubcommand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		want     string
		wantArgs []string
	}{
		{"subcommand first", []string{"status", "--config", "x.yaml"}, "status", []string{"--config", "x.yaml"}},
		{"global flags first", []string{"--config", "x.yaml", "status"}, "status", []string{"--config", "x.yaml"}},
		{"card before ask", []string{"--config", "x.yaml", "--card", "c.yaml", "ask", "hi"}, "ask", []string{"--config", "x.yaml", "--card", "c.yaml", "hi"}},
		{"repl no flags", []string{}, "", []string{}},
		// No subcommand: flags pass through untouched so the default REPL
		// target parses --config/--card/--mcp itself.
		{"repl with flags", []string{"--config", "x.yaml"}, "", []string{"--config", "x.yaml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, rest := parseSubcommand(tc.args)
			if sub != tc.want {
				t.Fatalf("subcommand = %q, want %q", sub, tc.want)
			}
			if !reflect.DeepEqual(rest, tc.wantArgs) {
				t.Fatalf("args = %v, want %v", rest, tc.wantArgs)
			}
		})
	}
}
