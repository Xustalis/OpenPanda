// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Redact scrubs credential assignments, bearer tokens, recognizable provider
// keys and PEM private keys from diagnostic text.
func Redact(s string) string {
	// Scrub the whole PEM before a named assignment can consume its header.
	s = bareCredential.ReplaceAllString(s, "[redacted]")
	s = kvSecretRe.ReplaceAllStringFunc(s, func(match string) string {
		if strings.EqualFold(match, "credential: no") {
			return match
		}
		prefix := kvSecretRe.FindStringSubmatchIndex(match)
		return match[:prefix[3]] + "[redacted]"
	})
	s = kvSecretQuotedRe.ReplaceAllString(s, `${1}[redacted]${2}`)
	return bearerRe.ReplaceAllString(s, `${1}[redacted]`)
}

var bareCredential = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{36,}|AKIA[A-Z0-9]{16})|(?s:-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*?(?:-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----|$))`)
var secretField = regexp.MustCompile(`(?i)(?:^|[_-])(?:api[_-]?key|secret|password|passwd|token|credential)$`)

// RedactJSON preserves number precision and only re-encodes records that contain secrets.
func RedactJSON(raw []byte) []byte {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return []byte(Redact(string(raw)))
	}
	if !scrubJSON(&v) {
		return raw
	}
	out, _ := json.Marshal(v)
	return out
}

func scrubJSON(v *any) bool {
	changed := false
	switch x := (*v).(type) {
	case string:
		clean := Redact(x)
		*v = clean
		return clean != x
	case []any:
		for i := range x {
			if scrubJSON(&x[i]) {
				changed = true
			}
		}
	case map[string]any:
		for k, item := range x {
			if _, ok := item.(string); ok && secretField.MatchString(k) {
				x[k] = "[redacted]"
				changed = true
				continue
			}
			if scrubJSON(&item) {
				x[k] = item
				changed = true
			}
		}
	}
	return changed
}

// kvSecretRe matches credential-named keys followed by "=" or ":" and a single
// unquoted value token, e.g. `ANTHROPIC_API_KEY=sk-...` or `token=xyz`. The key
// token is matched without a leading word boundary so embedded names like
// `ANTHROPIC_API_KEY` (where "API_KEY" follows an underscore) are caught.
var kvSecretRe = regexp.MustCompile(`(?i)((?:api[_-]?key|apikey|secret|token|password|passwd|credential)[a-z0-9_-]*\s*[=:]\s*)[^\s,;"']+`)

// kvSecretQuotedRe matches the JSON/YAML form where the value is quoted
// (`"api_key": "sk-..."`), which kvSecretRe misses because its value token
// excludes quotes. The surrounding quotes are captured and preserved so the
// scrubbed output stays well-formed.
var kvSecretQuotedRe = regexp.MustCompile(`(?i)((?:api[_-]?key|apikey|secret|token|password|passwd|credential)[a-z0-9_-]*["']?\s*[=:]\s*["'])[^"']*(["'])`)

// bearerRe matches a Bearer token: `Bearer <token>` (also the value of an
// Authorization header, whose token is the sensitive part).
var bearerRe = regexp.MustCompile(`(?i)(\bbearer\s+)[^\s,;"']+`)
