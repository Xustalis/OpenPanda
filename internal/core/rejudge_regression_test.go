// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestRegressionConcurrentRejudgesBillTheirOwnUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		v := `{"status":"done","objective_assessments":[{"dimension":"visual","criterion":0,"status":"passed","evidence":"independent render verified"}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": v}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 10}})
	}))
	defer srv.Close()
	defer unblock()
	c := newCoreWithAgentModel(t, "parallel-rejudge", config.ModelConfig{})
	c.AttachSupervisor(config.ModelConfig{BaseURL: srv.URL, APIKey: "local-test", Model: "judge"})
	a, b := parkedObjectiveTask(t, c, "same stored output"), parkedObjectiveTask(t, c, "same stored output")
	for _, task := range []Task{a, b} {
		if err := c.store.SetTokenBudget(ctx, task.TaskID, 30); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { _, _, err := c.RejudgeReviewed(ctx, a.TaskID); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, _, err := c.RejudgeReviewed(ctx, b.TaskID); err != nil {
		t.Fatal(err)
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
	for _, task := range []Task{a, b} {
		after, err := c.store.Get(ctx, task.TaskID)
		if err != nil || after.State != StateDone || after.TokenBudget != 10 {
			t.Fatalf("rejudge charged another request: %+v err=%v", after, err)
		}
		metrics, err := c.store.DelegationMetricsFor(ctx, task.TaskID)
		if err != nil || len(metrics) != 1 || metrics[0].Tokens.Int64 != 20 {
			t.Fatalf("request metrics=%+v err=%v", metrics, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("explicit rejudges need two actual requests: %d", calls.Load())
	}
}

func TestRegressionRejudgePartialEvidenceAndFreshRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		assessments := []map[string]any{{"dimension": "visual", "criterion": 0, "status": "passed", "evidence": "independent render"}}
		if n > 2 {
			assessments = append(assessments, map[string]any{"dimension": "visual", "criterion": 1, "status": "passed", "evidence": "independent interaction"})
		}
		v, _ := json.Marshal(map[string]any{"status": "done", "objective_assessments": assessments})
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": string(v)}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 10}})
	}))
	defer srv.Close()
	c := newCoreWithAgentModel(t, "rejudge-fresh", config.ModelConfig{})
	c.AttachSupervisor(config.ModelConfig{BaseURL: srv.URL, APIKey: "local-test", Model: "judge"})
	ctx := context.Background()
	task := parkedObjectiveTask(t, c, "stored output")
	spec := strings.Replace(objectiveSpec, `["tree reads as a real tree"]`, `["tree reads as a real tree","interaction works"]`, 1)
	if err := c.store.SetDetail(ctx, task.TaskID, TaskDetail{Intent: task.Intent, SpecJSON: spec}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.SetTokenBudget(ctx, task.TaskID, 1000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.RejudgeReviewed(ctx, task.TaskID); err == nil || calls.Load() != 2 {
		t.Fatalf("partial evidence must be corrected once and remain review: calls=%d err=%v", calls.Load(), err)
	}
	after, _, err := c.RejudgeReviewed(ctx, task.TaskID)
	if err != nil || after.State != StateDone || calls.Load() != 3 {
		t.Fatalf("fresh rejudge: calls=%d state=%s err=%v", calls.Load(), after.State, err)
	}
	metrics, err := c.store.DelegationMetricsFor(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	var billed int64
	for _, m := range metrics {
		billed += m.Tokens.Int64
	}
	if billed != 60 || after.TokenBudget != 940 {
		t.Fatalf("three real calls must settle once: billed=%d budget=%d", billed, after.TokenBudget)
	}
}

func TestRegressionRejudgeBudgetChecksEveryRequest(t *testing.T) {
	for _, budget := range []int64{-1, 20} {
		t.Run(fmt.Sprintf("budget=%d", budget), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": `{"status":"done","reason":"missing evidence"}`}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 10}})
			}))
			defer srv.Close()
			c := newCoreWithAgentModel(t, "rejudge-quota", config.ModelConfig{})
			c.AttachSupervisor(config.ModelConfig{BaseURL: srv.URL, APIKey: "local-test", Model: "judge"})
			ctx := context.Background()
			task := parkedObjectiveTask(t, c, "saved work")
			if err := c.store.SetTokenBudget(ctx, task.TaskID, budget); err != nil {
				t.Fatal(err)
			}
			_, _, err := c.RejudgeReviewed(ctx, task.TaskID)
			want := int32(0)
			if budget > 0 {
				want = 1
			}
			if err == nil || !strings.Contains(err.Error(), "budget exhausted") || calls.Load() != want {
				t.Fatalf("budget check: calls=%d want=%d err=%v", calls.Load(), want, err)
			}
			after, gerr := c.store.Get(ctx, task.TaskID)
			if gerr != nil || after.State != StateReview || after.TokenBudget != -1 {
				t.Fatalf("exhausted task: %+v %v", after, gerr)
			}
		})
	}
}

func TestRegressionRejudgeBillsAfterConcurrentCancellation(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithAgentModel(t, "rejudge-cancelled", config.ModelConfig{})
	task := parkedObjectiveTask(t, c, "saved work")
	if err := c.store.SetTokenBudget(ctx, task.TaskID, 1000); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := c.store.Cancel(ctx, task.TaskID); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": `{"status":"continue","reason":"unverified","followup":"inspect render"}`}}, "usage": map[string]int{"input_tokens": 100, "output_tokens": 20}})
	}))
	defer srv.Close()
	c.AttachSupervisor(config.ModelConfig{BaseURL: srv.URL, APIKey: "local-test", Model: "judge"})
	if _, _, err := c.RejudgeReviewed(ctx, task.TaskID); err == nil {
		t.Fatal("cancelled review must not update result")
	}
	after, err := c.store.Get(ctx, task.TaskID)
	if err != nil || after.State != StateCancelled || after.TokenBudget != 880 {
		t.Fatalf("cancelled request must retain its actual bill: %+v %v", after, err)
	}
	metrics, err := c.store.DelegationMetricsFor(ctx, task.TaskID)
	if err != nil || len(metrics) != 1 || metrics[0].Tokens.Int64 != 120 {
		t.Fatalf("metrics=%+v err=%v", metrics, err)
	}
}
