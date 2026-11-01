// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

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
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/guard"
)

// TUI event messages. Each is a tea.Msg the root model folds into its state.
type (
	// Every streamed event carries its source. Delayed callbacks from an
	// interrupted ask can then be discarded without touching a newer turn.
	deltaMsg struct {
		stream *askStream
		text   string
	}
	reasoningMsg struct {
		stream *askStream
		text   string
	}
	progressMsg struct {
		stream   *askStream
		progress askengine.Progress
	}
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
		stream *askStream
		out    *askengine.Result
	}
	// droppedMsg reports that a pump's ask was released while it was parked.
	// It carries no data: there is nothing to fold into the model, and the
	// pump is not re-armed.
	droppedMsg struct{}

	// taskRowMsg names the task row this ask created, delivered before that
	// task runs. A mid-turn Enter needs it to address the board: without it
	// the only target would be whatever the user last opened in the picker.
	taskRowMsg struct {
		stream *askStream
		taskID string
	}
)

// askStream carries one in-flight ask: the channel the callbacks write to, the
// context that cancels it (Esc/interrupt), and the outcome slot. It is created
// per ask and discarded when doneMsg lands.
type askStream struct {
	events chan tea.Msg
	cancel context.CancelFunc

	// detached records that the user stopped waiting on this ask (Esc/Ctrl+C),
	// and prevents its late completion from being committed over a newer turn.
	// Dropping cancels the caller context; a task not yet owned by the core stops
	// immediately, while a claimed foreground approval resume is terminalized by
	// Core.ResumeApproved. Independently scheduled work keeps core ownership.
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
// mode carries the slash-prefix interaction mode (/goal, /plan, /spec) — ""
// lets the classifier decide on its own. sessionID binds any session-scoped
// remembered approval this turn produces to the attached session. agents is
// the conversation's roster (the console's team pick): it bounds this round's
// dispatch budget and rides every task's spec, exactly as the panel's ask does.
// node is the one-shot /node pin for this turn ("" lets the scheduler choose).
// newAskStream allocates one in-flight ask — its event channel, its cancel, its
// dropped signal — and the callbacks that fan engine events onto it. The main
// turn and the side channel build on it alike; they differ only in the scope
// they ask with and in which part of the model folds the events.
func newAskStream() (*askStream, context.Context, askengine.StreamCallbacks) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &askStream{
		events:  make(chan tea.Msg, 256),
		cancel:  cancel,
		dropped: make(chan struct{}),
	}

	cb := askengine.StreamCallbacks{
		OnDelta:     func(text string) { s.send(ctx, deltaMsg{stream: s, text: text}) },
		OnReasoning: func(text string) { s.send(ctx, reasoningMsg{stream: s, text: text}) },
		OnProgress:  func(p askengine.Progress) { s.send(ctx, progressMsg{stream: s, progress: p}) },
		// GetSteer / OnSteerConsumed stay nil on purpose. Mid-turn input is the
		// user's NEXT turn, not a late fragment of the one already streaming: a
		// steer channel let the model read the same words twice (once in the
		// user turn, once folded back from the task board) and could not be
		// taken back. The pending queue in tui_update.go replaces it; the
		// durable /queue and /inject commands still cover cross-process
		// steering through the board.
		// The task this turn is about to run, reported while the row still
		// has work ahead of it — see StageNote in tui_actions.go.
		OnTaskRow: func(taskID string) {
			s.send(ctx, taskRowMsg{stream: s, taskID: taskID})
		},
		// OnApproval stays nil: a synchronous yes/no from a Bubble Tea card is not
		// answerable from this goroutine, so we take the NeedsApproval Result path
		// (the engine parks the task and returns it) and prompt on the model loop,
		// then ResumeApproved — the same split the classic REPL uses.
	}
	return s, ctx, cb
}

func startAsk(engine *askengine.Engine, history []entry.Turn, prompt, workDir, sessionID string, agents []string, node string, authorize bool, mode string) (*askStream, tea.Cmd) {
	s, ctx, cb := newAskStream()
	guard.Go(nil, "tui: ask worker", func() {
		// A panicking worker must still land a doneMsg: without it the pump
		// parks on events forever and the turn hangs on the spinner. The
		// deferred cancel already ran, so send needs an uncancelled ctx.
		s.send(context.Background(), doneMsg{stream: s, err: errors.New("ask worker panicked (see log)")})
	}, func() {
		defer s.cancel()
		// Ambient fallback keeps parity with the classic REPL path — a
		// session-less ask may still bind the daemon's working dir — and the
		// scoped form carries the thread's roster and node pin.
		out, err := engine.AskTurnsScoped(ctx, history, prompt, askengine.AskScope{
			WorkDir:        workDir,
			Mode:           mode,
			SessionID:      sessionID,
			Agents:         agents,
			Node:           node,
			AmbientProject: true,
		}, authorize, cb)
		// The outcome must reach the model whenever it is still listening, and
		// must not park this goroutine forever when it is not. send covers
		// both: ctx is cancelled only (a) by drop(), which means the user
		// stopped waiting and the watcher now owns the announcement, or (b) by
		// the defer above, which runs after this send. So while a turn is on
		// screen the send blocks until it is delivered, and once the turn is
		// released it unblocks instead of leaking a goroutine.
		s.send(ctx, doneMsg{stream: s, out: out, err: err})
	})
	return s, waitForActivity(s)
}

// startSideAsk is startAsk for the side channel: same pump, same cancellation,
// but the scope marks the ask as side, so the engine narrows the prompt, trims
// the tool face to read-only and refuses to dispatch. It keeps the main
// thread's workDir and roster — the whole point of a side question is that it
// is about the same work — while carrying its own session id so nothing it
// learns is written into the main thread's approval record.
func startSideAsk(engine *askengine.Engine, history []entry.Turn, prompt, workDir, sessionID string, agents []string, node string, authorize bool) (*askStream, tea.Cmd) {
	s, ctx, cb := newAskStream()
	guard.Go(nil, "tui: side worker", func() {
		s.send(context.Background(), doneMsg{stream: s, err: errors.New("side ask worker panicked (see log)")})
	}, func() {
		defer s.cancel()
		out, err := engine.AskTurnsScoped(ctx, history, prompt, askengine.AskScope{
			WorkDir:        workDir,
			SessionID:      sessionID,
			Agents:         agents,
			Node:           node,
			AmbientProject: true,
			Side:           true,
		}, authorize, cb)
		s.send(ctx, doneMsg{stream: s, out: out, err: err})
	})
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

// startResume re-runs a parked task through the same cancellable progress pump
// as an ask, so its route/exec/tool/judge phases extend the existing task card.
// A stage parked inside a plan gets the plan watch chained on its resume —
// the turn follows the pipeline to the next human gate or verdict instead of
// closing on the resumed task's receipt.
func startResume(engine *askengine.Engine, taskID, planID, workDir string) (*askStream, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &askStream{
		events:  make(chan tea.Msg, 256),
		cancel:  cancel,
		dropped: make(chan struct{}),
	}
	cb := askengine.StreamCallbacks{
		OnProgress: func(p askengine.Progress) {
			s.send(ctx, progressMsg{stream: s, progress: p})
		},
	}
	guard.Go(nil, "tui: resume worker", func() {
		s.send(context.Background(), doneMsg{stream: s, err: errors.New("resume worker panicked (see log)")})
	}, func() {
		defer cancel()
		out := engine.ResumeApproved(ctx, taskID, workDir, cb)
		if planID != "" {
			if awaited := engine.AwaitPlanOutcome(ctx, planID, cb); awaited != nil {
				out = awaited
			}
		}
		s.send(ctx, resumedMsg{stream: s, out: out})
	})
	return s, waitForActivity(s)
}
