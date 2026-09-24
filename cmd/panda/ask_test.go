package main

import (
	"reflect"
	"testing"
)

func TestParseSubcommand(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		want       string
		wantArgs   []string
		wantConfig string
	}{
		// --config is a global flag: extractGlobalFlags lifts it wherever it
		// sits, so the subcommand never sees the token at all.
		{"subcommand first", []string{"status", "--config", "x.yaml"}, "status", []string{}, "x.yaml"},
		{"global flags first", []string{"--config", "x.yaml", "status"}, "status", []string{}, "x.yaml"},
		{"single dash", []string{"status", "-config", "x.yaml"}, "status", []string{}, "x.yaml"},
		{"equals form", []string{"--config=x.yaml", "status"}, "status", []string{}, "x.yaml"},
		{"card before ask", []string{"--config", "x.yaml", "--card", "c.yaml", "ask", "hi"}, "ask", []string{"hi"}, "x.yaml"},
		{"config after prompt", []string{"ask", "hi", "-config", "x.yaml"}, "ask", []string{"hi"}, "x.yaml"},
		{"repl no flags", []string{}, "", []string{}, ""},
		{"non-global flag forwards", []string{"--watch", "queue"}, "queue", []string{"--watch"}, ""},
		{"dashdash ends flags", []string{"ask", "--", "-config"}, "ask", []string{"--", "-config"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cliConfigPath, cliCardPath, cliMCP = "", "", ""
			jsonOutput = false
			sub, args := parseSubcommand(extractGlobalFlags(tc.args))
			if sub != tc.want {
				t.Fatalf("subcommand = %q, want %q", sub, tc.want)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tc.wantArgs)
			}
			if cliConfigPath != tc.wantConfig {
				t.Fatalf("cliConfigPath = %q, want %q", cliConfigPath, tc.wantConfig)
			}
		})
	}
}
