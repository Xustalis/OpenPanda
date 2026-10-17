// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import (
	"slices"
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

func TestPromptMessages(t *testing.T) {
	promptKeys := []string{
		"prompt.device.summary.none",
		"prompt.device.hardware.cpu",
		"prompt.device.hardware.ram",
		"prompt.device.hardware.gpu",
		"prompt.skills.available",
		"prompt.task.intent_header",
		"prompt.summarize.system",
		"prompt.summarize.title",
		"prompt.subagent.header",
		"prompt.task.target",
	}

	for _, loc := range []Locale{English, ChineseSimp} {
		for _, k := range promptKeys {
			got := T(loc, k)
			if got == "" || got == k {
				t.Errorf("locale %s missing prompt message for key %s", loc, k)
			}
		}
	}

	// Verify interpolation
	cpuZh := Tf(ChineseSimp, "prompt.device.hardware.cpu", "n", "8")
	if cpuZh != "cpu 8 核" {
		t.Errorf("cpuZh = %q, want 'cpu 8 核'", cpuZh)
	}
	cpuEn := Tf(English, "prompt.device.hardware.cpu", "n", "8")
	if cpuEn != "cpu 8 cores" {
		t.Errorf("cpuEn = %q, want 'cpu 8 cores'", cpuEn)
	}
}

// TestKeyParity guards against translations drifting out of sync: every key
// defined for English must exist in every other locale. Missing keys fall back
// to English at runtime, which silently breaks the "fully localized" promise —
// this test makes the drift loud.
func TestKeyParity(t *testing.T) {
	en := messages[English]
	for _, loc := range Locales {
		if loc == English {
			continue
		}
		var missing []string
		for k := range en {
			if _, ok := messages[loc][k]; !ok {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("locale %s is missing %d keys, e.g. %v", loc, len(missing), missing[:min(10, len(missing))])
		}
	}
}

// TestKeyParityReverse covers the direction TestKeyParity cannot: keys that
// exist ONLY in a translation. An extra key is almost always a renamed-or-
// misspelled English key — the translator's string sits unread because the
// CLI asks for the English spelling and falls back to it. Catching the drift
// here is cheaper than auditing why a screen is half-translated.
func TestKeyParityReverse(t *testing.T) {
	en := messages[English]
	for _, loc := range Locales {
		if loc == English {
			continue
		}
		var extra []string
		for k := range messages[loc] {
			if _, ok := en[k]; !ok {
				extra = append(extra, k)
			}
		}
		if len(extra) > 0 {
			t.Errorf("locale %s has %d keys missing from English, e.g. %v", loc, len(extra), extra[:min(10, len(extra))])
		}
	}
}

// TestNoEmptyTranslations flags entries that exist but carry no text: T()
// treats an empty value as a hit, so the UI renders a blank line instead of
// falling back to English.
func TestNoEmptyTranslations(t *testing.T) {
	for _, loc := range Locales {
		for k, v := range messages[loc] {
			if strings.TrimSpace(v) == "" {
				t.Errorf("locale %s key %s has an empty translation", loc, k)
			}
		}
	}
}

// TestPlaceholderParity requires every {name} placeholder in the English
// source to appear in each translation — Tf interpolates by name, so a
// translation that drops or renames a placeholder renders the literal
// "{err}" (or silently drops the data) instead of the runtime value.
func TestPlaceholderParity(t *testing.T) {
	en := messages[English]
	for _, loc := range Locales {
		if loc == English {
			continue
		}
		for k, enText := range en {
			tr, ok := messages[loc][k]
			if !ok {
				continue // missing keys are TestKeyParity's beat
			}
			want, got := placeholders(enText), placeholders(tr)
			if want != got {
				t.Errorf("locale %s key %s placeholders = {%s}, want {%s}", loc, k, got, want)
			}
		}
	}
}

func placeholders(s string) string {
	set := map[string]bool{}
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			break
		}
		s = s[i+1:]
		j := strings.IndexByte(s, '}')
		if j < 0 {
			break
		}
		name := s[:j]
		// Only bare identifiers count: prompt templates carry literal JSON
		// examples like {"to":"<stage id>"} whose braces a translator is
		// EXPECTED to localize — treating those spans as placeholders flags
		// correct work. A Tf placeholder is always {word}, nothing else.
		if isPlaceholderName(name) {
			set[name] = true
		}
		s = s[j+1:]
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

func isPlaceholderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || '0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z') {
			return false
		}
	}
	return true
}
