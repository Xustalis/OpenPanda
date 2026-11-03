// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestSplitLineSuffix(t *testing.T) {
	cases := []struct {
		in   string
		path string
		line int
	}{
		{"src/main.go:42", "src/main.go", 42},
		{"README.md", "README.md", 0},
		{`C:\src\a.go`, `C:\src\a.go`, 0},
		{"https://example.com/x", "https://example.com/x", 0},
		{"a.go:0", "a.go:0", 0},
		{"a.go:abc", "a.go:abc", 0},
		{"a.go:", "a.go:", 0},
		{"", "", 0},
		{":12", ":12", 0},
	}
	for _, tc := range cases {
		path, line := splitLineSuffix(tc.in)
		if path != tc.path || line != tc.line {
			t.Errorf("splitLineSuffix(%q) = (%q, %d), want (%q, %d)", tc.in, path, line, tc.path, tc.line)
		}
	}
}

// capturePrint runs fn with os.Stdout redirected and returns what it wrote.
func capturePrint(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestPrintFileWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.go")
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	out := capturePrint(t, func() { code = printFileWindow(path, 50, 2) })
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{"line 48", "line 50", "line 52"} {
		if !strings.Contains(out, want) {
			t.Errorf("window is missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"line 47", "line 53", "line 1\n"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("window reaches past the context: %q found in\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "▶") {
		t.Errorf("the focus line is not marked:\n%s", out)
	}
}

func TestPrintFileWindowEdgeCases(t *testing.T) {
	dir := t.TempDir()

	bin := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(bin, []byte{0x00, 0x01, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := printFileWindow(bin, 1, 2); code == 0 {
		t.Error("a binary file was printed")
	}

	short := filepath.Join(dir, "short.txt")
	if err := os.WriteFile(short, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	capturePrint(t, func() { code = printFileWindow(short, 40, 2) })
	if code == 0 {
		t.Error("a line past the end reported success")
	}

	// Line 1 with context above it must start at the first line, not panic.
	capturePrint(t, func() { code = printFileWindow(short, 1, 5) })
	if code != 0 {
		t.Errorf("first-line window exit code = %d", code)
	}
}
