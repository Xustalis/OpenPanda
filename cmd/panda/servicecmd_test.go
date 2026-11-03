// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func testServiceSpec() webServiceSpec {
	return webServiceSpec{
		Exe:      "/Users/me/.local/share/openpanda/bin/panda",
		Args:     []string{"web", "-no-browser"},
		Home:     "/Users/me",
		Path:     "/Users/me/.local/bin:/usr/bin:/bin",
		LogPath:  "/Users/me/Library/Logs/OpenPanda/web.log",
		Label:    webServiceLabel,
		UnitName: webServiceUnitName,
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	out := string(renderLaunchdPlist(testServiceSpec()))
	for _, want := range []string{
		"<key>Label</key>",
		"com.openpanda.web",
		"/Users/me/.local/share/openpanda/bin/panda",
		"<string>web</string>",
		"<string>-no-browser</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"web.log",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q:\n%s", want, out)
		}
	}
}

func TestRenderLaunchdPlistEscapesPaths(t *testing.T) {
	spec := testServiceSpec()
	spec.Exe = `/tmp/a&b/panda`
	out := string(renderLaunchdPlist(spec))
	if strings.Contains(out, "/tmp/a&b/panda") {
		t.Fatalf("unescaped path in plist:\n%s", out)
	}
	if !strings.Contains(out, "/tmp/a&amp;b/panda") {
		t.Fatalf("escaped path missing:\n%s", out)
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	out := string(renderSystemdUnit(testServiceSpec()))
	for _, want := range []string{
		"Description=OpenPanda web console",
		"ExecStart=/Users/me/.local/share/openpanda/bin/panda web -no-browser",
		"WantedBy=default.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSystemdUnitQuotesSpaces(t *testing.T) {
	spec := testServiceSpec()
	spec.Exe = "/Applications/Open Panda/panda"
	out := string(renderSystemdUnit(spec))
	if !strings.Contains(out, `ExecStart="/Applications/Open Panda/panda" web -no-browser`) {
		t.Fatalf("path with a space was not quoted:\n%s", out)
	}
}

func TestRenderWindowsWrapper(t *testing.T) {
	out := string(renderWindowsWrapper(testServiceSpec()))
	if !strings.Contains(out, `"/Users/me/.local/share/openpanda/bin/panda" "web" "-no-browser"`) {
		t.Fatalf("wrapper argv wrong:\n%s", out)
	}
	if !strings.Contains(out, `>> "/Users/me/Library/Logs/OpenPanda/web.log" 2>&1`) {
		t.Fatalf("wrapper redirect wrong:\n%s", out)
	}
}

// A service restart re-runs `panda web`, so the token must survive on disk;
// an ephemeral one would strand every open tab at the gate.
func TestEnsurePanelTokenPersists(t *testing.T) {
	old := cliConfigPath
	cliConfigPath = filepath.Join(t.TempDir(), "config.yaml")
	defer func() { cliConfigPath = old }()

	first, err := ensurePanelToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("empty token")
	}
	second, err := ensurePanelToken()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("token changed between calls: %q != %q", first, second)
	}
	cfg, err := config.Load(cliConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.PanelToken != first {
		t.Fatalf("persisted token %q != %q", cfg.Network.PanelToken, first)
	}
}

func TestDedupePath(t *testing.T) {
	// The list separator is platform-owned: a literal ":" is one path on
	// Windows, so the test would assert the un-deduped string there.
	sep := string(os.PathListSeparator)
	got := dedupePath("/a" + sep + "/b" + sep + "/a" + sep + sep + "/c")
	if want := "/a" + sep + "/b" + sep + "/c"; got != want {
		t.Fatalf("dedupePath = %q, want %q", got, want)
	}
}
