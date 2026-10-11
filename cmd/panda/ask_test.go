// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestAnswerTail pins the end-of-turn reconcile: whatever slice of the parsed
// answer the live stream never delivered must come back — a strict tail, a
// full non-streamed answer, or a divergent replay — while an honest stream
// returns "" so nothing prints twice.
func TestAnswerTail(t *testing.T) {
	cases := []struct {
		name     string
		streamed string
		answer   string
		want     string
	}{
		{"nothing streamed", "", "完整回答", "完整回答"},
		{"fully streamed", "完整回答", "完整回答", ""},
		{"truncated stream", "前一半", "前一半后一半", "后一半"},
		{"truncation notice", "正文", "正文\n\n[回答因长度上限被截断]", "\n\n[回答因长度上限被截断]"},
		{"suffix is only whitespace", "正文", "正文\n\n", ""},
		{"streamed contains answer (unwrapped envelope)", "前导：里面的话", "里面的话", ""},
		{"divergent replay", "别的文本", "完整回答", "完整回答"},
		{"empty answer", "任何", "", ""},
		{"whitespace answer", "任何", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := answerTail(tc.streamed, tc.answer); got != tc.want {
				t.Fatalf("answerTail(%q, %q) = %q, want %q", tc.streamed, tc.answer, got, tc.want)
			}
		})
	}
	// Whatever the shape, streamed+tail must always render the answer whole —
	// unless the stream already contained it (the unwrap case), where the
	// parsed form is a substring, not the display text.
	for _, tc := range cases {
		got := tc.streamed + answerTail(tc.streamed, tc.answer)
		if want := strings.TrimSpace(tc.answer); want != "" && !strings.Contains(got, want) {
			t.Fatalf("%s: stream+tail %q lost the answer %q", tc.name, got, tc.answer)
		}
	}
}

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
