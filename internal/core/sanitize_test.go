package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilterSensitiveData(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text is untouched",
			input:    "This is a normal message",
			expected: "This is a normal message",
		},
		{
			name:     "api key assignment",
			input:    "api_key = abcdefghij1234567890klmnopqrstuvwxyz",
			expected: "[REDACTED]",
		},
		{
			name:     "github token",
			input:    "ghp_1234567890abcdefghijklmnopqrstuvwxyz",
			expected: "[REDACTED]",
		},
		{
			name:     "bare provider key with no key name to match on",
			input:    "use sk-ant-api03-abcdefghijklmnopqrstuvwxyz to authenticate",
			expected: "use [REDACTED] to authenticate",
		},
		{
			name:     "private key header only",
			input:    "-----BEGIN RSA PRIVATE KEY-----\nsomekey\n-----END RSA PRIVATE KEY-----",
			expected: "[REDACTED]\nsomekey\n-----END RSA PRIVATE KEY-----",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FilterSensitiveData(tc.input); got != tc.expected {
				t.Errorf("FilterSensitiveData(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestHasSensitiveData(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain text", "Hello world", false},
		{"api key", "api_key=abcdefghij1234567890klmnop", true},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nsometestdata", true},
		{"bare provider key", "sk-proj-abcdefghijklmnopqrstuvwxyz", true},
		// An address is not a credential. Flagging email here would drown the
		// signal in false positives, and if anything ever did rewrite the text
		// it would redact a colleague's address out of a note.
		{"email is not a credential", "test@example.com", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasSensitiveData(tc.input); got != tc.want {
				t.Errorf("HasSensitiveData(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestScanProjectMemoryCredentials(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"notes.md": []byte("just a note"),
		".env":     []byte("api_key=abcdefghij1234567890klmnop\n"),
		// A binary file must be skipped rather than reported: running credential
		// regexes over binary data is the false-positive failure mode the NUL
		// check exists to prevent.
		"blob.bin": {0x00, 0x01, 0x02, 0x03},
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	hits := scanProjectMemoryCredentials(dir)
	if len(hits) != 1 || hits[0] != ".env" {
		t.Fatalf("scanProjectMemoryCredentials() = %v, want [.env]", hits)
	}
}
