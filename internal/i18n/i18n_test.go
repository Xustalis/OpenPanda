package i18n

import (
	"strings"
	"testing"
)

func TestTFallsBackToEnglish(t *testing.T) {
	// Every locale must resolve the keys the CLI uses; a key missing from a
	// translation falls back to English rather than surfacing the raw key.
	for _, loc := range Locales {
		for _, key := range []string{"repl.welcome", "cmd.ask", "cmd.quit", "repl.unknown", "repl.web.noToken"} {
			if got := T(loc, key); got == key {
				t.Fatalf("T(%s, %s) returned the raw key", loc, key)
			} else if got != T(English, key) && loc != English {
				// fine: translated; only assert fallback when missing
				continue
			}
		}
	}
}

func TestTUnknownKeyReturnsKey(t *testing.T) {
	if got := T(English, "no.such.key"); got != "no.such.key" {
		t.Fatalf("unknown key = %q, want it echoed", got)
	}
}

func TestTfInterpolates(t *testing.T) {
	got := Tf(English, "repl.unknown", "cmd", "/wat")
	if !strings.Contains(got, "/wat") {
		t.Fatalf("Tf lost the placeholder value: %q", got)
	}
	if strings.Contains(got, "{cmd}") {
		t.Fatalf("Tf left the placeholder unreplaced: %q", got)
	}
}

func TestDetectEnvOverride(t *testing.T) {
	t.Setenv("OPENPANDA_LANG", "zh-CN")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	if got := Detect(); got != ChineseSimp {
		t.Fatalf("Detect() = %q, want zh-CN (OPENPANDA_LANG wins)", got)
	}
}

func TestDetectLangBase(t *testing.T) {
	t.Setenv("OPENPANDA_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "ja_JP.UTF-8")
	if got := Detect(); got != Japanese {
		t.Fatalf("Detect() = %q, want ja", got)
	}
}

func TestDetectUnknownFallsBackToEnglish(t *testing.T) {
	t.Setenv("OPENPANDA_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "fr_FR.UTF-8")
	if got := Detect(); got != English {
		t.Fatalf("Detect() = %q, want en", got)
	}
}

// TestParse covers the config-side counterpart of Detect: mapping a persisted
// ui.locale value to a supported Locale, tolerating the same shapes Detect
// accepts (case, region suffixes, encoding suffixes) and returning "" for
// anything unsupported so callers can treat it as "no preference recorded".
func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Locale
	}{
		{"en", English},
		{"zh-CN", ChineseSimp},
		{"ZH-cn", ChineseSimp},
		{"zh_CN.UTF-8", ChineseSimp},
		{"zh_Hans", ChineseSimp},
		{"ja", Japanese},
		{"es", Spanish},
		{"de", German},
		{"", ""},
		{"klingon", ""},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := Parse(c.in); got != c.want {
			t.Errorf("Parse(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAllLocalesHaveModelKeys(t *testing.T) {
	requiredKeys := []string{
		"repl.model.current",
		"repl.model.none",
		"repl.model.set",
		"repl.model.usage",
		"repl.model.head",
		"repl.model.active",
		"repl.model.hint",
		"repl.model.providers.head",
		"repl.model.add.usage",
		"repl.model.add.badprovider",
		"repl.model.add.nokey",
		"repl.model.add.done",
		"repl.model.remove.usage",
		"repl.model.remove.none",
		"repl.model.remove.done",
		"repl.model.remove.active",
		"repl.model.switch.none",
		"repl.model.fetch.head",
		"repl.model.fetch.empty",
		"repl.model.fetch.err",
		"repl.model.test.ok",
		"repl.model.test.fail",
	}

	for _, loc := range Locales {
		dict, ok := messages[loc]
		if !ok {
			t.Fatalf("missing dictionary for locale %s", loc)
		}
		for _, k := range requiredKeys {
			val, exists := dict[k]
			if !exists || val == "" {
				t.Errorf("locale %s is missing translation key %s", loc, k)
			}
		}
	}
}
