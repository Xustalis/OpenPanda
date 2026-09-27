//go:build !lite

package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// execOutputMsg is one progressive stdout/stderr chunk from a slash or shell
// command. exec and generation bind it to the exact execution that produced it.
type execOutputMsg struct {
	exec       *commandExec
	generation uint64
	text       string
}

// execDoneMsg is the terminal event for one slash or shell execution.
type execDoneMsg struct {
	exec       *commandExec
	generation uint64
	text       string
	output     string
	err        error
}

// commandExec owns one command's cancellation and event stream. Unlike the old
// capture path, it never swaps process-global stdout/stderr.
type commandExec struct {
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	events     chan tea.Msg

	mu     sync.Mutex
	output strings.Builder
}

func newCommandExec(generation uint64) *commandExec {
	ctx, cancel := context.WithCancel(context.Background())
	return &commandExec{
		generation: generation,
		ctx:        ctx,
		cancel:     cancel,
		events:     make(chan tea.Msg, 256),
	}
}

func (e *commandExec) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	chunk := string(append([]byte(nil), p...))
	e.mu.Lock()
	_, _ = e.output.WriteString(chunk)
	e.mu.Unlock()
	select {
	case e.events <- execOutputMsg{exec: e, generation: e.generation, text: chunk}:
		return len(p), nil
	case <-e.ctx.Done():
		return 0, e.ctx.Err()
	}
}

func (e *commandExec) text() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.output.String()
}

func startCommandExec(r *repl, text string, generation uint64) (*commandExec, tea.Cmd) {
	e := newCommandExec(generation)
	go func() {
		var err error
		if r != nil {
			func() {
				// A panicking handler must neither kill the process nor strand
				// the pump: surface it as the command's error result so the
				// Update loop lands back in modeIdle with an error block.
				defer func() {
					if p := recover(); p != nil {
						err = fmt.Errorf("%v", p)
					}
				}()
				// The TUI owns the terminal's input stream, so commands get an
				// empty stdin: a shell escape that would read keys hits EOF
				// instantly instead of eating keystrokes out from under the
				// textarea.
				r.dispatchWithIO(e.ctx, text, strings.NewReader(""), e, e)
			}()
			if err == nil {
				err = e.ctx.Err()
			}
		} else {
			err = context.Canceled
		}
		e.events <- execDoneMsg{
			exec:       e,
			generation: generation,
			text:       text,
			output:     e.text(),
			err:        err,
		}
	}()
	return e, waitForExec(e)
}

func waitForExec(e *commandExec) tea.Cmd {
	return func() tea.Msg {
		if e == nil {
			return execDoneMsg{err: context.Canceled}
		}
		return <-e.events
	}
}

// isBareCommand reports whether a submitted line should run through the repl
// dispatch rather than the ask engine.
func isBareCommand(text string) bool {
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!")
}

// screenMarker is the cursor-home escape a repainting writer (watchQueueTo's
// "/tasks watch" board) emits between frames.
const screenMarker = "\x1b[H"

// latestFrame returns the text after the stream's last repaint marker — for a
// watch board that is the current frame, which is all the live region should
// draw. Output with no marker passes through untouched.
func latestFrame(out string) string {
	if i := strings.LastIndex(out, screenMarker); i >= 0 {
		return out[i+len(screenMarker):]
	}
	return out
}

// commitFrame folds a finished repaint stream into the one frame the
// transcript keeps: without it, "/tasks watch" commits every 2s snapshot as a
// single concatenated block. The exit sequence repaints once more only to
// wipe ("\x1b[0m\x1b[H\x1b[J" + the exited line), so a tail shaped like
// cleanup folds the previous real frame back in — the board's end state plus
// the "exited" line. Anything that never repainted twice is ordinary output
// and passes through whole.
func commitFrame(out string) string {
	segs := strings.Split(out, screenMarker)
	if len(segs) < 3 {
		return out
	}
	last := segs[len(segs)-1]
	if strings.HasPrefix(last, "\x1b[J") || strings.HasPrefix(last, "\x1b[2J") {
		return segs[len(segs)-2] + last
	}
	return last
}

var _ io.Writer = (*commandExec)(nil)
