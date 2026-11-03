// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/intake"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func newIntakeCore(t *testing.T) *Core {
	t.Helper()
	db := openTestDB(t)
	return NewCore(db, "intake-node", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
}

func TestClaimIntakeDeduplicates(t *testing.T) {
	c := newIntakeCore(t)
	ctx := context.Background()

	first, err := c.ClaimIntake(ctx, "github", "delivery-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !first {
		t.Fatal("the first delivery must be claimed")
	}
	second, err := c.ClaimIntake(ctx, "github", "delivery-1")
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if second {
		t.Fatal("a replayed delivery must not be claimed twice")
	}
	// The key is scoped by source: another source may reuse the same id.
	other, err := c.ClaimIntake(ctx, "other", "delivery-1")
	if err != nil {
		t.Fatalf("other source: %v", err)
	}
	if !other {
		t.Fatal("a different source must not be deduplicated against github")
	}
	// A source that sends no delivery id never retries; nothing to dedupe.
	for i := 0; i < 2; i++ {
		ok, err := c.ClaimIntake(ctx, "github", "")
		if err != nil || !ok {
			t.Fatalf("empty key claim %d = %v, %v", i, ok, err)
		}
	}
}

func TestIntakeTaskQueuesWork(t *testing.T) {
	c := newIntakeCore(t)
	ctx := context.Background()
	ev := intake.Event{
		Source: "github", Delivery: "d1", Kind: intake.KindIssueComment,
		Repo: "acme/widgets", Number: 42, Thread: "acme/widgets#42", Actor: "alice",
		Title: "Crash on save", Body: "@panda-bot please fix",
		URL: "https://github.com/acme/widgets/issues/42",
	}
	id, err := c.IntakeTask(ctx, ev, "", PriorityHigh)
	if err != nil {
		t.Fatalf("IntakeTask: %v", err)
	}
	if id == "" {
		t.Fatal("IntakeTask returned an empty task id")
	}
	task, err := c.store.Get(ctx, id)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if task.State != StateQueued {
		t.Fatalf("state = %q, want queued", task.State)
	}
	if task.Title != "Crash on save" {
		t.Fatalf("title = %q", task.Title)
	}
	if !strings.Contains(task.Intent, "@panda-bot please fix") || !strings.Contains(task.Intent, "acme/widgets#42") {
		t.Fatalf("intent lost the request: %q", task.Intent)
	}
	if !strings.Contains(task.SpecJSON, "https://github.com/acme/widgets/issues/42") ||
		!strings.Contains(task.SpecJSON, `"source":"github"`) ||
		!strings.Contains(task.SpecJSON, `"source_number":"42"`) ||
		!strings.Contains(task.SpecJSON, `"source_repo":"acme/widgets"`) {
		t.Fatalf("spec lost the provenance: %q", task.SpecJSON)
	}
	if task.Priority != PriorityHigh {
		t.Fatalf("priority = %d, want %d", task.Priority, PriorityHigh)
	}
}

func TestIntakeTaskDefaultsPriority(t *testing.T) {
	c := newIntakeCore(t)
	id, err := c.IntakeTask(context.Background(), intake.Event{Title: "x"}, "", 99)
	if err != nil {
		t.Fatalf("IntakeTask: %v", err)
	}
	task, err := c.store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if task.Priority != PriorityNormal {
		t.Fatalf("priority = %d, want normal", task.Priority)
	}
}

func TestSourceFactsReachHooks(t *testing.T) {
	// The spec intake writes is what a reply hook reads; the two must agree on
	// the key names, so this pins the round trip rather than either side alone.
	spec := intakeSpec(intake.Event{
		Source: "github", Repo: "acme/widgets", Number: 42,
		Thread: "acme/widgets#42", Actor: "alice",
		URL: "https://github.com/acme/widgets/issues/42",
	})
	facts := sourceFactsOf(Task{SpecJSON: spec})
	if facts.Source != "github" || facts.Repo != "acme/widgets" || facts.Number != "42" ||
		facts.Thread != "acme/widgets#42" ||
		facts.URL != "https://github.com/acme/widgets/issues/42" {
		t.Fatalf("spec → hook facts lost data: %+v", facts)
	}
	if (sourceFactsOf(Task{})).Source != "" {
		t.Fatal("a task with no spec must carry no source facts")
	}
}
