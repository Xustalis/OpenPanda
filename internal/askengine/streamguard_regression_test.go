// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"errors"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestRegressionSuppressedProtocolCannotBecomeFinalAnswer(t *testing.T) {
	for _, raw := range []string{`{"padding":"` + strings.Repeat("x", directiveStreamMaxBytes) + `","kind":"task"`, "```json\n{\n"} {
		var out strings.Builder
		_, err := guardedStreamClassification(func(s string) { out.WriteString(s) }, func(write func(string)) (entry.Output, error) {
			write(raw)
			return entry.Output{Kind: entry.KindAnswer, Answer: raw}, nil
		})
		if !errors.Is(err, entry.ErrDirectiveProse) || out.Len() != 0 {
			t.Fatalf("unrecognized protocol must enter repair: stream=%q err=%v", out.String(), err)
		}
	}
	_, err := guardedStreamClassification(nil, func(write func(string)) (entry.Output, error) {
		write(`{"kind":"task"}`)
		return entry.Output{Kind: entry.KindTask}, nil
	})
	if err != nil {
		t.Fatalf("recognized dispatch must proceed: %v", err)
	}
}

func TestRegressionDirectivePrefixesNeverLeak(t *testing.T) {
	for _, body := range []string{
		`{"kind":"task","task":{"title":"x"}}`,
		"{\n  \"kind\": \"plan\",\n \"plan\":{}}",
		" \t\r\n{\r\n\"kind\":\"tool_call\",\"tool\":{}}",
		"```json\r\n{\"kind\":\"task\",\"task\":{}}\r\n```",
		"```\n{\"kind\":\"clarify\",\"clarify\":{}}\n```",
		`{"note":"` + strings.Repeat("x", 1000) + `","ki\u006ed":"ta\u0073k","task":{}}`,
		`{"note":"escaped \"kind\": \"answer\"","kind":"task","task":{}}`,
		"{\n", "```json\n", `{"kind":"ta`,
		`{"padding":"` + strings.Repeat("x", directiveStreamMaxBytes) + `","kind":"task"}`,
		strings.Repeat(" ", directiveStreamMaxBytes) + `{"kind":"task"}`,
	} {
		for _, step := range []int{1, 7, len(body)} {
			var out strings.Builder
			w, flush := guardedDelta(func(s string) { out.WriteString(s) })
			for i := 0; i < len(body); i += step {
				w(body[i:min(i+step, len(body))])
			}
			flush()
			flush()
			if out.Len() != 0 {
				t.Fatalf("step=%d prefix=%q leaked %q", step, body[:min(60, len(body))], out.String())
			}
		}
	}
}

func TestRegressionOrdinaryStreamsPreserveText(t *testing.T) {
	for _, body := range []string{
		"完成。", strings.Repeat("normal answer ", 2000),
		"```go\nfunc main() {}\n```", "```json\n{\"example\":42}\n```",
		`{"example":"ordinary JSON","quoted":"\"kind\":\"task\""}`,
		"Example protocol JSON:\n```json\n{\"kind\":\"task\",\"task\":{}}\n```",
	} {
		var out strings.Builder
		w, flush := guardedDelta(func(s string) { out.WriteString(s) })
		for _, r := range body {
			w(string(r))
		}
		flush()
		flush()
		if out.String() != body {
			t.Fatalf("changed ordinary stream: %q", out.String())
		}
	}
}

func TestRegressionDirectiveBufferIsBounded(t *testing.T) {
	for _, prefix := range []string{" ", `{"padding":"`} {
		g := newDirectiveStreamGuard(func(s string) { t.Fatalf("unresolved protocol leaked: %q", s) })
		g.Write(prefix)
		for range directiveStreamMaxBytes * 2 {
			g.Write(" ")
			if g.held.Len() >= directiveStreamMaxBytes {
				t.Fatalf("unbounded prefix buffer: %d", g.held.Len())
			}
		}
		if !g.suppressed {
			t.Fatal("oversized unresolved prefix must be suppressed")
		}
		g.Flush()
	}
}
