// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
//
// A "path:line" target prints the lines around that line instead of the whole
// file — the form an agent uses when it names a place in its report, and the one
// the console's file links carry. It is the terminal's counterpart of the web
// viewer: same window, same focus line, no editor required.
func runRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	raw := fs.Bool("raw", false, "output raw unrendered text")
	width := fs.Int("width", 0, "override terminal render width (columns)")
	contextLines := fs.Int("context", 12, "lines to show either side of a path:line target")
	fs.Parse(reorderFlags(fs, args))

	target := fs.Arg(0)
	var (
		data []byte
		err  error
		path string
	)

	if target == "" || target == "-" {
		// Read from standard input if piped or explicitly asked
		if target == "" && stdinIsTTY() {
			fail(2, "usage: panda read [--raw] [--width N] [--context N] <file[:line] | ->")
		}
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			fatal("read stdin", err)
		}
		path = target
	} else {
		var line int
		path, line = splitLineSuffix(target)
		if line > 0 {
			// A line target is a window, not a document: markdown rendering a
			// 25-line excerpt of a 2000-line file helps nobody.
			if code := printFileWindow(path, line, *contextLines); code != 0 {
				os.Exit(code)
			}
			return
		}
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

// splitLineSuffix splits a "path:line" target into its path and focus line.
// A trailing ":<digits>" is a line number; a Windows drive letter is not (its
// colon sits at index 1 with a non-numeric tail), and neither is anything else
// with a colon in it — a URL or a time is left whole.
func splitLineSuffix(target string) (string, int) {
	i := strings.LastIndexByte(target, ':')
	if i <= 0 || i == len(target)-1 {
		return target, 0
	}
	if i == 1 {
		if c := target[0]; (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return target, 0
		}
	}
	digits := target[i+1:]
	for j := 0; j < len(digits); j++ {
		if digits[j] < '0' || digits[j] > '9' {
			return target, 0
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return target, 0
	}
	return target[:i], n
}

// printFileWindow prints the lines around `line` with the target marked, and
// returns a process exit code. The window is bounded on both ends by the file
// and by the line itself, so a target past the end prints what exists instead
// of an empty screen.
func printFileWindow(path string, line, contextLines int) int {
	if contextLines < 0 {
		contextLines = 0
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read file: %v\n", err)
		return 1
	}
	defer f.Close()

	head := make([]byte, 8000)
	n, _ := f.Read(head)
	if strings.IndexByte(string(head[:n]), 0) >= 0 {
		fmt.Fprintf(os.Stderr, "%s: binary file — use your own tools\n", path)
		return 1
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		fmt.Fprintf(os.Stderr, "read file: %v\n", err)
		return 1
	}

	start := max(1, line-contextLines)
	end := line + contextLines
	color := stdoutIsTTY()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	num, printed := 0, 0
	for sc.Scan() {
		num++
		if num < start {
			continue
		}
		if num > end {
			break
		}
		text := sc.Text()
		marker := "  "
		if num == line {
			marker = "▶ "
			if color {
				text = "\x1b[1m" + text + "\x1b[0m"
			}
		}
		fmt.Printf("%s%5d │ %s\n", marker, num, text)
		printed++
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "read file: %v\n", err)
		return 1
	}
	if num < line {
		fmt.Fprintf(os.Stderr, "%s: no line %d (the file has %d)\n", path, line, num)
		return 1
	}
	if printed == 0 {
		fmt.Fprintf(os.Stderr, "%s: no content around line %d\n", path, line)
		return 1
	}
	return 0
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
