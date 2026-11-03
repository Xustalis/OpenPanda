// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"
)

func TestIntakeDefaultsToLoopback(t *testing.T) {
	cfg := Default()
	if cfg.Intake.Enabled {
		t.Fatal("intake must be off by default")
	}
	if cfg.Intake.Listen != "127.0.0.1:7788" {
		t.Fatalf("default listen = %q, want loopback", cfg.Intake.Listen)
	}
	if err := cfg.Intake.Validate(); err != nil {
		t.Fatalf("a disabled section must not need any field: %v", err)
	}
}

func TestIntakeValidateRefusesHalfConfigured(t *testing.T) {
	complete := IntakeConfig{
		Enabled: true,
		Listen:  "127.0.0.1:7788",
		GitHub: GitHubIntakeConfig{
			Secret: "s3cret",
			Repos:  []string{"acme/widgets"},
			Actors: []string{"alice"},
		},
	}
	if err := complete.Validate(); err != nil {
		t.Fatalf("complete config refused: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*IntakeConfig)
		want   string
	}{
		{"no secret", func(c *IntakeConfig) { c.GitHub.Secret = "" }, "secret"},
		{"no repo", func(c *IntakeConfig) { c.GitHub.Repos = nil }, "repos"},
		{"bad repo", func(c *IntakeConfig) { c.GitHub.Repos = []string{"widgets"} }, "owner/name"},
		{"no actor", func(c *IntakeConfig) { c.GitHub.Actors = nil }, "actors"},
		{"bad listen", func(c *IntakeConfig) { c.Listen = "7788" }, "host:port"},
		{"bad priority", func(c *IntakeConfig) { c.GitHub.Priority = "urgent" }, "priority"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := complete
			c.GitHub.Repos = append([]string(nil), complete.GitHub.Repos...)
			c.GitHub.Actors = append([]string(nil), complete.GitHub.Actors...)
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("half-configured intake must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestIntakeWantsMentionDefaultsTrue(t *testing.T) {
	var g GitHubIntakeConfig
	if !g.WantsMention() {
		t.Fatal("an unset require_mention must mean true")
	}
	off := false
	g.RequireMention = &off
	if g.WantsMention() {
		t.Fatal("an explicit false must be honoured")
	}
}
