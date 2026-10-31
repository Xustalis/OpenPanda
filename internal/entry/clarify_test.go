// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"strings"
	"testing"
)

func validClarify() *ClarifySpec {
	return &ClarifySpec{
		Plan: "先改 internal，再跑测试。",
		Questions: []ClarifyQuestion{
			{
				ID: "scope", Header: "范围", Question: "改哪个目录？",
				Options: []ClarifyOption{
					{Label: "internal", Description: "核心逻辑"},
					{Label: "cmd", Description: "命令行"},
				},
				Recommended: "internal",
			},
			{
				ID: "verify", Header: "验证", Question: "用哪个命令验证？",
				Options: []ClarifyOption{{Label: "make test"}, {Label: "make gate"}},
			},
		},
	}
}

func TestValidateClarifySpec(t *testing.T) {
	if err := ValidateClarifySpec(validClarify()); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*ClarifySpec)
	}{
		{"no questions", func(c *ClarifySpec) { c.Questions = nil }},
		{"too many questions", func(c *ClarifySpec) {
			q := c.Questions[0]
			c.Questions = []ClarifyQuestion{q, q, q, q, q}
		}},
		{"duplicate question id", func(c *ClarifySpec) { c.Questions[1].ID = c.Questions[0].ID }},
		{"missing header", func(c *ClarifySpec) { c.Questions[0].Header = "" }},
		{"header too long", func(c *ClarifySpec) { c.Questions[0].Header = strings.Repeat("x", 13) }},
		{"missing question", func(c *ClarifySpec) { c.Questions[0].Question = "  " }},
		{"one option", func(c *ClarifySpec) { c.Questions[0].Options = c.Questions[0].Options[:1] }},
		{"five options", func(c *ClarifySpec) {
			c.Questions[0].Options = append(c.Questions[0].Options, ClarifyOption{Label: "a"}, ClarifyOption{Label: "b"}, ClarifyOption{Label: "c"})
		}},
		{"duplicate label", func(c *ClarifySpec) { c.Questions[0].Options[1].Label = c.Questions[0].Options[0].Label }},
		{"empty label", func(c *ClarifySpec) { c.Questions[0].Options[0].Label = " " }},
		{"recommended names no option", func(c *ClarifySpec) { c.Questions[0].Recommended = "nope" }},
		{"plan too long", func(c *ClarifySpec) { c.Plan = strings.Repeat("x", maxClarifyPlanBytes+1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validClarify()
			tc.mut(c)
			if err := ValidateClarifySpec(c); err == nil {
				t.Fatalf("invalid spec accepted: %+v", c)
			}
		})
	}
}

func TestParseClarifyEnvelope(t *testing.T) {
	raw := `{"kind":"clarify","clarify":{"questions":[{"id":"q","header":"h","question":"Which?","options":[{"label":"a"},{"label":"b"}],"recommended":"a"}]}}`
	out, ok, err := decodeEnvelope(raw)
	if err != nil || !ok {
		t.Fatalf("decode = ok:%v err:%v", ok, err)
	}
	if out.Kind != KindClarify || out.Clarify == nil || len(out.Clarify.Questions) != 1 {
		t.Fatalf("out = %+v, want a clarify output", out)
	}
	// The directive must never leak to the user as streamed prose.
	if suppress, _ := judgeObject(raw); !suppress {
		t.Fatal("clarify JSON was not suppressed by the delta guard")
	}
	// And it must never be replayed from the classification cache.
	if classificationCacheable(out) {
		t.Fatal("clarify output marked cacheable")
	}
}

func TestPromptTeachesClarify(t *testing.T) {
	out := BuildPrompt(PromptOptions{})
	for _, want := range []string{
		"Clarify before acting (not after)",
		`{"kind":"clarify"`,
		"Ask only when the answer changes what you will do",
		"At most one clarify per request",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}
