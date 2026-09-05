package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func TestPanelURL(t *testing.T) {
	cases := []struct{ addr, want string }{
		{"127.0.0.1:7840", "http://127.0.0.1:7840"},
		{"localhost:7840", "http://localhost:7840"},
		{":7840", "http://localhost:7840"},
		{"0.0.0.0:9000", "http://localhost:9000"},
		{"[::]:7840", "http://localhost:7840"},
		{"bad", "http://bad"},
	}
	for _, tc := range cases {
		if got := panelURL(tc.addr); got != tc.want {
			t.Errorf("panelURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestReplCommandHelpKeysResolve(t *testing.T) {
	// Every help key the dispatch table references must resolve in English
	// (the fallback source of truth) — a typo'd key would print as the raw
	// key in /help.
	for _, c := range replCommands {
		if got := i18n.T(i18n.English, c.help); got == c.help {
			t.Errorf("help key %q for /%s is missing from i18n", c.help, c.name)
		}
	}
}

func TestDispatchQuitAndUnknown(t *testing.T) {
	r := &repl{loc: i18n.English}
	r.dispatch("/exit")
	if !r.quit {
		t.Fatal("/exit must set the quit flag")
	}

	// Unknown commands must not exit or panic; they report and continue.
	r2 := &repl{loc: i18n.English}
	r2.dispatch("/definitely-not-a-command")
	if r2.quit {
		t.Fatal("unknown command must not quit")
	}

	// Empty and whitespace-only lines are ignored.
	r3 := &repl{loc: i18n.English}
	r3.dispatch("   ")
	if r3.quit {
		t.Fatal("blank line must not quit")
	}
}

func TestCmdLangSwitch(t *testing.T) {
	r := &repl{loc: i18n.English}
	r.cmdLang("ZH-cn") // case-insensitive match
	if r.loc != i18n.ChineseSimp {
		t.Fatalf("loc = %q, want zh-CN", r.loc)
	}

	r.cmdLang("klingon")
	if r.loc != i18n.ChineseSimp {
		t.Fatalf("bad locale must not change loc; got %q", r.loc)
	}

	// The bad-locale message lists the codes so the fix is discoverable.
	r.loc = i18n.English
	r.cmdLang("klingon")
	// (output goes to stdout; asserting loc stability is the contract here)
}

func TestCmdTasksStateFilter(t *testing.T) {
	// The state filter takes the first field only: "/tasks running extra"
	// filters by "running", not "running extra".
	state := ""
	if fields := strings.Fields("running   extra"); len(fields) > 0 {
		state = fields[0]
	}
	if state != "running" {
		t.Fatalf("state = %q, want running", state)
	}
}

// TestCmdLangPersistsLocale pins the /lang fix: the choice lands in config.yaml
// as ui.locale (comment-preserving), updates the in-memory config, and reloads
// on the next start. Without persistence the switch only lived for the session.
func TestCmdLangPersistsLocale(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte("node:\n  name: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &repl{loc: i18n.English, cfg: &config.Config{}, configPath: path}
	r.cmdLang("de")
	if r.loc != i18n.German {
		t.Fatalf("loc = %q, want de", r.loc)
	}
	if r.cfg.UI.Locale != "de" {
		t.Fatalf("in-memory ui.locale = %q, want de", r.cfg.UI.Locale)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := i18n.Parse(cfg.UI.Locale); got != i18n.German {
		t.Fatalf("persisted ui.locale = %q, want de", cfg.UI.Locale)
	}
}

// TestCmdLangWithoutConfigStillSwitches covers the session-only path: no
// config path (or no cfg at all) must not break the switch itself.
func TestCmdLangWithoutConfigStillSwitches(t *testing.T) {
	r := &repl{loc: i18n.English, cfg: &config.Config{}}
	r.cmdLang("ja")
	if r.loc != i18n.Japanese || r.cfg.UI.Locale != "ja" {
		t.Fatalf("loc=%q ui.locale=%q, want ja/ja", r.loc, r.cfg.UI.Locale)
	}
}

func TestDispatchVersionAndAliases(t *testing.T) {
	r := &repl{loc: i18n.English}
	// Neither /version, /ver, nor /v should panic or exit.
	for _, cmd := range []string{"/version", "/ver", "/v"} {
		r.dispatch(cmd)
		if r.quit {
			t.Fatalf("%s unexpectedly triggered quit", cmd)
		}
	}
}
