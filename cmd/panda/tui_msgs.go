package main

// The bridge from the ask engine's push callbacks to Bubble Tea's pull-based
// message loop. AskTurns runs on its own goroutine and calls OnDelta /
// OnReasoning / OnProgress from there; Bubble Tea, meanwhile, wants to *receive*
// messages one at a time from Update. askStream reconciles the two: the
// callbacks fan events onto a buffered channel, and a tea.Cmd (waitForActivity)
// blocks on that channel, returning one tea.Msg per Update tick. The engine
// goroutine never touches the model, so there is no shared-state race — the only
// contact point is the channel.

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// TUI event messages. Each is a tea.Msg the root model folds into its state.
type (
	// deltaMsg is a chunk of streamed answer text.
	deltaMsg string
	// reasoningMsg is a chunk of chain-of-thought (display-only, D14).
	reasoningMsg string
	// progressMsg is one lifecycle phase update (route/exec/judge/tool/task).
	progressMsg askengine.Progress
	// doneMsg is the terminal outcome of one ask: the result (or error).
	doneMsg struct {
		out *askengine.Result
		err error
	}
	// resumedMsg is the outcome of a ResumeApproved re-run after the user
	// approved a parked tier-2 task.
	resumedMsg struct {
		out *askengine.Result
	}
)

// askStream carries one in-flight ask: the channel the callbacks write to, the
// context that cancels it (Esc/interrupt), and the outcome slot. It is created
// per ask and discarded when doneMsg lands.
type askStream struct {
	events chan tea.Msg
	cancel context.CancelFunc
}

// startAsk launches AskTurns on a background goroutine and returns the stream
// plus the first command to pump it. The callbacks push onto a buffered channel;
// they select against ctx.Done so a cancelled ask (Esc) never blocks the engine
// goroutine on a send into a channel the model has stopped draining.
func startAsk(engine *askengine.Engine, history []entry.Turn, prompt, workDir string, authorize bool) (*askStream, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &askStream{events: make(chan tea.Msg, 256), cancel: cancel}

	send := func(m tea.Msg) {
		select {
		case s.events <- m:
		case <-ctx.Done():
		}
	}
	cb := askengine.StreamCallbacks{
		OnDelta:     func(text string) { send(deltaMsg(text)) },
		OnReasoning: func(text string) { send(reasoningMsg(text)) },
		OnProgress:  func(p askengine.Progress) { send(progressMsg(p)) },
		// OnApproval stays nil: a synchronous yes/no from a Bubble Tea card is not
		// answerable from this goroutine, so we take the NeedsApproval Result path
		// (the engine parks the task and returns it) and prompt on the model loop,
		// then ResumeApproved — the same split the classic REPL uses.
	}
	go func() {
		defer cancel()
		out, err := engine.AskTurns(ctx, history, prompt, workDir, authorize, cb)
		// The outcome must always arrive, even after a cancel (Esc) — the model
		// stays alive and keeps draining, so a blocking send on the buffered
		// channel is delivered once the last pending delta is consumed. Gating
		// this on ctx.Done would strand the model in the asking state forever.
		s.events <- doneMsg{out: out, err: err}
	}()
	return s, waitForActivity(s)
}

// waitForActivity is the pump: it blocks on the stream's channel and returns the
// next event as a tea.Msg. The model re-issues it after every non-terminal event
// so the loop stays fed; on doneMsg it stops re-issuing and the ask ends.
func waitForActivity(s *askStream) tea.Cmd {
	return func() tea.Msg {
		m, ok := <-s.events
		if !ok {
			return doneMsg{}
		}
		return m
	}
}

// resumeApproved re-runs a parked tier-2 task with consent, off the model loop.
func resumeApproved(engine *askengine.Engine, taskID, workDir string) tea.Cmd {
	return func() tea.Msg {
		return resumedMsg{out: engine.ResumeApproved(taskID, workDir)}
	}
}
