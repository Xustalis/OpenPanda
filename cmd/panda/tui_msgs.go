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
	// stream identifies which ask finished, so a turn the user already stopped
	// waiting on can be told apart from the one on screen.
	doneMsg struct {
		stream *askStream
		out    *askengine.Result
		err    error
	}
	// resumedMsg is the outcome of a ResumeApproved re-run after the user
	// approved a parked tier-2 task.
	resumedMsg struct {
		out *askengine.Result
	}
	// droppedMsg reports that a pump's ask was released while it was parked.
	// It carries no data: there is nothing to fold into the model, and the
	// pump is not re-armed.
	droppedMsg struct{}
)

// askStream carries one in-flight ask: the channel the callbacks write to, the
// context that cancels it (Esc/interrupt), and the outcome slot. It is created
// per ask and discarded when doneMsg lands.
type askStream struct {
	events chan tea.Msg
	cancel context.CancelFunc

	// detached records that the user stopped waiting on this ask (Esc/Ctrl+C).
	// It never stops the work: once the engine has handed a task to the core,
	// the core owns the task's lifetime, so it runs to completion and the
	// out-of-band watcher announces it. The flag exists so the late doneMsg
	// is dropped rather than committed over whatever the user is doing now.
	detached bool

	// dropped is closed when the ask is detached. It wakes a pump that is
	// blocked on events so it can return instead of parking forever. It is
	// only ever closed, never sent on, so releasing an ask can never race an
	// engine send into a closed channel.
	dropped chan struct{}
}

// drop releases an ask: it cancels the engine's context (which unblocks any
// in-flight send) and closes dropped (which unblocks any waiting pump). Safe
// to call once per stream.
func (s *askStream) drop() {
	if s == nil || s.detached {
		return
	}
	s.detached = true
	s.cancel()
	close(s.dropped)
}

// send delivers one engine event to the pump. It selects against ctx so a send
// that finds no reader — because the user released the ask and walked away —
// unblocks instead of parking the engine's goroutine for the life of the
// process. ctx is cancelled only by drop or by the sending goroutine's own
// deferred cancel, which runs after its final send, so an ask still on screen
// always receives its outcome.
func (s *askStream) send(ctx context.Context, m tea.Msg) {
	select {
	case s.events <- m:
	case <-ctx.Done():
	}
}

// startAsk launches AskTurns on a background goroutine and returns the stream
// plus the first command to pump it. The callbacks push onto a buffered channel;
// they select against ctx.Done so a cancelled ask (Esc) never blocks the engine
// goroutine on a send into a channel the model has stopped draining.
func startAsk(engine *askengine.Engine, history []entry.Turn, prompt, workDir string, authorize bool) (*askStream, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &askStream{
		events:  make(chan tea.Msg, 256),
		cancel:  cancel,
		dropped: make(chan struct{}),
	}

	cb := askengine.StreamCallbacks{
		OnDelta:     func(text string) { s.send(ctx, deltaMsg(text)) },
		OnReasoning: func(text string) { s.send(ctx, reasoningMsg(text)) },
		OnProgress:  func(p askengine.Progress) { s.send(ctx, progressMsg(p)) },
		// OnApproval stays nil: a synchronous yes/no from a Bubble Tea card is not
		// answerable from this goroutine, so we take the NeedsApproval Result path
		// (the engine parks the task and returns it) and prompt on the model loop,
		// then ResumeApproved — the same split the classic REPL uses.
	}
	go func() {
		defer cancel()
		out, err := engine.AskTurns(ctx, history, prompt, workDir, authorize, cb)
		// The outcome must reach the model whenever it is still listening, and
		// must not park this goroutine forever when it is not. send covers
		// both: ctx is cancelled only (a) by drop(), which means the user
		// stopped waiting and the watcher now owns the announcement, or (b) by
		// the defer above, which runs after this send. So while a turn is on
		// screen the send blocks until it is delivered, and once the turn is
		// released it unblocks instead of leaking a goroutine.
		s.send(ctx, doneMsg{stream: s, out: out, err: err})
	}()
	return s, waitForActivity(s)
}

// waitForActivity is the pump: it blocks on the stream's channel and returns the
// next event as a tea.Msg. The model re-issues it after every non-terminal event
// so the loop stays fed; on doneMsg it stops re-issuing and the ask ends.
func waitForActivity(s *askStream) tea.Cmd {
	return func() tea.Msg {
		if s == nil {
			return droppedMsg{}
		}
		select {
		case m := <-s.events:
			return m
		// The ask was released while this pump was parked. Returning a no-op
		// message (rather than blocking forever) is what keeps a detached turn
		// from leaking one goroutine per interrupt.
		case <-s.dropped:
			return droppedMsg{}
		}
	}
}

// resumeApproved re-runs a parked tier-2 task with consent, off the model loop.
func resumeApproved(engine *askengine.Engine, taskID, workDir string) tea.Cmd {
	return func() tea.Msg {
		return resumedMsg{out: engine.ResumeApproved(taskID, workDir)}
	}
}
