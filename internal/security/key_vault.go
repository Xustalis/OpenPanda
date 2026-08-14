package security

import "regexp"

// Redact scrubs the two secret shapes that can appear in adapter output: a
// credential-named KEY=value / key: value pair, and a Bearer token. It is a
// heuristic safety net, not a parser — the real guarantee is that secrets are
// injected via env and never persisted, so this only defends against a
// misbehaving adapter echoing them back into a result or log.
func Redact(s string) string {
	s = kvSecretRe.ReplaceAllString(s, `${1}[redacted]`)
	return bearerRe.ReplaceAllString(s, `${1}[redacted]`)
}

// kvSecretRe matches credential-named keys followed by "=" or ":" and a single
// value token, e.g. `ANTHROPIC_API_KEY=sk-...` or `token=xyz`. The key token is
// matched without a leading word boundary so embedded names like
// `ANTHROPIC_API_KEY` (where "API_KEY" follows an underscore) are caught.
var kvSecretRe = regexp.MustCompile(`(?i)((?:api[_-]?key|apikey|secret|token|password|passwd|credential)[a-z0-9_-]*\s*[=:]\s*)[^\s,;"']+`)

// bearerRe matches a Bearer token: `Bearer <token>` (also the value of an
// Authorization header, whose token is the sensitive part).
var bearerRe = regexp.MustCompile(`(?i)(\bbearer\s+)[^\s,;"']+`)
