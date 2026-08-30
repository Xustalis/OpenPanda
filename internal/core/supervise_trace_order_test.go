package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// TestSupervisionRoundTracedAfterJudge: the supervision_round trace event is
// what surfaces as the "reviewing result…" stage in the CLI task card and the
// web orbit, and a stage's on-screen duration is measured until the NEXT
// stage's event lands. If the event fires right after Execute returns — while
// the judge call has not even started — the reviewing stage absorbs the
// agent's entire runtime and the executing stage reads "0s", which is exactly
// the misattribution users were shown. The event must therefore land after
// both the agent run and the judge call have finished, carrying the verdict
// that actually came out of the round.
func TestSupervisionRoundTracedAfterJudge(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "sup-trace", 1)
	c.SetWorkDir(t.TempDir())

	const step = 30 * time.Millisecond
	var execDone, judgeStart, judgeEnd, eventAt time.Time
	var judgeCalled atomic.Bool

	// The judge endpoint: its handler IS the judging work, so leaving it
	// marks the earliest moment a verdict exists.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		judgeCalled.Store(true)
		time.Sleep(step)
		w.Header().Set("content-type", "application/json")
		resp := map[string]any{"content": []map[string]string{{"type": "text", "text": `{"status":"done","reason":"ok","followup":""}`}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
		judgeEnd = time.Now()
	}))
	t.Cleanup(srv.Close)
	supervisor, err := entry.NewClient(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("new supervisor client: %v", err)
	}
	c.SetSupervisor(supervisor)

	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		time.Sleep(step)
		execDone = time.Now()
		return commander.AgentResult{OK: true, Result: "done", ExitCode: 0}
	})

	store := c.TaskStore()
	store.SetOnEvent(func(_, typ string, data any) {
		switch typ {
		case EvSupervisionRound:
			eventAt = time.Now()
		case EvJudgeStart:
			judgeStart = time.Now()
		}
	})
	t.Cleanup(func() { store.SetOnEvent(nil) })

	if _, _, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "trace order",
		Project:     "proj",
		ContextType: "command",
		Intent:      "do the thing",
		Requires:    []string{"code:modify"},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if eventAt.IsZero() {
		t.Fatal("no supervision_round trace event was recorded")
	}
	if !judgeCalled.Load() {
		t.Fatal("the judge was never consulted — nothing to attribute the round to")
	}
	// The event must land after the judge finished: any earlier and the
	// reviewing stage's stopwatch starts before the review exists.
	if eventAt.Before(judgeEnd) {
		t.Fatalf("supervision_round traced %v before the judge finished: the reviewing stage would absorb the agent's runtime", judgeEnd.Sub(eventAt))
	}
	if judgeEnd.Before(execDone) {
		t.Fatalf("test harness ordering broken: judge ended before the agent run")
	}
	// The reviewing stage's stopwatch starts when the judge starts, which is
	// after the agent run finished and before the verdict came back. Without
	// this marker the judge's runtime gets billed to the executing stage.
	if judgeStart.IsZero() {
		t.Fatal("no judge_start trace event was recorded")
	}
	if judgeStart.Before(execDone) {
		t.Fatalf("judge_start traced %v before the agent run finished", execDone.Sub(judgeStart))
	}
	if judgeStart.After(judgeEnd) {
		t.Fatalf("judge_start traced %v after the judge finished", judgeStart.Sub(judgeEnd))
	}
}
