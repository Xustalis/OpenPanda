// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// ---- harness ----

// stubTransport answers every request through fn — no test may touch the real
// network, and the gate must be exercised through the same http.Client the
// production paths use.
type stubTransport func(*http.Request) (*http.Response, error)

func (f stubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// okMessage is a valid Messages-API answer carrying text "hello".
func okMessage() *http.Response {
	body := `{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// offlineTestClient builds a client that looks remote (so the gate arms) with
// a stubbed transport and stubbed probes. The base URL is deliberately a
// public-looking host: a loopback one would disarm the gate by design.
func offlineTestClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	c, err := NewClient(config.ModelConfig{
		BaseURL: "https://api.example.com/v1",
		APIKey:  "sk-test",
		Model:   "test-model",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	// The probe target is env-dependent in production (a configured proxy
	// stands in for the endpoint); pin it so the test does not inherit the
	// developer's HTTPS_PROXY.
	c.localProxy = false
	c.offlineTarget = "api.example.com:443"
	c.offlineEvery = time.Millisecond
	c.hc = &http.Client{Transport: rt}
	c.retryBase = time.Millisecond
	c.maxRetry = 1
	return c
}

// probeScript returns a probe stub that fails its first failures calls, then
// answers, counting every call.
func probeScript(calls *atomic.Int32, failures int32) func(context.Context, string, time.Duration) error {
	return func(context.Context, string, time.Duration) error {
		if calls.Add(1) <= failures {
			return errors.New("dial tcp 203.0.113.9:443: connect: network is unreachable")
		}
		return nil
	}
}

// ---- endpoint classification ----

// TestIsLocalEndpoint pins the "needs no internet" set the gate exempts: a
// local model keeps working during an outage, so waiting on the public
// network for it would be pure delay.
func TestIsLocalEndpoint(t *testing.T) {
	local := []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080",
		"http://127.1.2.3/v1",
		"http://[::1]:11434/v1",
		"http://192.168.1.10:8000/v1",
		"https://10.0.0.5",
		"http://172.20.3.4:1234",
		"http://169.254.10.10",
		"http://[fd00::1]:11434",
		"http://ollama:11434",
		"https://gpu-box.internal",
		"http://model.lan",
	}
	for _, base := range local {
		if !IsLocalEndpoint(base) {
			t.Errorf("IsLocalEndpoint(%q) = false, want true", base)
		}
	}
	remote := []string{
		"",
		"https://api.deepseek.com/anthropic",
		"https://api.openai.com/v1",
		"http://8.8.8.8",
		"https://relay.example.com",
		"https://api.example.com/v1",
	}
	for _, base := range remote {
		if IsLocalEndpoint(base) {
			t.Errorf("IsLocalEndpoint(%q) = true, want false", base)
		}
	}
}

// TestDialAddrOf pins the probe address resolution: an explicit port is kept,
// the scheme's default fills in otherwise, and a bare host:port parses.
func TestDialAddrOf(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/anthropic": "api.deepseek.com:443",
		"https://api.example.com":            "api.example.com:443",
		"http://localhost:11434/v1":          "localhost:11434",
		"http://8.8.8.8":                     "8.8.8.8:80",
		"localhost:11434":                    "localhost:11434",
		"":                                   "",
	}
	for in, want := range cases {
		if got := dialAddrOf(in); got != want {
			t.Errorf("dialAddrOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOfflineWaitDefault pins model.offline_wait's tri-state: unset means on,
// an explicit false restores the fail-fast behaviour.
func TestOfflineWaitDefault(t *testing.T) {
	c, err := NewClient(config.ModelConfig{BaseURL: "https://api.example.com/v1", APIKey: "sk"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if !c.offlineWait {
		t.Error("offline gate should be armed by default")
	}
	off := false
	c, err = NewClient(config.ModelConfig{BaseURL: "https://api.example.com/v1", APIKey: "sk", OfflineWait: &off})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if c.offlineWait {
		t.Error("model.offline_wait: false should disarm the gate")
	}
}

// TestProbeTargetForProxy pins how a configured proxy lands on the gate's
// probe: a proxy on loopback or the LAN is dialed but not trusted as evidence
// (it answers whatever the uplink does), a remote one is both.
func TestProbeTargetForProxy(t *testing.T) {
	cases := []struct {
		proxy, target string
		local         bool
	}{
		{"http://127.0.0.1:7890", "127.0.0.1:7890", true},
		{"http://localhost:8080", "localhost:8080", true},
		{"http://192.168.1.5:3128", "192.168.1.5:3128", true},
		{"socks5://10.0.0.2:1080", "10.0.0.2:1080", true},
		{"http://proxy.corp.example:8080", "proxy.corp.example:8080", false},
		{"https://egress.example.com", "egress.example.com:443", false},
		{"", "", false},
	}
	for _, tc := range cases {
		target, local := probeTargetForProxy(tc.proxy)
		if target != tc.target || local != tc.local {
			t.Errorf("probeTargetForProxy(%q) = (%q, %v), want (%q, %v)",
				tc.proxy, target, local, tc.target, tc.local)
		}
	}
}

// ---- the gate ----

// TestOfflineGateWaitsThenReplays is the whole feature in one path: a call
// fails with a transport error while the link is down, the gate waits, the
// link returns, and the call replays — successfully, without spending a retry
// attempt.
func TestOfflineGateWaitsThenReplays(t *testing.T) {
	var attempts atomic.Int32
	rt := stubTransport(func(*http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("dial tcp: connect: network is unreachable")
		}
		return okMessage(), nil
	})
	c := offlineTestClient(t, rt)
	var probes atomic.Int32
	c.probe = probeScript(&probes, 1) // offline on entry, back on the first poll
	c.anchor = func(context.Context) bool { return false }

	var notes []OfflineNote
	ctx := WithOfflineHook(context.Background(), func(n OfflineNote) {
		notes = append(notes, n)
	})
	resp, err := c.CompleteTurnsWithTools(ctx, "sys", []Turn{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Text != "hello" {
		t.Errorf("text = %q, want hello", resp.Text)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2 (fail once, replay after the wait)", got)
	}
	if probes.Load() < 2 {
		t.Errorf("probes = %d, want the entry probe plus at least one poll", probes.Load())
	}
	if len(notes) != 2 {
		t.Fatalf("notes = %+v, want a waiting note and a resumed note", notes)
	}
	if !notes[0].Waiting || notes[0].Elapsed != 0 {
		t.Errorf("first note = %+v, want waiting with no elapsed", notes[0])
	}
	if notes[1].Waiting {
		t.Errorf("second note = %+v, want resumed", notes[1])
	}
}

// TestOfflineGateSkipsLocalEndpoint pins the exemption: a local model is
// reachable whether or not the world is, so the gate never probes for it.
func TestOfflineGateSkipsLocalEndpoint(t *testing.T) {
	rt := stubTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
	})
	c, err := NewClient(config.ModelConfig{
		BaseURL: "http://127.0.0.1:11434/v1", APIType: config.APITypeOpenAI, Model: "qwen3:8b",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if !c.localEndpoint {
		t.Fatal("loopback endpoint should be marked local")
	}
	c.hc = &http.Client{Transport: rt}
	c.retryBase = time.Millisecond
	c.maxRetry = 0
	var probes atomic.Int32
	c.probe = probeScript(&probes, 1<<30) // would fail forever if consulted
	c.anchor = func(context.Context) bool { return false }

	if _, err := c.CompleteTurnsWithTools(context.Background(), "sys", []Turn{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected the call to fail")
	}
	if got := probes.Load(); got != 0 {
		t.Errorf("probes = %d, want 0 for a local endpoint", got)
	}
}

// TestOfflineGateDefersToAnchors pins the discriminator: the endpoint being
// unreachable while the machine still has internet is a provider problem, and
// the gate leaves it to the retry ladder.
func TestOfflineGateDefersToAnchors(t *testing.T) {
	rt := stubTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 203.0.113.9:443: i/o timeout")
	})
	c := offlineTestClient(t, rt)
	var probes atomic.Int32
	c.probe = probeScript(&probes, 1<<30)
	c.anchor = func(context.Context) bool { return true } // the uplink is fine
	var notes atomic.Int32
	ctx := WithOfflineHook(context.Background(), func(OfflineNote) { notes.Add(1) })

	if _, err := c.CompleteTurnsWithTools(ctx, "sys", []Turn{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected the call to fail after the ladder")
	}
	if notes.Load() != 0 {
		t.Errorf("offline notes = %d, want 0 when the anchors answer", notes.Load())
	}
	if got := probes.Load(); got != 2 {
		t.Errorf("probes = %d, want one per failed attempt (2)", got)
	}
}

// TestOfflineGateDisabledByConfig pins model.offline_wait: false.
func TestOfflineGateDisabledByConfig(t *testing.T) {
	off := false
	rt := stubTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: network is unreachable")
	})
	c, err := NewClient(config.ModelConfig{
		BaseURL: "https://api.example.com/v1", APIKey: "sk", Model: "m", OfflineWait: &off,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.hc = &http.Client{Transport: rt}
	c.retryBase = time.Millisecond
	c.maxRetry = 0
	var probes atomic.Int32
	c.probe = probeScript(&probes, 1<<30)
	c.anchor = func(context.Context) bool { return false }

	if _, err := c.CompleteTurnsWithTools(context.Background(), "sys", []Turn{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected the call to fail")
	}
	if got := probes.Load(); got != 0 {
		t.Errorf("probes = %d, want 0 when the gate is disabled", got)
	}
}

// TestOfflineGateIgnoresProviderAnswers pins the arming rule: a 429 or a 5xx
// is the provider talking, which proves the link works — the gate must not
// probe, let alone wait, on it.
func TestOfflineGateIgnoresProviderAnswers(t *testing.T) {
	rt := stubTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("slow down")),
		}, nil
	})
	c := offlineTestClient(t, rt)
	var probes atomic.Int32
	c.probe = probeScript(&probes, 1<<30)
	c.anchor = func(context.Context) bool { return false }

	if _, err := c.CompleteTurnsWithTools(context.Background(), "sys", []Turn{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected the call to fail")
	}
	if got := probes.Load(); got != 0 {
		t.Errorf("probes = %d, want 0 for a provider answer", got)
	}
}

// TestOfflineGateHonoursCancellation pins the escape hatch: the wait has no
// timer, so the caller's context is what ends it — Esc in the TUI, the
// console's stop button.
func TestOfflineGateHonoursCancellation(t *testing.T) {
	rt := stubTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: network is unreachable")
	})
	c := offlineTestClient(t, rt)
	c.offlineEvery = 5 * time.Millisecond
	c.probe = probeScript(new(atomic.Int32), 1<<30)
	c.anchor = func(context.Context) bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.CompleteTurnsWithTools(ctx, "sys", []Turn{{Role: "user", Content: "hi"}}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancellation took %s — the wait is not watching the context", elapsed)
	}
}

// TestOfflineGateStreamsThenReplays covers the streaming path: nothing was
// delivered before the drop, so the replay after the wait is safe.
func TestOfflineGateStreamsThenReplays(t *testing.T) {
	var attempts atomic.Int32
	rt := stubTransport(func(*http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("dial tcp: network is unreachable")
		}
		body := "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
			"data: {\"type\":\"message_stop\"}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	c := offlineTestClient(t, rt)
	c.hcStream = &http.Client{Transport: rt}
	c.probe = probeScript(new(atomic.Int32), 1)
	c.anchor = func(context.Context) bool { return false }

	var got strings.Builder
	resp, err := c.StreamTurnsWithTools(context.Background(), "sys",
		[]Turn{{Role: "user", Content: "hi"}}, nil,
		func(s string) { got.WriteString(s) }, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Text != "hi" || got.String() != "hi" {
		t.Errorf("resp = %q, deltas = %q, want hi/hi", resp.Text, got.String())
	}
	if n := attempts.Load(); n != 2 {
		t.Errorf("attempts = %d, want 2", n)
	}
}
