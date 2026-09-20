package entry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
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

func TestSummarizeResult_MultiLanguage(t *testing.T) {
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		lastBody = string(buf[:n])
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Done"}]}`))
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

	ctx := context.Background()

	// 1. English Locale
	_, err = SummarizeResult(ctx, c, "Build task", "Run build", true, 0, "ok", "", i18n.English)
	if err != nil {
		t.Fatalf("SummarizeResult EN: %v", err)
	}
	if !strings.Contains(lastBody, "You are the dispatch result reporter") {
		t.Errorf("expected English system prompt in body, got: %s", lastBody)
	}
	if !strings.Contains(lastBody, "Task Title: Build task") {
		t.Errorf("expected English task title in body, got: %s", lastBody)
	}

	// 2. Chinese Locale
	_, err = SummarizeResult(ctx, c, "构建任务", "运行构建", true, 0, "ok", "", i18n.ChineseSimp)
	if err != nil {
		t.Fatalf("SummarizeResult ZH: %v", err)
	}
	if !strings.Contains(lastBody, "你是调度结果汇报员") {
		t.Errorf("expected Chinese system prompt in body, got: %s", lastBody)
	}
	if !strings.Contains(lastBody, "任务标题：构建任务") {
		t.Errorf("expected Chinese task title in body, got: %s", lastBody)
	}
}
