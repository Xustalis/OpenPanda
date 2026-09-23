//go:build !lite

package main

import (
	"context"
	"io"
	"os"
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
			r.dispatchWithIO(e.ctx, text, os.Stdin, e, e)
			err = e.ctx.Err()
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

var _ io.Writer = (*commandExec)(nil)
