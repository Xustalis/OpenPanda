// SPDX-License-Identifier: AGPL-3.0-or-later

package conversation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestDeepConcurrentProposalConfirmation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "confirm.db")
	s := &Store{DB: testdb.OpenFile(t, path)}
	if err := s.SetPlanning(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	v, err := s.SaveProposal(ctx, "session", "implement exactly this immutable proposal")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var wins atomic.Int32
	for i := 0; i < 16; i++ {
		db, err := storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			text, err := (&Store{DB: db}).Confirm(ctx, "session", v)
			if err == nil {
				wins.Add(1)
				if text != "implement exactly this immutable proposal" {
					t.Errorf("wrong proposal %q", text)
				}
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("confirm: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM conversation_plan_runs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if wins.Load() != 1 || count != 1 {
		t.Fatalf("wins=%d runs=%d", wins.Load(), count)
	}
}

func TestDeepRevokedCallbacksKeepHistoryWithoutRevivingGoal(t *testing.T) {
	for _, action := range []string{"stop", "edit", "clear", "replace", "delete"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			s := &Store{DB: testdb.Open(t)}
			old, err := s.Create(ctx, "session", "full old objective", nil)
			if err != nil {
				t.Fatal(err)
			}
			old.RoundID, err = s.Claim(ctx, old, "old-owner", "en")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Finish(ctx, old.RoundID, 11); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "replace":
				_, err = s.Create(ctx, "session", "entirely new objective", nil)
			case "delete":
				err = s.DeleteSession(ctx, "session")
			default:
				err = s.Control(ctx, "session", old.ID, action, "changed complete objective", nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Renew(ctx, old, "old-owner"); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale renewal: %v", err)
			}
			if err = s.Report(ctx, old, "blocked", "stale blocker"); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale report: %v", err)
			}
			if err = s.Hold(ctx, old, "active", "stale callback", false); err != nil {
				t.Fatal(err)
			}
			if err = core.NewTaskStore(s.DB, nil).CloseAskRound(ctx, old.RoundID, "stopped"); err != nil {
				t.Fatal(err)
			}
			if err = s.Settle(ctx, old, 7); err != nil {
				t.Fatal(err)
			}
			if err = s.Settle(ctx, old, 7); !errors.Is(err, ErrConflict) {
				t.Fatalf("duplicate: %v", err)
			}
			var tokens int64
			var status, reason string
			if err = s.DB.QueryRow("SELECT tokens,status,reason FROM conversation_goals WHERE id=?", old.ID).Scan(&tokens, &status, &reason); err != nil {
				t.Fatal(err)
			}
			if tokens != 18 || status == "complete" || reason == "stale callback" {
				t.Fatalf("tokens=%d status=%s reason=%s", tokens, status, reason)
			}
			state, err := s.Get(ctx, "session")
			if err != nil {
				t.Fatal(err)
			}
			if action == "replace" && (state.Goal.ID == old.ID || state.Goal.Tokens != 0 || state.Goal.Rounds != 0) {
				t.Fatalf("old usage contaminated replacement: %+v", state.Goal)
			}
			if (action == "delete" || action == "clear") && state.Goal != nil {
				t.Fatalf("revived goal: %+v", state.Goal)
			}
		})
	}
}

func TestDeepBlockerStreakBreaksOnProgressAndDifferentReason(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	g, err := s.Create(ctx, "session", "complete objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	sequence := []string{"credential A", "credential A", "", "credential A", "credential B", "credential B", "credential B"}
	expected := []int{1, 2, 0, 1, 1, 2, 3}
	for i, reason := range sequence {
		g.RoundID, err = s.Claim(ctx, g, "owner", "en")
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if reason != "" {
			for n := 0; n < 3; n++ {
				if err = s.Report(ctx, g, "blocked", reason); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err = core.NewTaskStore(s.DB, nil).CloseAskRound(ctx, g.RoundID, "closed"); err != nil {
			t.Fatal(err)
		}
		if err = s.Settle(ctx, g, 0); err != nil {
			t.Fatal(err)
		}
		st, err := s.Get(ctx, "session")
		if err != nil {
			t.Fatal(err)
		}
		g = st.Goal
		if g.BlockedStreak != expected[i] || (g.Status == "blocked") != (i == len(sequence)-1) {
			t.Fatalf("round %d: %+v", i, g)
		}
	}
}

func TestDeepResumeCannotStrandExhaustedBudgetAsActive(t *testing.T) {
	for _, replacement := range []int64{9, 10, 11} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			ctx := context.Background()
			s := &Store{DB: testdb.Open(t)}
			budget := int64(10)
			g, err := s.Create(ctx, "session", "complete objective", &budget)
			if err != nil {
				t.Fatal(err)
			}
			g.RoundID, err = s.Claim(ctx, g, "owner", "en")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Finish(ctx, g.RoundID, 10); err != nil {
				t.Fatal(err)
			}
			if err = core.NewTaskStore(s.DB, nil).CloseAskRound(ctx, g.RoundID, "closed"); err != nil {
				t.Fatal(err)
			}
			if err = s.Settle(ctx, g, 0); err != nil {
				t.Fatal(err)
			}
			err = s.Control(ctx, "session", g.ID, "resume", "", &replacement)
			if replacement <= 10 {
				if err == nil {
					t.Fatal("resume accepted exhausted budget and left an active goal that cannot claim a round")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			st, err := s.Get(ctx, "session")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Claim(ctx, st.Goal, "new-owner", "en"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeepLaterBlockerInvalidatesEarlierCompletionAudit(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	g, err := s.Create(ctx, "session", "verify every requirement", nil)
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID, err = s.Claim(ctx, g, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Report(ctx, g, "complete", "initial verification looked complete"); err != nil {
		t.Fatal(err)
	}
	if err = s.Report(ctx, g, "blocked", "final check found a missing required credential"); err != nil {
		t.Fatal(err)
	}
	if err = core.NewTaskStore(s.DB, nil).CloseAskRound(ctx, g.RoundID, "closed"); err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(ctx, g, 0); err != nil {
		t.Fatal(err)
	}
	st, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if st.Goal.Status == "complete" || st.Goal.CompletionRequested {
		t.Fatalf("later blocker did not revoke the audit: %+v", st.Goal)
	}
}

func TestDeepLateUsageAfterRevocationSettlementIsChargedOnce(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	budget := int64(20)
	old, err := s.Create(ctx, "session", "old full objective", &budget)
	if err != nil {
		t.Fatal(err)
	}
	old.RoundID, err = s.Claim(ctx, old, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, old.RoundID, 11); err != nil {
		t.Fatal(err)
	}
	// Stop seals the round before the canceled model request's finalizer runs.
	if err = s.Control(ctx, "session", old.ID, "clear", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = core.NewTaskStore(s.DB, nil).CloseAskRound(ctx, old.RoundID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(ctx, old, 7); err != nil {
		t.Fatal(err)
	}
	replacement, err := s.Create(ctx, "session", "new unrelated goal", nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Finish(ctx, old.RoundID, int64(12+i)); err != nil {
				t.Errorf("late finish: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if err = s.Finish(ctx, old.RoundID, 11); err != nil {
		t.Fatal(err)
	}
	var billed, ledger int64
	var status string
	if err = s.DB.QueryRow("SELECT g.tokens,r.entry_tokens+r.task_tokens,g.status FROM conversation_goals g JOIN conversation_goal_rounds r ON r.goal_id=g.id WHERE r.round_id=?", old.RoundID).Scan(&billed, &ledger, &status); err != nil {
		t.Fatal(err)
	}
	if billed != 30 || ledger != 30 || status != "cleared" {
		t.Fatalf("late usage missing or duplicated: billed=%d ledger=%d status=%s", billed, ledger, status)
	}
	state, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.ID != replacement.ID || state.Goal.Tokens != 0 || state.Goal.Status != "active" {
		t.Fatalf("late usage affected replacement: %+v", state.Goal)
	}
}

func TestDeepSettlementRechecksTaskUsageInsideTransaction(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	g, err := s.Create(ctx, "session", "all original work", nil)
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID, err = s.Claim(ctx, g, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	tasks := core.NewTaskStore(s.DB, nil)
	task, err := tasks.Create(core.WithAskRound(ctx, g.RoundID), "", "", "settlement race", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tasks.Cancel(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	// Another controller read zero before this metric committed.
	if err = tasks.RecordDelegationMetric(ctx, task.TaskID, "fixture", "executor", nil, true, 1, 13); err != nil {
		t.Fatal(err)
	}
	if err = tasks.CloseAskRound(ctx, g.RoundID, "closed"); err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(ctx, g, 0); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if state.Goal.Tokens != 13 {
		t.Fatalf("stale pre-transaction usage discarded a committed metric: %+v", state.Goal)
	}
}
