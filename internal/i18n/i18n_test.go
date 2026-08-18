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
