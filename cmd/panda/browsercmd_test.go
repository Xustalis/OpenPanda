// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/browser"
)

// The MCP text shape is what an agent actually reads: every field an
// operation returns has to survive into it, or the model is told a page is
// broken without being told why.
func TestFormatBrowserResult(t *testing.T) {
	out := formatBrowserResult(browser.Result{
		URL:        "http://127.0.0.1:5173/",
		Title:      "Probe",
		Text:       "hello",
		Controls:   []browser.Control{{Selector: "#go", Role: "button", Name: "Go"}},
		Screenshot: "/tmp/shot.png",
		Width:      1280,
		Height:     800,
		Console:    []browser.ConsoleEntry{{Type: "error", Text: "boom", URL: "a.js", Line: 3}},
		Network: []browser.NetworkEntry{
			{Method: "GET", URL: "http://127.0.0.1:5173/", Status: 200, DurationMS: 5},
			{Method: "GET", URL: "http://127.0.0.1:5173/missing.js", Failed: "net::ERR_ABORTED"},
		},
		Value: json.RawMessage(`"ok"`),
	})
	for _, want := range []string{
		"url: http://127.0.0.1:5173/", "title: Probe", "hello",
		`button "Go" [#go]`, "screenshot: /tmp/shot.png (1280x800)",
		"boom", "(a.js:3)", "GET http://127.0.0.1:5173/ -> 200 (5ms)",
		"FAILED net::ERR_ABORTED", `value: "ok"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatBrowserResultTruncates(t *testing.T) {
	out := formatBrowserResult(browser.Result{Text: strings.Repeat("x", maxSelfToolOut*2)})
	if len(out) > maxSelfToolOut+64 || !strings.Contains(out, "truncated") {
		t.Fatalf("len = %d", len(out))
	}
}

func TestBrowserToolsAreAdvertised(t *testing.T) {
	tools := browserTools(&selfToolsDeps{})
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
		if tool.Description == "" || len(tool.InputSchema) == 0 {
			t.Errorf("%s has no description or schema", tool.Name)
		}
	}
	for _, want := range []string{
		"panda_browser_open", "panda_browser_snapshot", "panda_browser_screenshot",
		"panda_browser_click", "panda_browser_type", "panda_browser_press",
		"panda_browser_scroll", "panda_browser_eval", "panda_browser_console",
		"panda_browser_network", "panda_browser_viewport", "panda_browser_close",
	} {
		if !names[want] {
			t.Errorf("tool %q is not advertised", want)
		}
	}
}
