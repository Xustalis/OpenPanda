package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/mdtext"
)

// runRead implements `panda read [file]` (with aliases `view`, `cat`, `md`,
// `markdown`, and direct invocation via `panda <file.md>`).
//
// If stdout is a TTY and the content is Markdown (by extension or content),
// it automatically renders the content with syntax highlighting and formatting.
// When redirected to a pipe or file, or when --raw is set, it emits raw bytes
// preserving Unix composability.
func runRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	raw := fs.Bool("raw", false, "output raw unrendered text")
	width := fs.Int("width", 0, "override terminal render width (columns)")
	fs.Parse(reorderFlags(args, map[string]bool{"width": true}))

	target := fs.Arg(0)
	var (
		data []byte
		err  error
		path string
	)

	if target == "" || target == "-" {
		// Read from standard input if piped or explicitly asked
		if target == "" && stdinIsTTY() {
			fmt.Fprintln(os.Stderr, "usage: panda read [--raw] [--width N] <file | ->")
			os.Exit(2)
		}
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			fatal("read stdin", err)
		}
		path = target
	} else {
		path = target
		data, err = os.ReadFile(path)
		if err != nil {
			fatal("read file", err)
		}
	}

	if *raw || !stdoutIsTTY() {
		os.Stdout.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Println()
		}
		return
	}

	if isMarkdownFile(path, data) {
		renderAndPrintMd(string(data), *width)
		return
	}

	// Non-markdown file: print verbatim
	os.Stdout.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		fmt.Println()
	}
}

// renderAndPrintMd renders Markdown to stdout, respecting custom width if provided.
func renderAndPrintMd(content string, width int) {
	if width > 0 {
		rendered, err := mdtext.Render(content, width)
		if err != nil {
			rendered = mdtext.ANSI(content)
		}
		fmt.Println(rendered)
		return
	}
	fmt.Println(mdtext.RenderTerminal(content))
}

// isMarkdownFile returns whether path or data represents a Markdown document.
func isMarkdownFile(path string, data []byte) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".md" || ext == ".markdown" || ext == ".mdown" {
		return true
	}
	if path == "" || path == "-" {
		s := string(data)
		return strings.Contains(s, "# ") ||
			strings.Contains(s, "```") ||
			strings.Contains(s, "**") ||
			strings.Contains(s, "## ") ||
			strings.Contains(s, "|")
	}
	return false
}
