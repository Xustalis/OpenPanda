package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// Credential scrubbing for text this node handles itself: log lines, event
// notes, and the plain-text files a project's memory is made of.
//
// It is deliberately NOT applied to a project pack. A pack is a compressed
// archive: a regex over compressed bytes matches at random, and "redacting" an
// archive in place corrupts the stream so the receiver can no longer unpack it.
// The pack path therefore only *scans* (see scanProjectMemoryCredentials); the
// data itself must be cleaned where it is still text, before it is packed.
var sensitivePatterns = []*regexp.Regexp{
	// api_key=..., access_token: ..., "bearer_token": "...", etc.
	regexp.MustCompile(`(?i)(?:api[_-]?key|apikey|secret[_-]?key|access[_-]?token|bearer[_-]?token)\s*[=:]\s*["']?[A-Za-z0-9_\-]{16,}["']?`),
	// AWS access key id / secret access key.
	regexp.MustCompile(`(?i)aws[_-]?(?:secret|access)[_-]?key\s*[=:]\s*["']?[A-Za-z0-9/+=]{40}["']?`),
	// GitHub tokens (ghp_/gho_/ghu_/ghs_/ghr_).
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{36,}`),
	// A quoted token/password value, the shape config files use.
	regexp.MustCompile(`(?i)\b(?:token|password|passwd)\s*[=:]\s*["'][^"'\s]{16,}["']`),
	// PEM private key headers.
	regexp.MustCompile(`-----BEGIN (?:RSA |DSA |EC |OPENSSH )?PRIVATE KEY-----`),
	// Bare provider keys (Anthropic/OpenAI style). They carry no key name to
	// match on, so nothing else in this list catches them.
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}\b`),
}

// maxScanBytes caps how much of one file a credential scan reads. A memory
// directory holds notes, not datasets.
const maxScanBytes = 1 << 20 // 1 MiB

// FilterSensitiveData replaces credential-shaped substrings in s with
// [REDACTED]. Only safe on text — see the note on sensitivePatterns.
func FilterSensitiveData(s string) string {
	for _, re := range sensitivePatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// HasSensitiveData reports whether s contains anything credential-shaped.
func HasSensitiveData(s string) bool {
	for _, re := range sensitivePatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// scanProjectMemoryCredentials lists the files under dir whose text looks like
// it carries a credential, as paths relative to dir.
//
// Best-effort and read-only: unreadable entries, empty files, oversized files
// and binaries are skipped rather than reported. It never rewrites anything —
// the point is to tell the operator what is about to leave the node, not to
// guess at a safe edit.
func scanProjectMemoryCredentials(dir string) []string {
	var hits []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() == 0 || info.Size() > maxScanBytes {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil || !isLikelyText(data) {
			return nil
		}
		if HasSensitiveData(string(data)) {
			if rel, rerr := filepath.Rel(dir, path); rerr == nil {
				hits = append(hits, rel)
			}
		}
		return nil
	})
	return hits
}

// isLikelyText reports whether data looks like text rather than binary. A NUL
// byte in the first block is the classic heuristic, and it is what keeps the
// scan from running credential regexes over, say, a PNG in the memory dir.
func isLikelyText(data []byte) bool {
	limit := len(data)
	if limit > 512 {
		limit = 512
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return false
		}
	}
	return true
}
