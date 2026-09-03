// Package i18n provides the CLI's user-facing strings in the five project
// languages (the same set the READMEs and web console ship). English is the
// fallback: a key missing from another language falls back to English rather
// than erroring, so translations can land incrementally.
package i18n

import (
	"os"
	"strings"
)

// Locale is a supported UI language.
type Locale string

const (
	English     Locale = "en"
	ChineseSimp Locale = "zh-CN"
	Japanese    Locale = "ja"
	Spanish     Locale = "es"
	German      Locale = "de"
)

// Locales lists the supported languages in display order.
var Locales = []Locale{English, ChineseSimp, Japanese, Spanish, German}

// LocaleNames maps each locale to its endonym (for /lang display).
var LocaleNames = map[Locale]string{
	English:     "English",
	ChineseSimp: "简体中文",
	Japanese:    "日本語",
	Spanish:     "Español",
	German:      "Deutsch",
}

// Detect picks the startup locale: OPENPANDA_LANG wins, then the LANG-style
// environment, then English.
func Detect() Locale {
	for _, env := range []string{"OPENPANDA_LANG", "LC_ALL", "LANG"} {
		v := strings.TrimSpace(os.Getenv(env))
		if v == "" {
			continue
		}
		tag := strings.SplitN(v, ".", 2)[0]
		if loc := matchLocale(tag); loc != "" {
			return loc
		}
		// "en_US.UTF-8" base-language match, "zh_Hans" → zh-CN
		base := strings.SplitN(tag, "_", 2)[0]
		if loc := matchLocale(base); loc != "" {
			return loc
		}
	}
	return English
}

func matchLocale(tag string) Locale {
	l := Locale(tag)
	for _, loc := range Locales {
		if l == loc {
			return loc
		}
	}
	if strings.HasPrefix(tag, "zh") {
		return ChineseSimp
	}
	return ""
}

// Parse maps a persisted locale setting (config ui.locale) to a supported
// Locale, case-insensitively and tolerant of the same shapes Detect accepts
// ("zh_Hans", "zh_CN.UTF-8"). It returns "" when the value is unset or not a
// supported language, so callers can treat it as "no preference recorded".
func Parse(s string) Locale {
	s = strings.ToLower(strings.TrimSpace(strings.SplitN(s, ".", 2)[0]))
	if s == "" {
		return ""
	}
	if loc := matchLocale(s); loc != "" {
		return loc
	}
	base := strings.SplitN(s, "_", 2)[0]
	if loc := matchLocale(base); loc != "" {
		return loc
	}
	return ""
}

// T translates key into the given locale, falling back to English and then
// to the key itself (missing keys stay greppable).
func T(loc Locale, key string) string {
	if loc != English {
		if m := messages[loc]; m != nil {
			if s, ok := m[key]; ok {
				return s
			}
		}
	}
	if s, ok := messages[English][key]; ok {
		return s
	}
	return key
}

// Tf translates and interpolates {name} placeholders from alternating
// key/value pairs: Tf(loc, "repl.unknown", "cmd", name).
func Tf(loc Locale, key string, pairs ...string) string {
	s := T(loc, key)
	for i := 0; i+1 < len(pairs); i += 2 {
		s = strings.ReplaceAll(s, "{"+pairs[i]+"}", pairs[i+1])
	}
	return s
}
