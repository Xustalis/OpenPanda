package main

import (
	"io"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// execDoneMsg reports that an executed slash or shell command finished.
type execDoneMsg struct {
	text   string
	output string
}

var captureMu sync.Mutex

// captureOutput intercepts stdout and stderr produced by fn, returning the
// combined captured text. This allows the classic REPL's handlers (which print
// straight to stdout with fmt/pal) to execute cleanly inside the full-screen AltScreen TUI
// without releasing the terminal or discarding output.
func captureOutput(fn func()) string {
	captureMu.Lock()
	defer captureMu.Unlock()

	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	os.Stdout = w
	os.Stderr = w

	outC := make(chan string, 1)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r)
		outC <- buf.String()
	}()

	fn()

	_ = w.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	out := <-outC
	_ = r.Close()
	return out
}

// runSlash builds the command to execute a slash line or shell command in the background
// and deliver its captured output to the model loop without exiting AltScreen.
func (m tuiModel) runSlash(text string) tea.Cmd {
	return func() tea.Msg {
		out := captureOutput(func() {
			if m.r != nil {
				m.r.dispatch(text)
			}
		})
		return execDoneMsg{
			text:   text,
			output: out,
		}
	}
}

// isBareCommand reports whether a submitted line should run through the repl
// dispatch (a slash command or a shell escape) rather than the ask engine. The
// quit shortcuts are handled before this — they end the program, not a handler.
func isBareCommand(text string) bool {
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!")
}
