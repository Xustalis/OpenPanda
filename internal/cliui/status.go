package cliui

// The live status line.
//
// One line at the bottom of the screen carrying a spinner, the verb for what is
// happening right now, and the numbers a user actually wants while they wait
// (elapsed time, tokens). It replaces the single static "thinking…" line the
// REPL used to print before going silent for thirty seconds.
//
// Three rules make it safe to run alongside streaming output:
//
//   - Every write goes through one mutex, so the animation goroutine and the
//     caller's own printing never interleave mid-escape-sequence.
//   - It never emits a newline of its own. The line is repainted in place with
//     \r, so it occupies exactly one physical row — hence the width-aware
//     truncation: a wrapped status line would leave orphaned rows behind.
//   - Anything the caller wants to print during a run goes through Log or
//     Suspend, which erase the status line first and repaint it after.
//
// On a non-TTY (pipes, CI, `panda ask | tee`) construct it with live=false: it
// then prints one static line per event and animates nothing.

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
	eraseLine  = "\r\x1b[K"
)

// Status is a live one-line progress indicator. Construct with NewStatus; all
// methods are safe to call from any goroutine, and every one of them is a no-op
// on a Status that is not running, so callers need no state of their own.
type Status struct {
	w        io.Writer
	pal      Palette
	frames   []string
	interval time.Duration
	live     bool
	width    func() int // terminal columns; nil means "unknown, do not truncate"

	mu      sync.Mutex
	verb    string
	note    string
	preview string
	hint    string
	unit    string // translated word for the token counter ("tokens", "个 token")
	tokens  int64
	start   time.Time
	total   time.Duration // frozen elapsed, set by Stop
	frame   int
	shown   bool // a status line is currently on screen
	running bool
	stop    chan struct{}
	done    chan struct{}

	// phase chain (P0 redesign §4). Phase records are kept ordered; the
	// current phase also duplicates into .note so it still surfaces on a
	// repaint even when we haven't reworked the render chrome yet.
	phases       []PhaseRecord
	currentPhase string
}

// PhaseRecord is one finished (or in-flight) orbit step.
type PhaseRecord struct {
	Name  string // stable id: "classify" | "route" | "exec" | "done"
	Label string // human label
	Start time.Time
	Dur   time.Duration // zero means still active
}

// NewStatus builds a status line writing to w. live enables the animation and
// in-place repainting; pass false for anything that is not an interactive
// terminal.
func NewStatus(w io.Writer, p Palette, live bool) *Status {
	return &Status{w: w, pal: p, frames: Frames(p), interval: FrameInterval, live: live}
}

// SetWidth supplies the terminal width, re-read on every paint so a resized
// window is honoured immediately. Without it the line is never truncated.
func (s *Status) SetWidth(fn func() int) *Status { s.width = fn; return s }

// SetInterval overrides the frame interval (tests use a fast one).
func (s *Status) SetInterval(d time.Duration) *Status {
	if d > 0 {
		s.interval = d
	}
	return s
}

// Start begins a run with verb as the current activity ("thinking", "routing").
// Calling Start on an already-running status just changes the verb, so a caller
// that cannot tell whether a previous run ended is still correct.
func (s *Status) Start(verb string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.verb = verb
		s.paintLocked()
		return
	}
	s.verb, s.note, s.tokens = verb, "", 0
	s.preview = ""
	s.start, s.total = time.Now(), 0
	s.frame, s.running = 0, true
	if !s.live {
		// One static line, then silence: piped output and dumb terminals get
		// the same information without twelve repaints a second.
		fmt.Fprintf(s.w, "%s %s\n", s.pal.MarkBullet(), verb)
		return
	}
	s.paintLocked()
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go s.animate(s.stop, s.done)
}

// animate advances the spinner until stop closes. It holds the same mutex as
// every other writer, so a frame can never land inside someone else's line.
func (s *Status) animate(stop, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.mu.Lock()
			if s.running {
				s.frame++
				s.paintLocked()
			}
			s.mu.Unlock()
		}
	}
}

// Verb changes the activity word mid-run. Silent when not animating: the verb
// is a live detail, and reprinting a line per change would be noise.
func (s *Status) Verb(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || v == s.verb {
		return
	}
	s.verb = v
	s.paintLocked()
}

// Note sets the trailing detail on the status line ("running tool memory_get").
// It is ephemeral — use Log for something the user should still see afterwards.
func (s *Status) Note(n string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note = n
	s.paintLocked()
}

// Hint sets a dimmed trailing hint, typically the interrupt key.
func (s *Status) Hint(h string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hint = h
	s.paintLocked()
}

// Phase advances the orbit's phase chain. name is a stable id the caller can
// re-enter to correct an earlier optimistic guess; label is the user-facing
// text. If the phase is already current the call is a no-op so the caller
// never needs to track whether it already fired.
//
// Completes any previously in-flight phase and prints a lightweight progress
// summary to .note ("classify → route → executing"), which keeps the full
// render chrome untouched (§D3 render is still handled by the front-end
// DecisionOrbit component; CLI here only gives at-a-glance parity).
func (s *Status) Phase(name, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		return
	}
	now := time.Now()
	// Close the previously in-flight phase (same-name re-entry is a no-op).
	if len(s.phases) > 0 && s.currentPhase != name {
		last := &s.phases[len(s.phases)-1]
		if last.Dur == 0 {
			last.Dur = now.Sub(last.Start)
		}
	}
	if s.currentPhase != name {
		s.phases = append(s.phases, PhaseRecord{Name: name, Label: label, Start: now})
		s.currentPhase = name
	}
	// Reflect the chain through the note field so the status line shows it
	// without touching renderLocked's width math.
	s.note = s.phaseChainLocked()
	s.paintLocked()
}

// PhaseHistory returns a shallow copy of the phase record list — for the
// closing summary line. Live durations are resolved at call time.
func (s *Status) PhaseHistory() []PhaseRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := make([]PhaseRecord, len(s.phases))
	copy(out, s.phases)
	for i := range out {
		if out[i].Dur == 0 && !out[i].Start.IsZero() {
			out[i].Dur = now.Sub(out[i].Start)
		}
	}
	return out
}

func (s *Status) phaseChainLocked() string {
	if len(s.phases) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.phases))
	for _, p := range s.phases {
		if p.Label != "" {
			parts = append(parts, p.Label)
		} else {
			parts = append(parts, p.Name)
		}
	}
	return strings.Join(parts, " → ")
}

// Preview shows a live tail of text that is still arriving — the words the
// model has streamed since the last line break. Without it a long paragraph
// looks identical to a stalled request: the spinner turns and nothing else
// moves until a newline finally lands. Whatever does not fit is cut from the
// FRONT, so the newest words are the ones on screen. Passing "" clears it.
func (s *Status) Preview(text string) {
	// Collapse whitespace: a preview is one row, and a streamed chunk carries
	// whatever indentation the model was writing with.
	text = strings.Join(strings.Fields(text), " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == s.preview {
		return
	}
	s.preview = text
	s.paintLocked()
}

// SetTokens updates the token counter shown on the line. Zero hides it.
func (s *Status) SetTokens(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n == s.tokens {
		return
	}
	s.tokens = n
	s.paintLocked()
}

// Suspend runs f with the status line off the screen and repaints afterwards.
// This is how anything else prints during a run — streamed answer text, a
// completion notice from the task watcher — without fighting the spinner for
// the cursor.
func (s *Status) Suspend(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearLocked()
	f()
	s.paintLocked()
}

// Log prints one permanent line above the status line.
func (s *Status) Log(line string) {
	s.Suspend(func() { fmt.Fprintln(s.w, line) })
}

// Stop ends the run, erases the status line and returns how long it took. It is
// idempotent, so `defer st.Stop()` alongside an explicit Stop is fine.
func (s *Status) Stop() time.Duration {
	s.mu.Lock()
	if !s.running {
		d := s.total
		s.mu.Unlock()
		return d
	}
	s.running = false
	s.total = time.Since(s.start)
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()

	// Join the animation goroutine outside the lock: it may be blocked on the
	// mutex trying to paint its last frame.
	if stop != nil {
		close(stop)
		<-done
	}

	s.mu.Lock()
	s.clearLocked()
	d := s.total
	s.mu.Unlock()
	return d
}

// Elapsed is the run's duration — live while running, frozen after Stop.
func (s *Status) Elapsed() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.elapsedLocked()
}

func (s *Status) elapsedLocked() time.Duration {
	if !s.running {
		return s.total
	}
	return time.Since(s.start)
}

// Running reports whether a run is in flight.
func (s *Status) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Stats renders the run's numbers for a closing line: "2.3s · 1.2k tokens".
// Empty when there is nothing to report.
func (s *Status) Stats() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.elapsedLocked()
	if d == 0 && s.tokens == 0 {
		return ""
	}
	out := HumanDuration(d)
	if s.tokens > 0 {
		out += s.pal.Separator() + HumanCount(s.tokens) + " " + s.tokenWord()
	}
	return out
}

// tokenWord is the (translated) unit for the token counter; callers set it
// through SetTokenWord so this package stays locale-free.
func (s *Status) tokenWord() string {
	if s.unit == "" {
		return "tokens"
	}
	return s.unit
}

// SetTokenWord supplies the translated word for the token unit.
func (s *Status) SetTokenWord(w string) *Status {
	s.mu.Lock()
	s.unit = w
	s.mu.Unlock()
	return s
}

// clearLocked erases the status line if one is on screen.
func (s *Status) clearLocked() {
	if !s.shown {
		return
	}
	fmt.Fprint(s.w, eraseLine+showCursor)
	s.shown = false
}

// paintLocked repaints the status line in place. A no-op unless a live run is
// in flight, which is what makes every setter safe to call unconditionally.
func (s *Status) paintLocked() {
	if !s.live || !s.running {
		return
	}
	line := s.renderLocked()
	prefix := ""
	if !s.shown {
		prefix = hideCursor
	}
	fmt.Fprint(s.w, prefix+"\r"+line+"\x1b[K")
	s.shown = true
}

// renderLocked assembles the line: spinner, verb, then the dimmed field list
// (note · elapsed · tokens · hint), and finally the streaming preview in
// whatever columns are left. It measures the unstyled text so a CJK verb
// cannot push the line into a second physical row; if truncation is needed the
// styling is dropped rather than cut mid-escape.
func (s *Status) renderLocked() string {
	frame := s.frames[s.frame%len(s.frames)]
	sep := s.pal.Separator()
	fields := make([]string, 0, 4)
	if s.note != "" {
		fields = append(fields, s.note)
	}
	fields = append(fields, HumanDuration(s.elapsedLocked()))
	if s.tokens > 0 {
		fields = append(fields, HumanCount(s.tokens)+" "+s.tokenWord())
	}
	if s.hint != "" {
		fields = append(fields, s.hint)
	}
	tail := strings.Join(fields, sep)
	plain := frame + " " + s.verb + sep + tail
	styled := s.pal.Accent(frame) + " " + s.verb + s.pal.Muted(sep+tail)

	max := 0
	if s.width != nil {
		max = s.width()
	}
	// Leave the last column free: painting into it arms the terminal's pending
	// wrap, and the next \r would land on a row we did not intend.
	if max > 1 && DisplayWidth(plain) > max-1 {
		return Truncate(plain, max-1, s.pal.Unicode())
	}
	// The preview gets the remainder of the row — and only if the width is
	// known, since a wrapped status line would leave an orphaned row behind.
	if s.preview != "" && max > 1 {
		room := max - 1 - DisplayWidth(plain) - DisplayWidth(sep)
		if room >= minPreviewCols {
			if p := TruncateTail(s.preview, room, s.pal.Unicode()); p != "" {
				styled += s.pal.Muted(sep + p)
			}
		}
	}
	return styled
}

// minPreviewCols is the narrowest preview worth painting; below it the tail is
// all ellipsis and the row is better spent on the numbers.
const minPreviewCols = 12
