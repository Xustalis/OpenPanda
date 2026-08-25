package entry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// newCacheDB opens an in-memory migrated database for DiskCache tests.
func newCacheDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

// ---- DiskCache ----

func TestDiskCacheRoundTrip(t *testing.T) {
	dc := NewDiskCache(newCacheDB(t))
	ctx := context.Background()

	dc.Put(ctx, "classify", "k1", "k2", map[string]int{"answer": 1})
	var got map[string]int
	if !dc.Get(ctx, "classify", "k1", "k2", &got) {
		t.Fatal("expected cache hit after Put")
	}
	if got["answer"] != 1 {
		t.Fatalf("got = %v, want {answer:1}", got)
	}
}

func TestDiskCacheMissOnUnknownOrChangedKey(t *testing.T) {
	dc := NewDiskCache(newCacheDB(t))
	ctx := context.Background()
	dc.Put(ctx, "classify", "k1", "k2", "x")

	var s string
	if dc.Get(ctx, "classify", "other", "k2", &s) {
		t.Fatal("changed k1 must miss")
	}
	if dc.Get(ctx, "classify", "k1", "other", &s) {
		t.Fatal("changed k2 must miss")
	}
	if dc.Get(ctx, "supervise", "k1", "k2", &s) {
		t.Fatal("changed namespace must miss")
	}
}

func TestDiskCacheTTLEviction(t *testing.T) {
	db := newCacheDB(t)
	// A negative TTL makes every row instantly expired, deterministically.
	dc := &DiskCache{db: db, ttl: -time.Second}
	ctx := context.Background()
	dc.Put(ctx, "classify", "k1", "k2", "x")
	var s string
	if dc.Get(ctx, "classify", "k1", "k2", &s) {
		t.Fatal("expired row must miss")
	}
}

func TestDiskCacheNilIsNoOp(t *testing.T) {
	var dc *DiskCache
	var s string
	if dc.Get(context.Background(), "ns", "k1", "k2", &s) {
		t.Fatal("nil cache must miss")
	}
	dc.Put(context.Background(), "ns", "k1", "k2", "x") // must not panic

	if NewDiskCache(nil) != nil {
		t.Fatal("NewDiskCache(nil) must return a nil (disabled) cache")
	}
}

// ---- classify disk cache ----

// startCountingAnswerServer spins up a fake Anthropic endpoint that always
// answers with the same prose and counts requests.
func startCountingAnswerServer(t *testing.T) (*Client, *int32, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": "今天天气不错，晴。"}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, &calls, &mu
}

func serverCalls(calls *int32, mu *sync.Mutex) int32 {
	mu.Lock()
	defer mu.Unlock()
	return *calls
}

func TestClassifyDiskCacheHitSkipsLLM(t *testing.T) {
	c, calls, mu := startCountingAnswerServer(t)
	c.SetDiskCache(NewDiskCache(newCacheDB(t)))
	ctx := context.Background()

	out1, err := Classify(ctx, c, nil, "", "今天天气怎么样")
	if err != nil {
		t.Fatalf("classify 1: %v", err)
	}
	out2, err := Classify(ctx, c, nil, "", "今天天气怎么样")
	if err != nil {
		t.Fatalf("classify 2: %v", err)
	}
	if got := serverCalls(calls, mu); got != 1 {
		t.Fatalf("llm calls = %d, want 1 (second ask served from cache)", got)
	}
	if out1.Kind != out2.Kind || out1.Answer != out2.Answer {
		t.Fatalf("cached output %+v differs from live %+v", out2, out1)
	}
}

func TestClassifyDiskCacheKeyChangesOnInputs(t *testing.T) {
	c, calls, mu := startCountingAnswerServer(t)
	c.SetDiskCache(NewDiskCache(newCacheDB(t)))
	ctx := context.Background()

	devs := []ledger.Node{{Name: "n1", Chip: "m1"}}
	if _, err := Classify(ctx, c, devs, "", "问题"); err != nil {
		t.Fatalf("classify 1: %v", err)
	}
	if _, err := Classify(ctx, c, devs, "", "问题"); err != nil {
		t.Fatalf("classify 2 (identical): %v", err)
	}
	if got := serverCalls(calls, mu); got != 1 {
		t.Fatalf("identical inputs should hit cache, llm calls = %d", got)
	}

	// A changed device snapshot must miss.
	devs2 := []ledger.Node{{Name: "n1", Chip: "m1"}, {Name: "n2", Chip: "m2"}}
	if _, err := Classify(ctx, c, devs2, "", "问题"); err != nil {
		t.Fatalf("classify 3 (new device): %v", err)
	}
	// A changed memory wall must miss.
	if _, err := Classify(ctx, c, devs, "用户偏好暗色主题", "问题"); err != nil {
		t.Fatalf("classify 4 (new memory): %v", err)
	}
	// A changed prompt must miss.
	if _, err := Classify(ctx, c, devs, "", "另一个问题"); err != nil {
		t.Fatalf("classify 5 (new prompt): %v", err)
	}
	if got := serverCalls(calls, mu); got != 4 {
		t.Fatalf("llm calls = %d, want 4 (1 cached + 3 changed-input misses)", got)
	}
}

func TestClassifyDiskCacheToolRosterChangesKey(t *testing.T) {
	c, calls, mu := startCountingAnswerServer(t)
	c.SetDiskCache(NewDiskCache(newCacheDB(t)))
	ctx := context.Background()

	reg := NewRegistry()
	reg.Register(Tool{Name: "memory_add", Description: "remember", Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil }})
	if _, err := ClassifyTurnsWithTools(ctx, c, nil, "", []Turn{{Role: "user", Content: "hi"}}, reg); err != nil {
		t.Fatalf("classify 1: %v", err)
	}
	if _, err := ClassifyTurnsWithTools(ctx, c, nil, "", []Turn{{Role: "user", Content: "hi"}}, reg); err != nil {
		t.Fatalf("classify 2: %v", err)
	}
	reg.Register(Tool{Name: "memory_read", Description: "read", Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil }})
	if _, err := ClassifyTurnsWithTools(ctx, c, nil, "", []Turn{{Role: "user", Content: "hi"}}, reg); err != nil {
		t.Fatalf("classify 3: %v", err)
	}
	if got := serverCalls(calls, mu); got != 2 {
		t.Fatalf("llm calls = %d, want 2 (changed tool roster must miss)", got)
	}
}

func TestClassifyWithoutDiskCacheAlwaysCalls(t *testing.T) {
	c, calls, mu := startCountingAnswerServer(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := Classify(ctx, c, nil, "", "今天天气怎么样"); err != nil {
			t.Fatalf("classify %d: %v", i+1, err)
		}
	}
	if got := serverCalls(calls, mu); got != 2 {
		t.Fatalf("llm calls = %d, want 2 (no cache attached)", got)
	}
}

func TestClassifyStreamCacheHitDeliversAnswerDelta(t *testing.T) {
	var mu sync.Mutex
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{"今天", "天气不错"} {
			b, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]string{"content": chunk}}},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.SetDiskCache(NewDiskCache(newCacheDB(t)))
	ctx := context.Background()

	var deltas1 []string
	out1, err := ClassifyStreamWithTools(ctx, c, nil, "", []Turn{{Role: "user", Content: "天气"}}, nil, func(s string) { deltas1 = append(deltas1, s) })
	if err != nil {
		t.Fatalf("stream 1: %v", err)
	}
	var deltas2 []string
	out2, err := ClassifyStreamWithTools(ctx, c, nil, "", []Turn{{Role: "user", Content: "天气"}}, nil, func(s string) { deltas2 = append(deltas2, s) })
	if err != nil {
		t.Fatalf("stream 2: %v", err)
	}
	if out1.Answer != out2.Answer {
		t.Fatalf("cached answer %q != live %q", out2.Answer, out1.Answer)
	}
	if got := strings.Join(deltas2, ""); got != out1.Answer {
		t.Fatalf("cache-hit deltas = %q, want the full answer %q", got, out1.Answer)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("llm calls = %d, want 1", calls)
	}
}

// ---- supervise disk cache ----

func TestSuperviseDiskCacheHitAndMiss(t *testing.T) {
	var mu sync.Mutex
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{
			{"type": "text", "text": `{"status":"done","reason":"all met","followup":""}`},
		}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.SetDiskCache(NewDiskCache(newCacheDB(t)))
	ctx := context.Background()

	if _, err := Supervise(ctx, c, "修复登录 bug", "已完成并验证"); err != nil {
		t.Fatalf("supervise 1: %v", err)
	}
	v, err := Supervise(ctx, c, "修复登录 bug", "已完成并验证")
	if err != nil {
		t.Fatalf("supervise 2: %v", err)
	}
	if v.Status != "done" {
		t.Fatalf("cached verdict = %+v, want done", v)
	}
	if _, err := Supervise(ctx, c, "修复登录 bug", "只改了一半"); err != nil {
		t.Fatalf("supervise 3: %v", err)
	}
	if _, err := Supervise(ctx, c, "写文档", "已完成并验证"); err != nil {
		t.Fatalf("supervise 4: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatalf("llm calls = %d, want 3 (1 live + 1 cached + 2 changed-input misses)", calls)
	}
}

func TestSuperviseFailOpenNotCached(t *testing.T) {
	var mu sync.Mutex
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": "抱歉，我无法判断。"}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.SetDiskCache(NewDiskCache(newCacheDB(t)))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		v, err := Supervise(ctx, c, "task A", "agent said done")
		if err != nil {
			t.Fatalf("supervise %d: %v", i+1, err)
		}
		if v.Status != "done" {
			t.Fatalf("verdict %d = %+v, want done (fail open)", i+1, v)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("llm calls = %d, want 2 (fail-open verdicts must not be cached)", calls)
	}
}

// ---- prompt layering ----

func TestChooseLayers(t *testing.T) {
	// Empty history: no optional layers.
	if l := ChooseLayers(nil); l.MemoryRules || l.TaskExample {
		t.Fatalf("empty history = %+v, want no layers", l)
	}

	// A native memory tool_use block anywhere in history attaches the rules.
	memTurn := Turn{Role: "assistant", Blocks: []ContentBlock{{Type: "tool_use", Name: "memory_add"}}}
	if l := ChooseLayers([]Turn{{Role: "user", Content: "hi"}, memTurn}); !l.MemoryRules || l.TaskExample {
		t.Fatalf("memory tool turn = %+v, want MemoryRules only", l)
	}

	// The text-JSON fallback shape also counts as memory-tool activity.
	if l := ChooseLayers([]Turn{{Role: "assistant", Content: `{"tool":"memory_add"}`}}); !l.MemoryRules {
		t.Fatalf("fallback memory tool = %+v, want MemoryRules", l)
	}

	// A recent assistant task marker attaches the task example.
	taskTurn := Turn{Role: "assistant", Content: "[任务 t-1 done] 修复完成"}
	if l := ChooseLayers([]Turn{taskTurn}); !l.TaskExample || l.MemoryRules {
		t.Fatalf("task turn = %+v, want TaskExample only", l)
	}

	// A task marker from a user turn does not (only assistant turns record
	// task outcomes).
	if l := ChooseLayers([]Turn{{Role: "user", Content: "[任务 t-1 done]"}}); l.TaskExample {
		t.Fatalf("user task marker = %+v, want no TaskExample", l)
	}

	// A task marker older than the recent window does not attach the example.
	var old []Turn
	for i := 0; i < layersWindow+2; i++ {
		old = append(old, Turn{Role: "user", Content: "filler"})
	}
	old[0] = taskTurn
	if l := ChooseLayers(old); l.TaskExample {
		t.Fatalf("stale task marker = %+v, want no TaskExample", l)
	}
}

func TestBuildPromptLayerInjection(t *testing.T) {
	base := BuildPrompt(PromptOptions{})
	if !strings.Contains(base, "类型 3") {
		t.Fatal("resident core (routing rules) missing from the prompt")
	}
	if strings.Contains(base, "记忆治理规则") {
		t.Fatal("memory rules must not attach without memory-tool history")
	}
	if strings.Contains(base, "task 完整示例") {
		t.Fatal("task example must not attach without recent task history")
	}

	mem := BuildPrompt(PromptOptions{History: []Turn{{Role: "assistant", Blocks: []ContentBlock{{Type: "tool_use", Name: "memory_add"}}}}})
	if !strings.Contains(mem, "记忆治理规则") {
		t.Fatal("memory rules missing after memory-tool use")
	}

	task := BuildPrompt(PromptOptions{History: []Turn{{Role: "assistant", Content: "[任务 t-1 done] ok"}}})
	if !strings.Contains(task, "task 完整示例") {
		t.Fatal("task example missing after a recent task turn")
	}
}

func TestDeviceSummaryCache(t *testing.T) {
	resetDeviceSummaryCache(t)
	devs := []ledger.Node{{Name: "macbook", Chip: "M1", Native: []ledger.NativeAbility{{ID: "build:macos"}}}}

	first := summarizeDevicesCached(devs)
	if first == "" {
		t.Fatal("empty device summary")
	}

	// Poison the cached result under the same key: a cache hit must return it
	// verbatim instead of rebuilding.
	deviceSummaryCache.mu.Lock()
	savedKey, savedResult := deviceSummaryCache.key, deviceSummaryCache.result
	deviceSummaryCache.result = "CACHED"
	deviceSummaryCache.mu.Unlock()
	if got := summarizeDevicesCached(devs); got != "CACHED" {
		t.Fatalf("unchanged snapshot should reuse the cache, got %q", got)
	}

	// A changed snapshot must rebuild.
	other := []ledger.Node{{Name: "worker", Chip: "x86"}}
	if got := summarizeDevicesCached(other); got == "CACHED" || got == "" {
		t.Fatalf("changed snapshot must rebuild, got %q", got)
	}

	deviceSummaryCache.mu.Lock()
	deviceSummaryCache.key, deviceSummaryCache.result = savedKey, savedResult
	deviceSummaryCache.mu.Unlock()
}

func resetDeviceSummaryCache(t *testing.T) {
	t.Helper()
	deviceSummaryCache.mu.Lock()
	deviceSummaryCache.key = ""
	deviceSummaryCache.result = ""
	deviceSummaryCache.mu.Unlock()
}

func TestSplitPromptSections(t *testing.T) {
	stable, volatile := splitPromptSections("rules\ndevice\n\n═══ 用户记忆（参考） ═══\n偏好")
	if strings.Contains(stable, "用户记忆") || !strings.HasPrefix(volatile, "═══ 用户记忆") {
		t.Fatalf("split at marker failed: stable=%q volatile=%q", stable, volatile)
	}
	stable, volatile = splitPromptSections("plain prompt")
	if stable != "plain prompt" || volatile != "" {
		t.Fatalf("markerless prompt should be fully stable, got %q / %q", stable, volatile)
	}
}

// ---- session prefix cache (provider-native markers) ----

func TestSystemPayloadCacheMarker(t *testing.T) {
	c, err := NewClient(config.ModelConfig{BaseURL: "https://api.example.com", APIKey: "sk"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	sys := "rules\n═══ 当前可用设备 ═══\n- n1\n\n═══ 用户记忆 ═══\n偏好"

	blocks, ok := c.systemPayload(sys).([]systemBlock)
	if !ok || len(blocks) != 2 {
		t.Fatalf("expected 2 system blocks, got %#v", c.systemPayload(sys))
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("stable block missing cache_control: %+v", blocks[0])
	}
	if strings.Contains(blocks[0].Text, "用户记忆") {
		t.Fatal("volatile memory section leaked into the stable cached prefix")
	}
	if blocks[1].CacheControl != nil {
		t.Fatalf("volatile block must not carry a cache marker: %+v", blocks[1])
	}

	// Caching off: the system stays a plain string (escape hatch).
	c.SetPromptCaching(false)
	if s, ok := c.systemPayload(sys).(string); !ok || s != sys {
		t.Fatalf("caching off should return the plain string, got %#v", c.systemPayload(sys))
	}
}

func TestAnthropicRequestCarriesCacheMarker(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": "ok"}}}
		out, _ := json.Marshal(resp)
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	sys := "rules\n\n═══ 用户记忆 ═══\n偏好"
	if _, err := c.CompleteTurnsWithTools(context.Background(), sys, []Turn{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var req struct {
		System []systemBlock `json:"system"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("parse request: %v\n%s", err, body)
	}
	if len(req.System) != 2 {
		t.Fatalf("system blocks = %d, want 2 (stable + volatile)", len(req.System))
	}
	if req.System[0].CacheControl == nil {
		t.Fatal("stable system block missing cache_control breakpoint")
	}
}

func TestOpenAIPromptCacheKey(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}}
		out, _ := json.Marshal(resp)
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	sys := "rules\n\n═══ 用户记忆 ═══\n偏好"
	if _, err := c.CompleteTurnsWithTools(context.Background(), sys, []Turn{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("complete 1: %v", err)
	}
	// Same stable prefix (identical memory) → identical cache key.
	if _, err := c.CompleteTurnsWithTools(context.Background(), sys, []Turn{{Role: "user", Content: "again"}}, nil); err != nil {
		t.Fatalf("complete 2: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d", len(bodies))
	}
	var keys []string
	for _, b := range bodies {
		var req struct {
			PromptCacheKey string `json:"prompt_cache_key"`
		}
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Fatalf("parse request: %v", err)
		}
		if len(req.PromptCacheKey) != 32 {
			t.Fatalf("prompt_cache_key = %q, want 32 hex chars", req.PromptCacheKey)
		}
		keys = append(keys, req.PromptCacheKey)
	}
	if keys[0] != keys[1] {
		t.Fatal("identical stable prefixes must produce the same prompt_cache_key")
	}
}

func TestOpenAIStreamSendsCacheKeyAndUsageOption(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]string{"content": "ok"}}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if _, err := c.StreamTurnsWithTools(context.Background(), "sys", []Turn{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("stream: %v", err)
	}
	var req struct {
		PromptCacheKey string            `json:"prompt_cache_key"`
		StreamOptions  *oaiStreamOptions `json:"stream_options"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("parse request: %v\n%s", err, body)
	}
	if req.PromptCacheKey == "" {
		t.Fatal("streaming request missing prompt_cache_key")
	}
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options = %+v, want include_usage", req.StreamOptions)
	}

	// Opt-out strips both hints.
	c.SetPromptCaching(false)
	if _, err := c.StreamTurnsWithTools(context.Background(), "sys", []Turn{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("stream 2: %v", err)
	}
	if strings.Contains(body, "prompt_cache_key") || strings.Contains(body, "stream_options") {
		t.Fatalf("caching disabled but request still carries hints: %s", body)
	}
}

// ---- usage accounting ----

func TestUsageAccumulatedNonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{
			"content": []map[string]string{{"type": "text", "text": "ok"}},
			"usage":   map[string]int{"input_tokens": 10, "output_tokens": 5},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.Complete(context.Background(), "sys", "hi"); err != nil {
			t.Fatalf("complete %d: %v", i+1, err)
		}
	}
	if u := c.Usage(); u.InputTokens != 20 || u.OutputTokens != 10 {
		t.Fatalf("usage = %+v, want in=20 out=10 (accumulated over 2 calls)", u)
	}
}

func TestUsageAccumulatedOpenAIStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]string{"content": "hi"}}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		final, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{},
			"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 3},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", final)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{APIType: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.StreamTurnsWithTools(context.Background(), "", []Turn{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if u := c.Usage(); u.InputTokens != 7 || u.OutputTokens != 3 {
		t.Fatalf("usage = %+v, want in=7 out=3", u)
	}
}

func TestUsageAccumulatedAnthropicStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		start, _ := json.Marshal(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"usage": map[string]int{"input_tokens": 11, "output_tokens": 1},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", start)
		blockStart, _ := json.Marshal(map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]string{"type": "text"},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", blockStart)
		delta, _ := json.Marshal(map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]string{"type": "text_delta", "text": "你好"},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", delta)
		msgDelta, _ := json.Marshal(map[string]any{
			"type":  "message_delta",
			"delta": map[string]string{"stop_reason": "end_turn"},
			"usage": map[string]int{"output_tokens": 4},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", msgDelta)
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var got string
	resp, err := c.StreamTurnsWithTools(context.Background(), "", []Turn{{Role: "user", Content: "hi"}}, nil, func(s string) { got += s })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Text != "你好" || got != "你好" {
		t.Fatalf("text = %q / streamed %q", resp.Text, got)
	}
	if u := c.Usage(); u.InputTokens != 11 || u.OutputTokens != 4 {
		t.Fatalf("usage = %+v, want in=11 out=4 (message_start + message_delta)", u)
	}
}

func TestUsageSub(t *testing.T) {
	before := Usage{InputTokens: 10, OutputTokens: 5}
	after := Usage{InputTokens: 25, OutputTokens: 5}
	if d := after.Sub(before); d.InputTokens != 15 || d.OutputTokens != 0 {
		t.Fatalf("delta = %+v, want in=15 out=0", d)
	}
	// A reset (or shrink) clamps at zero instead of going negative.
	if d := before.Sub(after); d.InputTokens != 0 || d.OutputTokens != 0 {
		t.Fatalf("clamped delta = %+v, want zeros", d)
	}
}
