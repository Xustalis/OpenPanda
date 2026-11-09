// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package localmodels

// The CLI surface of the OpenCode collector, exercised through a fake binary.
// The fixtures are the shapes the real builds print: v2 answers
// `api model.list` with one JSON envelope and rejects `models --verbose` with
// "Unrecognized flag" (verified on 2.0.20), while 1.x answers only the dump.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeOpenCode writes an executable that answers the two catalogue commands
// from the given bodies and fails everything else the way a real build does.
func fakeOpenCode(t *testing.T, apiBody, verboseBody string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode")

	write := func(name, body string) string {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return file
	}
	apiFile := write("api.json", apiBody)
	verboseFile := write("verbose.txt", verboseBody)

	script := `#!/bin/sh
if [ "$1" = "api" ] && [ "$2" = "model.list" ]; then
  if [ -s ` + apiFile + ` ]; then cat ` + apiFile + `; exit 0; fi
  echo "error: unknown command api" >&2
  exit 1
fi
if [ "$1" = "models" ] && [ "$2" = "--verbose" ]; then
  if [ -s ` + verboseFile + ` ]; then cat ` + verboseFile + `; exit 0; fi
  echo "ERROR" >&2
  echo "  Unrecognized flag: --verbose in command opencode models" >&2
  exit 1
fi
echo "unknown command" >&2
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func collectFake(t *testing.T, cli string) Source {
	t.Helper()
	// Discover applies the defaults; calling the collector directly means
	// supplying the timeout it would have set, or the context expires at once.
	return collectOpenCode(context.Background(), Options{
		OpenCodeCLI: cli,
		Home:        t.TempDir(),
		Getenv:      func(string) string { return "" },
		NoProbe:     true,
		CLITimeout:  10 * time.Second,
	})
}

// v2: the envelope is the catalogue, the removed flag never runs, and a
// zero-cost model on OpenCode's own gateway is importable with no key.
func TestCollectOpenCodeReadsTheV2Catalogue(t *testing.T) {
	cli := fakeOpenCode(t, `{"location":{"directory":"/tmp"},"data":[
	  {"id":"iceman-free","modelID":"iceman-free","providerID":"opencode","name":"Iceman Free",
	   "settings":{"baseURL":"https://opencode.ai/inference/openai/v1","provider":"opencode"},
	   "capabilities":{"tools":true,"input":["text"],"output":["text"]},
	   "variants":[],"cost":[{"input":0,"output":0,"cache":{"read":0,"write":0}}],
	   "status":"active","enabled":true,"limit":{"context":262144,"output":131072}},
	  {"id":"gpt-6.1-sol","modelID":"gpt-6.1-sol","providerID":"opencode","name":"GPT 6.1 Sol",
	   "settings":{"baseURL":"https://opencode.ai/inference/openai/v1","provider":"opencode"},
	   "cost":[{"input":2,"output":10}],
	   "status":"active","enabled":true,"limit":{"context":1050000,"output":128000}}
	]}`, "")

	src := collectFake(t, cli)
	if src.Status != StatusOK {
		t.Fatalf("status = %q (%s)", src.Status, src.Detail)
	}
	if len(src.Candidates) != 2 {
		t.Fatalf("candidates = %+v", src.Candidates)
	}
	free, paid := src.Candidates[0], src.Candidates[1]

	if free.Model != "iceman-free" || free.BaseURL != "https://opencode.ai/inference/openai/v1" {
		t.Errorf("free row = %+v", free)
	}
	if free.Provider != "opencode" {
		t.Errorf("provider = %q, want the catalogue namespace", free.Provider)
	}
	if free.ContextWindow != 262144 {
		t.Errorf("context = %d", free.ContextWindow)
	}
	if !free.Importable || !free.NoAuth {
		t.Errorf("free tier should be importable without a key: %+v", free)
	}
	if paid.Importable {
		t.Errorf("a priced row needs a credential, got %+v", paid)
	}
	if !strings.Contains(paid.Note, "OPENCODE_API_KEY") {
		t.Errorf("paid note should name the variable: %q", paid.Note)
	}
}

// 1.x: the api surface does not exist, so the dump must still answer. This is
// the regression the fallback exists for.
func TestCollectOpenCodeFallsBackToTheVerboseDump(t *testing.T) {
	cli := fakeOpenCode(t, "", `opencode/deepseek-v4.1-flash-free
{
  "id": "deepseek-v4.1-flash-free",
  "providerID": "opencode",
  "name": "DeepSeek V4.1 Flash Free",
  "api": {
    "url": "https://opencode.ai/zen/v1"
  },
  "limit": {
    "context": 200000
  }
}
`)

	src := collectFake(t, cli)
	if src.Status != StatusOK {
		t.Fatalf("status = %q (%s)", src.Status, src.Detail)
	}
	if len(src.Candidates) != 1 {
		t.Fatalf("candidates = %+v", src.Candidates)
	}
	got := src.Candidates[0]
	if got.Model != "deepseek-v4.1-flash-free" || got.BaseURL != "https://opencode.ai/zen/v1" {
		t.Errorf("row = %+v", got)
	}
	if got.ContextWindow != 200000 {
		t.Errorf("context = %d", got.ContextWindow)
	}
}

// A v2 install whose service answers an empty envelope is "empty", not broken:
// the console words those two differently and only one of them is the user's
// problem.
func TestCollectOpenCodeReportsAnEmptyCatalogueAsEmpty(t *testing.T) {
	cli := fakeOpenCode(t, `{"location":{"directory":"/tmp"},"data":[]}`, "")

	src := collectFake(t, cli)
	if src.Status != StatusEmpty {
		t.Fatalf("status = %q (%s)", src.Status, src.Detail)
	}
	if len(src.Candidates) != 0 {
		t.Errorf("candidates = %+v", src.Candidates)
	}
	if !strings.Contains(src.Detail, "api model.list") {
		t.Errorf("detail should name the surface that answered: %q", src.Detail)
	}
}

// Neither surface usable is the one case worth an error, and it has to name
// both attempts so the person can see which build they are on.
func TestCollectOpenCodeReportsBothSurfacesFailing(t *testing.T) {
	cli := fakeOpenCode(t, "", "")

	src := collectFake(t, cli)
	if src.Status != StatusError {
		t.Fatalf("status = %q (%s)", src.Status, src.Detail)
	}
	if !strings.Contains(src.Detail, "api model.list") ||
		!strings.Contains(src.Detail, "models --verbose") {
		t.Errorf("detail should name both surfaces: %q", src.Detail)
	}
}
