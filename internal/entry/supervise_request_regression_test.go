// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestRegressionSuperviseBypassDoesNotDisableConcurrentCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, `{"content":[{"type":"text","text":"{\"status\":\"done\",\"reason\":\"verified\"}"}],"usage":{"input_tokens":7,"output_tokens":3}}`)
	}))
	defer srv.Close()
	defer unblock()
	c, err := NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "local-test", Model: "judge"})
	if err != nil {
		t.Fatal(err)
	}
	c.SetDiskCache(NewDiskCache(newCacheDB(t)))
	var reported atomic.Int64
	opts := SuperviseOptions{Contract: "contract-v1/final", AfterCall: func(u Usage, err error, d time.Duration) { reported.Add(u.Total()) }}
	if _, err := Supervise(ctx, c, "intent", "result", opts); err != nil {
		t.Fatal(err)
	}
	bypass := opts
	bypass.BypassCache = true
	done := make(chan error, 1)
	go func() { _, err := Supervise(ctx, c, "intent", "result", bypass); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := Supervise(ctx, c, "intent", "result", opts); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || reported.Load() != 10 {
		t.Fatalf("concurrent cache hit made a request/bill: calls=%d usage=%d", calls.Load(), reported.Load())
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, contract := range []string{"contract-v2/final", "contract-v2/intermediate"} {
		opts.Contract = contract
		if _, err := Supervise(ctx, c, "intent", "result", opts); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 4 || reported.Load() != 40 || c.Usage().Total() != 40 {
		t.Fatalf("cache scope/usage: calls=%d reported=%d total=%d", calls.Load(), reported.Load(), c.Usage().Total())
	}
}

func TestRegressionCompleteUsageAllDialectsAndProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		name, api, body string
		cached, created int64
		fail            bool
	}{
		{"anthropic", config.APITypeAnthropic, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":7,"cache_read_input_tokens":3,"cache_creation_input_tokens":2,"output_tokens":5}}`, 3, 2, false},
		{"openai", config.APITypeOpenAI, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`, 3, 0, false},
		{"responses", config.APITypeResponses, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":12,"output_tokens":5}}`, 0, 0, false},
		{"google", config.APITypeGoogle, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"thoughtsTokenCount":2}}`, 0, 0, false},
		{"provider error with usage", config.APITypeAnthropic, `{"error":{"type":"invalid_request_error","message":"refused"},"usage":{"input_tokens":12,"output_tokens":5}}`, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			c, err := NewClient(config.ModelConfig{APIType: tc.api, BaseURL: srv.URL, APIKey: "local-test", Model: "local-model"})
			if err != nil {
				t.Fatal(err)
			}
			c.maxRetry = 0
			for i := 0; i < 2; i++ {
				_, u, err := c.CompleteUsage(context.Background(), "system", "question")
				if (err != nil) != tc.fail || u.InputTokens != 12 || u.OutputTokens != 5 || u.CachedInputTokens != tc.cached || u.CacheCreationTokens != tc.created {
					t.Fatalf("request usage=%+v error=%v", u, err)
				}
			}
			if c.Usage().Total() != 34 {
				t.Fatalf("global usage=%+v", c.Usage())
			}
		})
	}
}
