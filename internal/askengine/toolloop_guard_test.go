// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func overflowClassifyError() error {
	return &entry.ClassifyError{
		Status:  400,
		Detail:  "maximum context length exceeded",
		UserMsg: "bad request: maximum context length exceeded",
	}
}

func TestClassifyBoundedRetriesOverflowOnSameModel(t *testing.T) {
	e := &Engine{} // nil logger: guard paths must stay safe without one
	var sizes []int
	calls := 0
	run := func(ts []entry.Turn) (entry.Output, error) {
		calls++
		sizes = append(sizes, toolResultRunes(ts))
		if calls == 1 {
			return entry.Output{}, overflowClassifyError()
		}
		return entry.Output{Kind: entry.KindAnswer, Answer: "recovered"}, nil
	}
	turns := []entry.Turn{toolResultTurn("1", strings.Repeat("x", 20000))}
	out, fitted, err := e.classifyBounded(context.Background(), nil, turns, run)
	if err != nil {
		t.Fatalf("overflow should be retried, got %v", err)
	}
	if out.Answer != "recovered" {
		t.Fatalf("answer = %q, want recovered", out.Answer)
	}
	if calls != 2 {
		t.Fatalf("classify calls = %d, want 2 (one rejection + one retry)", calls)
	}
	if len(sizes) != 2 || sizes[1] >= sizes[0] {
		t.Fatalf("tool-result sizes = %v, want the retry to carry a smaller transcript", sizes)
	}
	if toolResultRunes(fitted) != sizes[1] {
		t.Fatal("returned turns do not carry the trimming forward")
	}
}

func TestClassifyBoundedDoesNotRetryOtherErrors(t *testing.T) {
	e := &Engine{}
	plain := errors.New("some other failure")
	calls := 0
	run := func([]entry.Turn) (entry.Output, error) {
		calls++
		return entry.Output{}, plain
	}
	_, _, err := e.classifyBounded(context.Background(), nil, nil, run)
	if !errors.Is(err, plain) {
		t.Fatalf("err = %v, want the original error", err)
	}
	if calls != 1 {
		t.Fatalf("classify calls = %d, want 1 for a non-overflow failure", calls)
	}
}

func TestClassifyBoundedCapsOverflowRetries(t *testing.T) {
	e := &Engine{}
	calls := 0
	run := func([]entry.Turn) (entry.Output, error) {
		calls++
		return entry.Output{}, overflowClassifyError()
	}
	_, _, err := e.classifyBounded(context.Background(), nil, nil, run)
	if err == nil {
		t.Fatal("persistent overflow must surface as an error")
	}
	if calls != 1+maxOverflowRetries {
		t.Fatalf("classify calls = %d, want 1 + %d retries", calls, maxOverflowRetries)
	}
}

func TestLoopFailureReason(t *testing.T) {
	if got := loopFailureReason(nil, i18n.ChineseSimp); got != "" {
		t.Fatalf("nil error = %q", got)
	}
	if got := loopFailureReason(errors.New("boom"), i18n.ChineseSimp); got != "中断原因：boom" {
		t.Fatalf("zh = %q", got)
	}
	if got := loopFailureReason(errors.New("boom"), i18n.English); got != "Reason: boom" {
		t.Fatalf("en = %q", got)
	}
	long := errors.New(strings.Repeat("错", 1000))
	got := loopFailureReason(long, i18n.ChineseSimp)
	if n := len([]rune(got)); n > 310 {
		t.Fatalf("reason has %d runes, want <= 310", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("bounded reason lost its truncation marker")
	}
}

func TestContainedPanicResult(t *testing.T) {
	e := &Engine{}
	res, err := e.containedPanicResult("boom")
	if res != nil || err == nil {
		t.Fatalf("res = %v, err = %v", res, err)
	}
	if !strings.Contains(err.Error(), "contained") {
		t.Fatalf("err = %q, want a contained-error wording", err)
	}
}

func TestEmptyAnswerNotice(t *testing.T) {
	if !strings.Contains(emptyAnswerNotice(i18n.ChineseSimp), "未返回") {
		t.Fatal("zh notice lost its meaning")
	}
	if !strings.Contains(emptyAnswerNotice(i18n.English), "no text") {
		t.Fatal("en notice lost its meaning")
	}
}

func TestExecuteToolCheckedContainsPanic(t *testing.T) {
	reg := entry.NewRegistry()
	reg.Register(entry.Tool{
		Name: "panicky", Tier: defense.TierReversible,
		Schema: map[string]any{"type": "object"},
		Run: func(context.Context, map[string]any) (string, error) {
			panic("secret internals")
		},
	})
	got, err := executeToolChecked(context.Background(), reg, &entry.ToolCall{Tool: "panicky"}, true, false, "")
	if err == nil {
		t.Fatal("a tool panic must surface as an error to the caller")
	}
	var pe *toolPanicError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T, want *toolPanicError", err)
	}
	if !strings.Contains(got, "已隔离") || strings.Contains(got, "secret internals") {
		t.Fatalf("model-facing result = %q, want a contained failure without internals", got)
	}
	// The string-only wrapper used by other callers behaves the same.
	if got := executeTool(context.Background(), reg, &entry.ToolCall{Tool: "panicky"}, true, false, ""); strings.Contains(got, "secret internals") {
		t.Fatal("executeTool leaked a panic value")
	}
}

func TestExecuteToolCheckedNilInputs(t *testing.T) {
	if _, err := executeToolChecked(context.Background(), nil, nil, true, false, ""); err == nil {
		t.Fatal("nil call/registry must return an error")
	}
	if _, err := executeToolChecked(context.Background(), entry.NewRegistry(), nil, true, false, ""); err == nil {
		t.Fatal("nil call must return an error")
	}
}

// newGuardTestEngine wires a bare engine to a local OpenAI-compatible server,
// the same way the existing max-rounds regression test does. MaxRetries=-1
// keeps deliberate 400s from spending the transport backoff ladder.
func newGuardTestEngine(t *testing.T, srv *httptest.Server, reg *entry.Registry, window int) *Engine {
	t.Helper()
	client, err := entry.NewClient(config.ModelConfig{
		APIType: "openai", BaseURL: srv.URL, Model: "test-model", APIKey: "test-key",
		ContextWindow: window, MaxRetries: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &Engine{
		cfg:      &config.Config{},
		injector: memory.NewInjector(nil, nil),
		registry: reg,
		db:       db,
	}
	e.client.Store(client)
	return e
}

const toolCallResponse = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"dump","arguments":"{}"}}]}}]}`

func writeAnswer(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+text+`"}}]}`)
}

// TestAskTurnsToolTranscriptStaysBounded is the direct regression for the
// crash: six rounds of a tool that dumps 50 KB each must not produce a request
// that grows without bound. Without the budget, the sixth request would carry
// roughly 300 KB of replayed file dumps.
func TestAskTurnsToolTranscriptStaysBounded(t *testing.T) {
	var mu sync.Mutex
	var bodies []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, len(b))
		mu.Unlock()
		if strings.Contains(string(b), `"tools"`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, toolCallResponse)
			return
		}
		writeAnswer(w, "收敛完成")
	}))
	defer srv.Close()

	reg := entry.NewRegistry()
	reg.Register(entry.Tool{
		Name: "dump", Tier: defense.TierReversible,
		Schema: map[string]any{"type": "object"},
		Run: func(context.Context, map[string]any) (string, error) {
			return strings.Repeat("x", 50000), nil
		},
	})
	e := newGuardTestEngine(t, srv, reg, 16000)
	res, err := e.AskTurns(context.Background(), nil, "run the dump tool repeatedly", "", true, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "answer" || !strings.Contains(res.Answer, "收敛完成") {
		t.Fatalf("res = %+v, want a converged answer", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 6 {
		t.Fatalf("only %d requests, want the full tool loop to run", len(bodies))
	}
	max := 0
	for _, n := range bodies {
		if n > max {
			max = n
		}
	}
	if max > 120000 {
		t.Fatalf("largest request was %d bytes, want < 120000 (unbounded would exceed 300000)", max)
	}
}

// TestAskTurnsContextOverflowRecovers pins the second half of the fix: an
// oversized-request rejection is retried on the same model with a tighter
// transcript, instead of turning into "stopped responding after N tools".
func TestAskTurnsContextOverflowRecovers(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"maximum context length exceeded"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"压缩后成功"}}]}`)
	}))
	defer srv.Close()

	e := newGuardTestEngine(t, srv, entry.NewRegistry(), 8000)
	res, err := e.AskTurns(context.Background(), nil, "run a quick check", "", true, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !strings.Contains(res.Answer, "压缩后成功") || strings.Contains(res.Answer, "停止响应") {
		t.Fatalf("res = %+v, want the recovered answer, not an interruption digest", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2 (rejected, then retried on the same model)", calls)
	}
}

// TestAskTurnsToolPanicIsContained verifies that a crashing tool adapter does
// not take down the ask: the loop sees a contained failure as the tool_result
// and still converges to a normal answer.
func TestAskTurnsToolPanicIsContained(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, toolCallResponse)
			return
		}
		writeAnswer(w, "工具异常已隔离")
	}))
	defer srv.Close()

	reg := entry.NewRegistry()
	reg.Register(entry.Tool{
		Name: "dump", Tier: defense.TierReversible,
		Schema: map[string]any{"type": "object"},
		Run: func(context.Context, map[string]any) (string, error) {
			panic("boom")
		},
	})
	e := newGuardTestEngine(t, srv, reg, 8000)
	res, err := e.AskTurns(context.Background(), nil, "run the dump tool", "", true, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !strings.Contains(res.Answer, "工具异常已隔离") {
		t.Fatalf("res = %+v, want a converged answer after the contained panic", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 || !strings.Contains(bodies[1], "已隔离") {
		t.Fatal("the recovered panic was not replayed to the model as a contained failure")
	}
}

// TestAskTurnsLoopFailureShowsReason pins the error-visibility fix: when the
// model fails after tools already ran, the reply still says what ran, but it
// must also name the real cause instead of only "stopped responding".
func TestAskTurnsLoopFailureShowsReason(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = io.WriteString(w, toolCallResponse)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid request schema"}}`)
	}))
	defer srv.Close()

	reg := entry.NewRegistry()
	reg.Register(entry.Tool{
		Name: "dump", Tier: defense.TierReversible,
		Schema: map[string]any{"type": "object"},
		Run: func(context.Context, map[string]any) (string, error) {
			return "dump ok", nil
		},
	})
	e := newGuardTestEngine(t, srv, reg, 8000)
	res, err := e.AskTurns(context.Background(), nil, "run the dump tool", "", true, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !strings.Contains(res.Answer, "停止响应") || !strings.Contains(res.Answer, "模型调用失败中断") {
		t.Fatalf("res = %+v, want a digest plus an interruption notice", res)
	}
	// The summary stays protocol-free: the provider's own text belongs in the
	// structured warning and the log, not in the list of executed tools.
	if strings.Contains(res.Answer, "invalid request schema") || strings.Contains(res.Answer, "DSML") {
		t.Fatalf("raw provider text leaked into the digest: %q", res.Answer)
	}
	if !strings.Contains(res.Warning, "中断原因") || !strings.Contains(res.Warning, "invalid request schema") {
		t.Fatalf("warning = %q, want the provider's real failure reason", res.Warning)
	}
}
