//go:build !lite

package main

// Splitting an X10 mouse event is not something Update can be handed by
// constructing messages by hand: the prefix arrives as bubbletea's unexported
// unknownCSISequenceMsg, which nothing outside the package can build. These
// tests therefore drive a real bubbletea program over a reader that hands out
// one chunk per Read — which is exactly how a read boundary ends up inside an
// escape sequence.

import (
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// x10ChunkReader returns one chunk per Read, so the caller decides where the
// read boundaries fall.
type x10ChunkReader struct {
	chunks [][]byte
	i      int
}

func (r *x10ChunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}

// x10Capture records every message a program receives. The lock is for the
// count() poll below, which runs while the program goroutine is still live.
type x10Capture struct {
	mu  sync.Mutex
	got []tea.Msg
}

func (c *x10Capture) Init() tea.Cmd { return nil }
func (c *x10Capture) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	c.mu.Lock()
	c.got = append(c.got, msg)
	c.mu.Unlock()
	return c, nil
}
func (c *x10Capture) View() string { return "" }

func (c *x10Capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

func (c *x10Capture) messages() []tea.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]tea.Msg(nil), c.got...)
}

// x10Messages runs a program over the chunks and returns what it received.
//
// EOF on the input does not stop a bubbletea program — the reader treats it as
// "no more input for now" and waits — so the program is stopped once the
// message stream has gone quiet instead of on a fixed delay.
func x10Messages(t *testing.T, chunks [][]byte) []tea.Msg {
	t.Helper()
	c := &x10Capture{}
	p := tea.NewProgram(c,
		tea.WithInput(&x10ChunkReader{chunks: chunks}),
		tea.WithoutRenderer(),
		tea.WithOutput(io.Discard),
	)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_, _ = p.Run()
	}()

	start := time.Now()
	quietSince := start
	for {
		before := c.count()
		time.Sleep(20 * time.Millisecond)
		if c.count() == before {
			if time.Since(quietSince) > 200*time.Millisecond {
				break
			}
		} else {
			quietSince = time.Now()
		}
		if time.Since(start) > 5*time.Second {
			break
		}
	}
	p.Quit()
	<-stopped
	return c.messages()
}

// unknownCSIType is the reflect.Type of bubbletea's unexported
// unknownCSISequenceMsg, captured from a real message because it cannot be named
// from here. It is what lets the guard be tested against arbitrary contents
// without going through the reader.
func unknownCSIType(t *testing.T) reflect.Type {
	t.Helper()
	for _, msg := range x10Messages(t, [][]byte{{0x1b, 0x5b, 0x4d}}) {
		if typ := reflect.TypeOf(msg); typ != nil && typ.String() == unknownCSIMsgName {
			return typ
		}
	}
	t.Fatalf("no %s in the stream; Bubble Tea's parsing changed", unknownCSIMsgName)
	return nil
}

// fakeCSI builds a message of that type carrying exactly the given bytes.
func fakeCSI(t *testing.T, typ reflect.Type, b []byte) tea.Msg {
	t.Helper()
	v := reflect.New(typ).Elem()
	v.SetBytes(b)
	return v.Interface()
}

// TestX10PreludeIsIdentifiedByShapeNotContents pins the property the guard
// depends on: it must not look at the message's bytes.
//
// Bubble Tea slices this message out of its reusable 256-byte read buffer, so by
// the time Update runs the contents are already the next read's bytes. Reading
// them would make the guard fire or not fire according to what the terminal sent
// next — the case below where the buffer has been refilled with an arrow key is
// the one that would silently stop protecting the prompt.
func TestX10PreludeIsIdentifiedByShapeNotContents(t *testing.T) {
	typ := unknownCSIType(t)

	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"the prefix itself", []byte{0x1b, '[', 'M'}},
		{"buffer refilled with the next coordinates", []byte{'`', '[', '-'}},
		{"buffer refilled with a later arrow key", []byte{0x1b, '[', 'A'}},
		{"buffer refilled with typed text", []byte("abc")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !isX10MousePrelude(fakeCSI(t, typ, tc.b)) {
				t.Errorf("% x should be an X10 prelude", tc.b)
			}
		})
	}

	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"an event cut before its prefix", []byte{0x1b, '['}},
		{"the prefix plus a stray byte", []byte{0x1b, '[', 'M', 'x'}},
		{"a longer terminal report", []byte{0x1b, '[', '?', '1', ';', '2', 'c'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if isX10MousePrelude(fakeCSI(t, typ, tc.b)) {
				t.Errorf("% x is not the three-byte prefix", tc.b)
			}
		})
	}

	t.Run("a keystroke", func(t *testing.T) {
		if isX10MousePrelude(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")}) {
			t.Error("a keystroke is not an X10 prelude")
		}
	})
}

// TestSplitX10MouseEventSurfacesAnUnrecognisedCSI keeps the guard honest about
// why it exists at all: it fails the moment Bubble Tea renames the type, or
// starts parsing the truncated prefix itself, either of which would leave the
// coordinates with nowhere to be caught.
func TestSplitX10MouseEventSurfacesAnUnrecognisedCSI(t *testing.T) {
	for name, chunks := range map[string][][]byte{
		"boundary after the prefix":   {{0x1b, 0x5b, 0x4d}, {0x60, 0x5b, 0x2d}},
		"boundary inside the payload": {{0x1b, 0x5b, 0x4d, 0x60}, {0x5b, 0x2d}},
		"boundary at the last byte":   {{0x1b, 0x5b, 0x4d, 0x60, 0x5b}, {0x2d}},
	} {
		t.Run(name, func(t *testing.T) {
			msgs := x10Messages(t, chunks)
			for _, msg := range msgs {
				if isX10MousePrelude(msg) {
					return
				}
			}
			t.Fatalf("no X10 prelude recognised in %v", msgs)
		})
	}
}

// TestWholeX10MouseEventIsAMouseMsg is the other half of the contract: an
// intact six-byte event parses into a MouseMsg, so the fix must neither be
// needed nor interfere there.
func TestWholeX10MouseEventIsAMouseMsg(t *testing.T) {
	var sawMouse bool
	for _, msg := range x10Messages(t, [][]byte{{0x1b, 0x5b, 0x4d, 0x60, 0x5b, 0x2d}}) {
		if _, ok := msg.(tea.MouseMsg); ok {
			sawMouse = true
		}
		if isX10MousePrelude(msg) {
			t.Errorf("an intact event must not look like a split one: %v", msg)
		}
	}
	if !sawMouse {
		t.Fatal("a whole six-byte X10 event should arrive as a MouseMsg")
	}
}

// newIdleX10Model is the minimal chat-mode model, matching the other TUI tests.
// The mouse mode matters: X10 is only what a terminal speaks while the app holds
// the mouse, and that is what arms the guard.
func newIdleX10Model(t *testing.T, mouse mouseMode) tuiModel {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkPath: "/test"},
		Model:   config.ModelConfig{BaseURL: "http://localhost:8080", Model: "test"},
		UI:      config.UIConfig{Onboarded: true, TermsAccepted: true},
	}
	r := &repl{loc: i18n.English, cfg: cfg, interactive: true}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.mouse = mouse
	m.width, m.height = 100, 30
	return m
}

// TestSplitX10MouseEventIsNotTypedIntoThePrompt is the reported bug: scrolling
// on a terminal without SGR mouse support typed the event's coordinate bytes
// into the input bar.
//
// The last two cases keep the fix honest. These bytes are also something a
// person can type, so swallowing them by shape rather than by provenance would
// trade a display bug for a data-loss one.
func TestSplitX10MouseEventIsNotTypedIntoThePrompt(t *testing.T) {
	cases := []struct {
		name   string
		chunks [][]byte
		want   string
	}{
		{"whole event", [][]byte{{0x1b, 0x5b, 0x4d, 0x60, 0x5b, 0x2d}}, ""},
		{"boundary after the prefix", [][]byte{{0x1b, 0x5b, 0x4d}, {0x60, 0x5b, 0x2d}}, ""},
		{"boundary inside the payload", [][]byte{{0x1b, 0x5b, 0x4d, 0x60}, {0x5b, 0x2d}}, ""},
		{"boundary at the last byte", [][]byte{{0x1b, 0x5b, 0x4d, 0x60, 0x5b}, {0x2d}}, ""},
		{"two split events in a row", [][]byte{
			{0x1b, 0x5b, 0x4d}, {0x60, 0x5b, 0x2d},
			{0x1b, 0x5b, 0x4d}, {0x60, 0x5b, 0x2d},
		}, ""},
		// A click carries button 0, whose Cb is a space, so the payload does
		// not arrive as one run of characters.
		{"a click's space Cb", [][]byte{{0x1b, 0x5b, 0x4d}, {0x20, 0x5b, 0x2d}}, ""},
		// Past column 95 the coordinates are high bytes, and one that is not
		// valid UTF-8 on its own is reported as an unknown byte rather than a
		// character.
		{"a coordinate past column 95", [][]byte{{0x1b, 0x5b, 0x4d}, {0x60, 0x80, 0x2d}}, ""},
		{"typing the same bytes by hand still reaches the prompt", [][]byte{[]byte("`[-")}, "`[-"},
		{"typing right after a split event survives", [][]byte{
			{0x1b, 0x5b, 0x4d}, {0x60, 0x5b, 0x2d}, []byte("hi"),
		}, "hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newIdleX10Model(t, mouseScroll)
			for _, msg := range x10Messages(t, tc.chunks) {
				next, _ := m.Update(msg)
				m = next.(tuiModel)
			}
			if got := m.ta.Value(); got != tc.want {
				t.Errorf("prompt = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestX10GuardStaysDisarmedWithoutTheMouse pins the other half of the guard: the
// count is only armed while the app holds the mouse.
//
// mouseSelect leaves the mouse with the terminal, so the wheel arrives as arrow
// keys and X10 never appears. Arming on a stray three-byte CSI in that mode would
// swallow the next three characters the user typed, which is a worse bug than the
// one being fixed.
func TestX10GuardStaysDisarmedWithoutTheMouse(t *testing.T) {
	m := newIdleX10Model(t, mouseSelect)
	for _, msg := range x10Messages(t, [][]byte{{0x1b, 0x5b, 0x4d}, {0x60, 0x5b, 0x2d}}) {
		next, _ := m.Update(msg)
		m = next.(tuiModel)
	}
	if got, want := m.ta.Value(), "`[-"; got != want {
		t.Errorf("prompt = %q, want %q (the guard must not arm without capture)", got, want)
	}
}
