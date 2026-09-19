package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsMarkdownFile(t *testing.T) {
	cases := []struct {
		path string
		data []byte
		want bool
	}{
		{"README.md", nil, true},
		{"doc.markdown", nil, true},
		{"notes.mdown", nil, true},
		{"main.go", nil, false},
		{"config.yaml", nil, false},
		{"", []byte("# Heading\nSome text"), true},
		{"-", []byte("```go\ncode\n```"), true},
		{"-", []byte("just plain text"), false},
	}

	for _, tc := range cases {
		got := isMarkdownFile(tc.path, tc.data)
		if got != tc.want {
			t.Errorf("isMarkdownFile(%q, %q) = %v, want %v", tc.path, tc.data, got, tc.want)
		}
	}
}

func TestRunReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	content := "# Hello OpenPanda\n\nThis is **test** markdown."
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Should not panic or error with --raw
	runRead([]string{"--raw", path})
}

func TestRenderAndPrintMd(t *testing.T) {
	renderAndPrintMd("# Test\n\n```python\nprint(1)\n```", 80)
	renderAndPrintMd("# Test\n\n```python\nprint(1)\n```", 0)
}
