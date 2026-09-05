package entry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestSummarizeResultDiskCache(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"任务成功完成"}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(config.ModelConfig{
		APIType: config.APITypeAnthropic,
		BaseURL: srv.URL,
		APIKey:  "sk-test",
		Model:   "test-model",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	dc := NewDiskCache(newCacheDB(t))
	c.SetDiskCache(dc)

	ctx := context.Background()
	// First call -> hits LLM
	sum1, err := SummarizeResult(ctx, c, "构建任务", "执行构建", true, 0, "build success", "")
	if err != nil {
		t.Fatalf("SummarizeResult 1: %v", err)
	}
	if sum1 != "任务成功完成" {
		t.Fatalf("unexpected summary: %q", sum1)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call to LLM, got %d", calls.Load())
	}

	// Second identical call -> hits DiskCache, skips LLM
	sum2, err := SummarizeResult(ctx, c, "构建任务", "执行构建", true, 0, "build success", "")
	if err != nil {
		t.Fatalf("SummarizeResult 2: %v", err)
	}
	if sum2 != "任务成功完成" {
		t.Fatalf("unexpected cached summary: %q", sum2)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected DiskCache hit to skip LLM, but calls = %d", calls.Load())
	}

	// Third call with different output -> misses DiskCache, calls LLM
	_, err = SummarizeResult(ctx, c, "构建任务", "执行构建", false, 1, "build failed", "error log")
	if err != nil {
		t.Fatalf("SummarizeResult 3: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected DiskCache miss to call LLM, but calls = %d", calls.Load())
	}
}
