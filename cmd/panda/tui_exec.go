package main

// Running the existing slash commands from the TUI. The classic REPL's ~30
// handlers print straight to stdout with fmt/pal(); rewriting every one to an
// injected writer would churn 150+ call sites for no behavioural gain. Instead
// we lean on Bubble Tea's Exec: it releases the terminal, runs our command in
// the foreground with stdout restored to the real tty, then repaints. The
// handler's output lands in scrollback exactly as it does in the classic loop —
// consistent with the TUI's inline model, where committed output already lives
// in the terminal's own history — and the dispatch table stays the single source
// of truth for what every command does.

import (
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// execDoneMsg reports that an Exec'd slash command finished. dispatch never
// returns an error, so err is reserved for a future handler that might.
type execDoneMsg struct{ err error }

// replExecCmd adapts a repl slash-command line to Bubble Tea's ExecCommand. Run
// executes with the terminal released, so the handlers' fmt.Println output goes
// to the real screen (and thus scrollback). The Set* methods are no-ops: the
// handlers write to the process stdout directly, not to an injected writer.
type replExecCmd struct {
	r    *repl
	line string
	echo string // the rendered "❯ /cmd" line, printed above the output
}

func (c replExecCmd) Run() error {
	if c.echo != "" {
		fmt.Println(c.echo)
	}
	c.r.dispatch(c.line)
	return nil
}

func (replExecCmd) SetStdin(io.Reader)  {}
func (replExecCmd) SetStdout(io.Writer) {}
func (replExecCmd) SetStderr(io.Writer) {}

// runSlash builds the command to run a slash line in the foreground and report
// completion. The echo mirrors the classic prompt so the transcript reads as a
// dialogue; it is printed inside Run (after the terminal is released) rather
// than via tea.Println, which sidesteps any ordering race between the managed
// renderer and the released-terminal write.
func (m tuiModel) runSlash(text string) tea.Cmd {
	echo := block{kind: blockUser, body: text}.render(m.th, m.width, m.expandThought)
	return tea.Exec(replExecCmd{r: m.r, line: text, echo: echo}, func(err error) tea.Msg {
		return execDoneMsg{err: err}
	})
}

// isBareCommand reports whether a submitted line should run through the repl
// dispatch (a slash command or a shell escape) rather than the ask engine. The
// quit shortcuts are handled before this — they end the program, not a handler.
func isBareCommand(text string) bool {
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!")
}
