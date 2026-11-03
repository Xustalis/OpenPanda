// SPDX-License-Identifier: AGPL-3.0-or-later

// Package browser is the node's headless browser: one Chrome tab an agent can
// open, read, click, type into, resize, screenshot and inspect through the
// console and network logs. It is what makes "check the page I just built" a
// real step instead of a claim.
//
// The browser is deliberately separate from the human preview. The console
// frames a project's real dev server in the reader's browser; this package
// drives its own headless Chrome on the node, so an agent can verify a page
// without stealing the reader's pane, and works even when no one is watching.
//
// Chrome is an external binary, auto-detected here or named by
// OPENPANDA_CHROME. Nothing in this package requires CGO.
package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png" // register the PNG decoder for screenshot dimensions
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// ErrNoChrome says the node has no browser this package can drive.
var ErrNoChrome = errors.New("browser: no Chrome/Chromium found (install one or set OPENPANDA_CHROME)")

// DefaultViewport is the size a fresh page gets before any emulation.
const DefaultViewport = "1280x800"

// Limits keep one page from flooding an agent's context or the host's memory.
const (
	maxTextBytes      = 8000
	maxTreeBytes      = 16000
	maxControls       = 200
	maxConsoleEntries = 300
	maxNetworkEntries = 500
	maxEvalBytes      = 16000
	maxShotPixels     = 6000 // width or height of a screenshot, in CSS px
)

// Control is one interactive element the agent can address by CSS selector.
type Control struct {
	Selector string  `json:"selector"`
	Role     string  `json:"role"`
	Name     string  `json:"name"`
	Text     string  `json:"text,omitempty"`
	Value    string  `json:"value,omitempty"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	Disabled bool    `json:"disabled,omitempty"`
}

// ConsoleEntry is one line the page wrote to the console, or one uncaught
// exception.
type ConsoleEntry struct {
	At    time.Time `json:"at"`
	Type  string    `json:"type"`
	Text  string    `json:"text"`
	URL   string    `json:"url,omitempty"`
	Line  int64     `json:"line,omitempty"`
	Stack string    `json:"stack,omitempty"`
}

// NetworkEntry is one request the page made, with its outcome when known.
type NetworkEntry struct {
	At         time.Time `json:"at"`
	Method     string    `json:"method"`
	URL        string    `json:"url"`
	Type       string    `json:"type,omitempty"`
	Status     int64     `json:"status,omitempty"`
	MimeType   string    `json:"mime,omitempty"`
	Failed     string    `json:"failed,omitempty"`
	DurationMS int64     `json:"ms,omitempty"`
}

// Result is what an operation hands back. Every field is optional; the host
// serializes it as-is and the CLI/MCP format it.
type Result struct {
	URL        string         `json:"url,omitempty"`
	Title      string         `json:"title,omitempty"`
	Text       string         `json:"text,omitempty"`
	Tree       string         `json:"tree,omitempty"`
	Controls   []Control      `json:"controls,omitempty"`
	Value      any            `json:"value,omitempty"`
	Screenshot string         `json:"screenshot,omitempty"`
	Width      int            `json:"width,omitempty"`
	Height     int            `json:"height,omitempty"`
	Console    []ConsoleEntry `json:"console,omitempty"`
	Network    []NetworkEntry `json:"network,omitempty"`
	Message    string         `json:"message,omitempty"`
}

// Options configure a Manager.
type Options struct {
	// ChromePath overrides discovery (OPENPANDA_CHROME also works).
	ChromePath string
	// DataDir holds the browser profile and screenshots.
	DataDir string
	Logger  *slog.Logger
}

// Manager owns one headless browser and its single tab. Operations are
// serialized: one tab cannot be driven from two goroutines at once.
type Manager struct {
	opts Options

	startMu   sync.Mutex
	started   bool
	launchErr error
	profile   string

	allocCtx    context.Context
	allocCancel context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc

	opMu sync.Mutex
	logs *consoleLog
	net  *netLog

	closed bool
}

// Start builds a Manager. Chrome is launched on the first operation, so an
// agent that never touches the browser pays nothing.
func Start(o Options) *Manager {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Manager{
		opts: o,
		logs: newConsoleLog(maxConsoleEntries),
		net:  newNetLog(maxNetworkEntries),
	}
}

// Close ends the tab and the browser.
func (m *Manager) Close() {
	m.startMu.Lock()
	closed := m.closed
	m.closed = true
	cancel := m.cancel
	allocCancel := m.allocCancel
	profile := m.profile
	m.startMu.Unlock()
	if closed {
		return
	}
	if cancel != nil {
		cancel()
	}
	if allocCancel != nil {
		allocCancel()
	}
	if profile != "" {
		_ = os.RemoveAll(profile)
	}
}

func (m *Manager) ensure() error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if m.closed {
		return errors.New("browser: closed")
	}
	if m.started && m.launchErr == nil {
		return nil
	}
	if m.started && m.launchErr != nil {
		// A missing browser, or a launch that failed, is retried on the next
		// call: Chrome may have been installed between attempts.
		m.started = false
	}
	m.started = true
	path := m.opts.ChromePath
	if path == "" {
		path = os.Getenv("OPENPANDA_CHROME")
	}
	if path == "" {
		path = FindChrome()
	}
	if path == "" {
		m.launchErr = ErrNoChrome
		return m.launchErr
	}
	dir := m.opts.DataDir
	if dir == "" {
		dir = os.TempDir()
	}
	profile, err := os.MkdirTemp(dir, "openpanda-browser-*")
	if err != nil {
		m.launchErr = fmt.Errorf("browser: profile dir: %w", err)
		return m.launchErr
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.UserDataDir(profile),
		chromedp.WindowSize(1280, 800),
		chromedp.Flag("disable-notifications", true),
		chromedp.Flag("deny-permission-prompts", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	// The first run allocates the browser; it must not carry a timeout, or
	// the browser dies with it.
	if err := chromedp.Do(ctx); err != nil {
		cancel()
		allocCancel()
		_ = os.RemoveAll(profile)
		m.launchErr = fmt.Errorf("browser: launch %s: %w", path, err)
		return m.launchErr
	}
	m.allocCtx, m.allocCancel, m.ctx, m.cancel = allocCtx, allocCancel, ctx, cancel
	m.profile = profile
	m.launchErr = nil
	m.subscribe()
	return nil
}

// subscribe buffers console output and network activity for later reads. The
// iterators end when the target context ends.
func (m *Manager) subscribe() {
	go func() {
		for msg, err := range chromedp.Console(m.ctx) {
			if err != nil {
				return
			}
			e := ConsoleEntry{
				At:   msg.Time,
				Type: string(msg.Type),
				Text: msg.Text,
				URL:  msg.URL,
				Line: msg.Line,
			}
			if e.At.IsZero() {
				e.At = time.Now()
			}
			if msg.Stack != nil && len(msg.Stack.CallFrames) > 0 {
				var b strings.Builder
				for i, f := range msg.Stack.CallFrames {
					if i >= 8 {
						break
					}
					fmt.Fprintf(&b, "%s (%s:%d)\n", f.FunctionName, f.URL, f.LineNumber)
				}
				e.Stack = strings.TrimSpace(b.String())
			}
			m.logs.add(e)
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(m.ctx, network.RequestWillBeSent) {
			if err != nil {
				return
			}
			m.net.request(&ev)
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(m.ctx, network.ResponseReceived) {
			if err != nil {
				return
			}
			m.net.response(&ev)
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(m.ctx, network.LoadingFailed) {
			if err != nil {
				return
			}
			m.net.failed(&ev)
		}
	}()
}

// run ensures the browser, serializes the operation and bounds it.
func (m *Manager) run(fn func(ctx context.Context) (Result, error)) (Result, error) {
	if err := m.ensure(); err != nil {
		return Result{}, err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 60*time.Second)
	defer cancel()
	res, err := fn(ctx)
	if err != nil {
		return res, err
	}
	return res, nil
}

// Open navigates the tab to url and returns what landed there.
func (m *Manager) Open(rawURL string, viewport string) (Result, error) {
	u, err := normaliseURL(rawURL)
	if err != nil {
		return Result{}, err
	}
	return m.run(func(ctx context.Context) (Result, error) {
		if viewport != "" {
			vp, err := ParseViewport(viewport)
			if err != nil {
				return Result{}, err
			}
			if err := chromedp.Do(ctx, vp.action()); err != nil {
				return Result{}, err
			}
		}
		if err := chromedp.Do(ctx, chromedp.Navigate(u.String())); err != nil {
			return Result{}, err
		}
		return pageIdentity(ctx)
	})
}

// Snapshot returns the page's identity, visible text, interactive controls and
// accessibility outline — enough to decide what to do next without guessing.
func (m *Manager) Snapshot() (Result, error) {
	return m.run(func(ctx context.Context) (Result, error) {
		res, err := pageIdentity(ctx)
		if err != nil {
			return Result{}, err
		}
		text, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.body ? document.body.innerText : ''`))
		if err == nil {
			res.Text = clip(text, maxTextBytes)
		}
		controls, err := chromedp.Run(ctx, chromedp.Evaluate[[]Control](controlsScript))
		if err == nil && len(controls) > maxControls {
			controls = controls[:maxControls]
		}
		res.Controls = controls
		tree, err := axTree(ctx)
		if err == nil {
			res.Tree = clip(tree, maxTreeBytes)
		}
		return res, nil
	})
}

// Screenshot captures the viewport (or the whole page) into DataDir/shots and
// returns the file path an agent can read or attach.
func (m *Manager) Screenshot(full bool) (Result, error) {
	return m.run(func(ctx context.Context) (Result, error) {
		var (
			shot []byte
			err  error
		)
		if full {
			shot, err = chromedp.Run(ctx, chromedp.FullScreenshot(90))
		} else {
			shot, err = chromedp.Run(ctx, chromedp.CaptureScreenshot())
		}
		if err != nil {
			return Result{}, err
		}
		dir := filepath.Join(m.opts.DataDir, "shots")
		if m.opts.DataDir == "" {
			dir = filepath.Join(os.TempDir(), "openpanda-browser-shots")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{}, err
		}
		path := filepath.Join(dir, fmt.Sprintf("shot-%d.png", time.Now().UnixNano()))
		if err := os.WriteFile(path, shot, 0o644); err != nil {
			return Result{}, err
		}
		res := Result{Screenshot: path}
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(shot)); err == nil {
			res.Width, res.Height = cfg.Width, cfg.Height
		}
		return res, nil
	})
}

// Click presses the first element matching selector, or a viewport
// coordinate when no selector is given.
func (m *Manager) Click(selector string, x, y float64) (Result, error) {
	return m.run(func(ctx context.Context) (Result, error) {
		if strings.TrimSpace(selector) != "" {
			if err := chromedp.Do(ctx, chromedp.Click(chromedp.CSS(selector))); err != nil {
				return Result{}, err
			}
		} else {
			if err := chromedp.Do(ctx, chromedp.MouseClickXY(x, y)); err != nil {
				return Result{}, err
			}
		}
		res, _ := pageIdentity(ctx)
		res.Message = "clicked"
		return res, nil
	})
}

// Type enters text into selector (or the focused element). clear replaces the
// field's value first; submit presses Enter afterwards.
func (m *Manager) Type(selector, text string, clear, submit bool) (Result, error) {
	return m.run(func(ctx context.Context) (Result, error) {
		if strings.TrimSpace(selector) != "" {
			sel := chromedp.CSS(selector)
			if clear {
				if err := chromedp.Do(ctx, chromedp.Clear(sel)); err != nil {
					return Result{}, err
				}
			}
			if err := chromedp.Do(ctx, chromedp.SendKeys(sel, text)); err != nil {
				return Result{}, err
			}
		} else {
			if err := chromedp.Do(ctx, chromedp.KeyEvent(text)); err != nil {
				return Result{}, err
			}
		}
		if submit {
			if err := chromedp.Do(ctx, chromedp.KeyEvent("\r")); err != nil {
				return Result{}, err
			}
		}
		res, _ := pageIdentity(ctx)
		res.Message = "typed"
		return res, nil
	})
}

// Press sends one named key to the focused element.
func (m *Manager) Press(key string) (Result, error) {
	spec, ok := namedKey(key)
	if !ok {
		return Result{}, fmt.Errorf("browser: unknown key %q", key)
	}
	return m.run(func(ctx context.Context) (Result, error) {
		if err := chromedp.Do(ctx, pressKey(spec)); err != nil {
			return Result{}, err
		}
		res, _ := pageIdentity(ctx)
		res.Message = "pressed " + key
		return res, nil
	})
}

// Scroll sends a wheel event at the viewport centre.
func (m *Manager) Scroll(dx, dy float64) (Result, error) {
	return m.run(func(ctx context.Context) (Result, error) {
		size, _ := chromedp.Run(ctx, chromedp.Evaluate[map[string]float64](
			`({w: window.innerWidth, h: window.innerHeight})`))
		x, y := size["w"]/2, size["h"]/2
		if x == 0 {
			x = 640
		}
		if y == 0 {
			y = 400
		}
		err := chromedp.Do(ctx, wheel(x, y, dx, dy))
		if err != nil {
			return Result{}, err
		}
		res, _ := pageIdentity(ctx)
		res.Message = "scrolled"
		return res, nil
	})
}

// Eval runs a JavaScript expression and returns its JSON value.
func (m *Manager) Eval(expression string) (Result, error) {
	if strings.TrimSpace(expression) == "" {
		return Result{}, errors.New("browser: empty expression")
	}
	return m.run(func(ctx context.Context) (Result, error) {
		value, err := chromedp.Run(ctx, chromedp.Evaluate[any](expression, chromedp.EvalAwaitPromise))
		if err != nil {
			// undefined and null are answers, not transport failures.
			if errors.Is(err, chromedp.ErrJSUndefined) || errors.Is(err, chromedp.ErrJSNull) {
				return Result{Value: nil, Message: err.Error()}, nil
			}
			return Result{}, err
		}
		raw, _ := json.Marshal(value)
		if len(raw) > maxEvalBytes {
			return Result{Value: clip(string(raw), maxEvalBytes)}, nil
		}
		return Result{Value: json.RawMessage(raw)}, nil
	})
}

// Console returns the newest console messages, oldest first.
func (m *Manager) Console(limit int) Result {
	return Result{Console: m.logs.tail(limit)}
}

// Network returns the newest requests, oldest first.
func (m *Manager) Network(limit int) Result {
	return Result{Network: m.net.tail(limit)}
}

// Viewport applies an emulated viewport: mobile, tablet, desktop, or
// WIDTHxHEIGHT.
func (m *Manager) Viewport(spec string) (Result, error) {
	vp, err := ParseViewport(spec)
	if err != nil {
		return Result{}, err
	}
	return m.run(func(ctx context.Context) (Result, error) {
		if err := chromedp.Do(ctx, vp.action()); err != nil {
			return Result{}, err
		}
		return Result{Width: int(vp.Width), Height: int(vp.Height), Message: "viewport " + spec}, nil
	})
}

// pageIdentity reads the two facts every operation reports.
func pageIdentity(ctx context.Context) (Result, error) {
	res := Result{}
	if loc, err := chromedp.Run(ctx, chromedp.Location()); err == nil {
		res.URL = loc
	}
	if title, err := chromedp.Run(ctx, chromedp.Title()); err == nil {
		res.Title = title
	}
	return res, nil
}

// normaliseURL accepts what an agent may pass and refuses schemes the console
// cannot reason about. file:// is allowed because a built static site is often
// verified before it is packed.
func normaliseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("browser: url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("browser: url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "file":
	default:
		return nil, fmt.Errorf("browser: %q is not a page scheme (http, https or file)", u.Scheme)
	}
	if u.Scheme != "file" && u.Host == "" {
		return nil, errors.New("browser: url has no host")
	}
	return u, nil
}

// ViewportSpec is one emulated screen.
type ViewportSpec struct {
	Width, Height int64
	Scale         float64
	Mobile, Touch bool
	Reset         bool
}

// ParseViewport understands the three names the console uses plus
// WIDTHxHEIGHT (optionally at a scale, WIDTHxHEIGHT@SCALE).
func ParseViewport(spec string) (ViewportSpec, error) {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "", "desktop":
		return ViewportSpec{Width: 1280, Height: 800, Scale: 1}, nil
	case "mobile", "phone":
		return ViewportSpec{Width: 390, Height: 844, Scale: 3, Mobile: true, Touch: true}, nil
	case "tablet", "ipad":
		return ViewportSpec{Width: 768, Height: 1024, Scale: 2, Mobile: true, Touch: true}, nil
	}
	var w, h int64
	var scale float64 = 1
	body := strings.TrimSpace(spec)
	if i := strings.LastIndexByte(body, '@'); i > 0 {
		if _, err := fmt.Sscanf(body[i+1:], "%g", &scale); err != nil || scale <= 0 {
			return ViewportSpec{}, fmt.Errorf("browser: bad viewport scale in %q", spec)
		}
		body = body[:i]
	}
	if _, err := fmt.Sscanf(strings.ToLower(body), "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return ViewportSpec{}, fmt.Errorf("browser: want mobile, tablet, desktop or WIDTHxHEIGHT (got %q)", spec)
	}
	if w > maxShotPixels || h > maxShotPixels {
		return ViewportSpec{}, fmt.Errorf("browser: viewport is too large (max %d)", maxShotPixels)
	}
	return ViewportSpec{Width: w, Height: h, Scale: scale, Mobile: w < 900, Touch: w < 900}, nil
}

func (v ViewportSpec) action() chromedp.Action[chromedp.Void] {
	opts := []chromedp.EmulateViewportOption{chromedp.EmulateScale(v.Scale)}
	if v.Mobile {
		opts = append(opts, chromedp.EmulateMobile)
	}
	if v.Touch {
		opts = append(opts, chromedp.EmulateTouch)
	}
	return chromedp.EmulateViewport(v.Width, v.Height, opts...)
}

// keySpec is one named key as the protocol wants it.
type keySpec struct {
	Key  string
	Code string
	VK   int64
	Text string
}

func namedKey(name string) (keySpec, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "enter", "return":
		return keySpec{Key: "Enter", Code: "Enter", VK: 13, Text: "\r"}, true
	case "tab":
		return keySpec{Key: "Tab", Code: "Tab", VK: 9}, true
	case "escape", "esc":
		return keySpec{Key: "Escape", Code: "Escape", VK: 27}, true
	case "backspace":
		return keySpec{Key: "Backspace", Code: "Backspace", VK: 8}, true
	case "delete", "del":
		return keySpec{Key: "Delete", Code: "Delete", VK: 46}, true
	case "space":
		return keySpec{Key: " ", Code: "Space", VK: 32, Text: " "}, true
	case "arrowup", "up":
		return keySpec{Key: "ArrowUp", Code: "ArrowUp", VK: 38}, true
	case "arrowdown", "down":
		return keySpec{Key: "ArrowDown", Code: "ArrowDown", VK: 40}, true
	case "arrowleft", "left":
		return keySpec{Key: "ArrowLeft", Code: "ArrowLeft", VK: 37}, true
	case "arrowright", "right":
		return keySpec{Key: "ArrowRight", Code: "ArrowRight", VK: 39}, true
	case "home":
		return keySpec{Key: "Home", Code: "Home", VK: 36}, true
	case "end":
		return keySpec{Key: "End", Code: "End", VK: 35}, true
	case "pageup":
		return keySpec{Key: "PageUp", Code: "PageUp", VK: 33}, true
	case "pagedown":
		return keySpec{Key: "PageDown", Code: "PageDown", VK: 34}, true
	}
	return keySpec{}, false
}

func pressKey(k keySpec) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		down := input.DispatchKeyEventParams{
			Type:                  input.DispatchKeyEventTypeKeyDown,
			Key:                   k.Key,
			Code:                  k.Code,
			WindowsVirtualKeyCode: k.VK,
			NativeVirtualKeyCode:  k.VK,
		}
		if k.Text != "" {
			down.Text = k.Text
			down.UnmodifiedText = k.Text
		}
		if _, err := cdp.Call(ctx, t, input.DispatchKeyEvent, down); err != nil {
			return err
		}
		up := down
		up.Type = input.DispatchKeyEventTypeKeyUp
		up.Text = ""
		up.UnmodifiedText = ""
		_, err := cdp.Call(ctx, t, input.DispatchKeyEvent, up)
		return err
	})
}

func wheel(x, y, dx, dy float64) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		_, err := cdp.Call(ctx, t, input.DispatchMouseEvent, input.DispatchMouseEventParams{
			Type:   input.DispatchMouseEventTypeMouseWheel,
			X:      x,
			Y:      y,
			DeltaX: dx,
			DeltaY: dy,
		})
		return err
	})
}

// axTree renders the accessibility tree as indented lines an agent can read.
func axTree(ctx context.Context) (string, error) {
	res, err := chromedp.Call(ctx, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
	if err != nil {
		return "", err
	}
	byID := make(map[accessibility.NodeID]*accessibility.Node, len(res.Nodes))
	for _, n := range res.Nodes {
		byID[n.NodeID] = n
	}
	var b strings.Builder
	lines := 0
	var walk func(n *accessibility.Node, depth int)
	walk = func(n *accessibility.Node, depth int) {
		if n == nil || lines >= 800 || b.Len() > maxTreeBytes {
			return
		}
		if n.Ignored {
			return
		}
		role := axValue(n.Role)
		name := axValue(n.Name)
		value := axValue(n.Value)
		if role != "" || name != "" {
			b.WriteString(strings.Repeat("  ", depth))
			b.WriteString(role)
			if name != "" {
				fmt.Fprintf(&b, " %q", name)
			}
			if value != "" && value != name {
				fmt.Fprintf(&b, " value=%q", value)
			}
			b.WriteByte('\n')
			lines++
		}
		for _, id := range n.ChildIDs {
			walk(byID[id], depth+1)
		}
	}
	for _, n := range res.Nodes {
		if n.ParentID == "" {
			walk(n, 0)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func axValue(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err == nil {
		return s
	}
	return string(v.Value)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… (truncated)"
}

// controlsScript walks the interactive elements and reports a CSS selector
// for each. Selectors are built from tags and nth-child, with IDs preferred,
// so they are stable enough for the click tool without a framework's help.
const controlsScript = `(() => {
  const out = [];
  const seen = new Set();
  const sel = 'a[href],button,input,select,textarea,[role="button"],[role="link"],[role="checkbox"],[role="menuitem"],[contenteditable="true"]';
  const esc = (s) => (window.CSS && CSS.escape) ? CSS.escape(s) : s;
  function cssPath(el) {
    if (el.id) return '#' + esc(el.id);
    const parts = [];
    let n = el;
    while (n && n.nodeType === 1 && parts.length < 6) {
      let name = n.nodeName.toLowerCase();
      const parent = n.parentElement;
      if (parent) {
        const idx = Array.prototype.indexOf.call(parent.children, n) + 1;
        name += ':nth-child(' + idx + ')';
      }
      parts.unshift(name);
      n = parent;
    }
    return parts.join(' > ');
  }
  function roleOf(el) {
    const r = (el.getAttribute('role') || '').toLowerCase();
    if (r) return r;
    const tag = el.nodeName.toLowerCase();
    if (tag === 'a') return 'link';
    if (tag === 'button') return 'button';
    if (tag === 'select') return 'combobox';
    if (tag === 'textarea') return 'textbox';
    if (tag === 'input') {
      const t = (el.getAttribute('type') || 'text').toLowerCase();
      if (t === 'checkbox' || t === 'radio') return t;
      if (t === 'submit' || t === 'button') return 'button';
      return 'textbox';
    }
    return tag;
  }
  function labelOf(el) {
    return (el.getAttribute('aria-label') || el.getAttribute('placeholder') ||
      el.getAttribute('alt') || (el.innerText || '').trim() ||
      el.getAttribute('name') || '').trim().slice(0, 120);
  }
  for (const el of document.querySelectorAll(sel)) {
    if (seen.has(el)) continue;
    seen.add(el);
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    if (r.width <= 0 || r.height <= 0 || cs.visibility === 'hidden' || cs.display === 'none') continue;
    out.push({
      selector: cssPath(el),
      role: roleOf(el),
      name: labelOf(el),
      text: (el.innerText || '').trim().slice(0, 120),
      value: ('value' in el ? String(el.value || '').slice(0, 120) : ''),
      x: Math.round(r.x), y: Math.round(r.y),
      w: Math.round(r.width), h: Math.round(r.height),
      disabled: !!el.disabled
    });
    if (out.length >= 200) break;
  }
  return out;
})()`

// FindChrome looks for a browser this package can drive. OPENPANDA_CHROME
// wins; the rest are the install locations and PATH names of Chrome, Chromium
// and Edge on each platform.
func FindChrome() string {
	candidates := []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"microsoft-edge", "msedge",
	}
	if runtime.GOOS == "darwin" {
		candidates = append([]string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}, candidates...)
	}
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if base == "" {
				continue
			}
			candidates = append([]string{
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
			}, candidates...)
		}
	}
	for _, c := range candidates {
		if filepath.IsAbs(c) {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}
