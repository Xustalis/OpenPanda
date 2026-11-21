// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestRegressionConcurrentRecoveryUsageIsPerCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startedA := make(chan struct{})
	releaseA := make(chan struct{})
	var calls atomic.Int32
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseA) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(startedA)
			select {
			case <-releaseA:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": `{"status":"continue","reason":"recoverable error","followup":"inspect existing files and repair"}`}},
			"usage":   map[string]int{"input_tokens": 10, "output_tokens": 10},
		})
	}))
	defer srv.Close()
	defer unblock()
	client, err := entry.NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "local-test-key", Model: "local-usage-probe"})
	if err != nil {
		t.Fatal(err)
	}
	c := newCore(t, "parallel-billing-probe")
	a, err := c.store.Create(ctx, "", "p", "A", "node", []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.store.Create(ctx, "", "p", "B", "node", []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.store.SetTokenBudget(ctx, a.TaskID, 30); err != nil {
		t.Fatal(err)
	}
	if err = c.store.SetTokenBudget(ctx, b.TaskID, 1000); err != nil {
		t.Fatal(err)
	}
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		c.superviseAgentFailure(ctx, client, a, "recover A", commander.Result{Agent: "fake", ExitCode: 1, Stderr: "failed A"}, nil, "", 1, 2)
	}()
	select {
	case <-startedA:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	c.superviseAgentFailure(ctx, client, b, "recover B", commander.Result{Agent: "fake", ExitCode: 1, Stderr: "failed B"}, nil, "", 1, 2)
	unblock()
	select {
	case <-doneA:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	metrics, err := c.store.ListDelegationMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	billed := int64(0)
	perTask := map[string]int64{}
	for _, m := range metrics {
		billed += m.Tokens.Int64
		perTask[m.TaskID] += m.Tokens.Int64
	}
	afterA, err := c.store.Get(ctx, a.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	actual := client.Usage().Total()
	t.Logf("calls=%d provider_usage=%d billed_total=%d billed_A=%d billed_B=%d budget_A=%d", calls.Load(), actual, billed, perTask[a.TaskID], perTask[b.TaskID], afterA.TokenBudget)
	if actual != 40 {
		t.Fatalf("fixture must report 40 total tokens: %d", actual)
	}
	if billed != actual || perTask[a.TaskID] != 20 || perTask[b.TaskID] != 20 || afterA.TokenBudget != 10 {
		t.Errorf("overlapping calls charge other task's usage and exhaust valid budget")
	}
}

func TestRegressionRejudgeRecordsAndBillsUsage(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": `{"status":"continue","reason":"render evidence is missing","followup":"capture an independent render"}`}},
			"usage":   map[string]int{"input_tokens": 100, "output_tokens": 20},
		})
	}))
	defer srv.Close()
	c := newCoreWithAgentModel(t, "rejudge-usage-probe", config.ModelConfig{})
	c.AttachSupervisor(config.ModelConfig{BaseURL: srv.URL, APIKey: "local-test-key", Model: "local-usage-probe"})
	task := parkedObjectiveTask(t, c, "stored output awaiting MAX evidence")
	if err := c.store.SetTokenBudget(ctx, task.TaskID, 1000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.RejudgeReviewed(ctx, task.TaskID); err == nil {
		t.Fatal("fixture must remain in review")
	}
	_, _, client := c.executionSnapshot()
	metrics, err := c.store.DelegationMetricsFor(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	billed := int64(0)
	for _, m := range metrics {
		billed += m.Tokens.Int64
	}
	after, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("provider_usage=%d recorded_tokens=%d budget_before=1000 budget_after=%d", client.Usage().Total(), billed, after.TokenBudget)
	if client.Usage().Total() != 120 {
		t.Fatal("fixture usage missing")
	}
	if billed != 120 || after.TokenBudget != 880 {
		t.Errorf("rejudge model call is neither recorded nor charged to the task")
	}

}

func TestRegressionControlSerialRecoveryUsageIsPerCall(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": `{"status":"continue","reason":"recoverable error","followup":"inspect existing files and repair"}`}},
			"usage":   map[string]int{"input_tokens": 10, "output_tokens": 10},
		})
	}))
	defer srv.Close()
	client, err := entry.NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "local-test-key", Model: "serial-usage-probe"})
	if err != nil {
		t.Fatal(err)
	}
	c := newCore(t, "serial-billing-probe")
	for _, title := range []string{"serial A", "serial B"} {
		task, err := c.store.Create(ctx, "", "p", title, "node", []string{"node"})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.store.SetTokenBudget(ctx, task.TaskID, 30); err != nil {
			t.Fatal(err)
		}
		c.superviseAgentFailure(ctx, client, task, title, commander.Result{Agent: "fake", ExitCode: 1, Stderr: "failed"}, nil, "", 1, 2)
		after, err := c.store.Get(ctx, task.TaskID)
		if err != nil || after.TokenBudget != 10 {
			t.Fatalf("serial control %s: budget=%d error=%v", title, after.TokenBudget, err)
		}
		metrics, err := c.store.DelegationMetricsFor(ctx, task.TaskID)
		if err != nil || len(metrics) != 1 || metrics[0].Tokens.Int64 != 20 {
			t.Fatalf("serial control metrics=%+v error=%v", metrics, err)
		}
	}
	if client.Usage().Total() != 40 {
		t.Fatal("serial provider usage must be 40")
	}
	t.Log("serial control: actual=40 recorded=40 budgets=10/10")
}
