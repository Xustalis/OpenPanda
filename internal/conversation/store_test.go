// SPDX-License-Identifier: AGPL-3.0-or-later

package conversation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestGoalAndInteractiveRoundClaimsExcludeEachOther(t *testing.T) {
	s := &Store{DB: testdb.Open(t)}
	ctx := context.Background()
	tasks := core.NewTaskStore(s.DB, nil)
	g, err := s.Create(ctx, "session", "full objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tasks.BeginAskRound(ctx, "interactive", "session", "en"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, g, "owner", "en"); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim while user owns session: %v", err)
	}
	if err = tasks.CloseAskRound(ctx, "interactive", "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, g, "owner", "en"); err != nil {
		t.Fatal(err)
	}
	if err = tasks.BeginAskRound(ctx, "late-user", "session", "en"); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("user claim while controller owns session: %v", err)
	}
}

func TestGoalClaimAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	if path := os.Getenv("PANDA_GOAL_CLAIM_FIXTURE"); path != "" {
		db, err := storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		s := &Store{DB: db}
		state, err := s.Get(ctx, "process-fixture")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Claim(ctx, state.Goal, fmt.Sprint(os.Getpid()), "en"); err == nil {
			fmt.Println("CLAIM_WON")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "process.db")
	s := &Store{DB: testdb.OpenFile(t, path)}
	if _, err := s.Create(ctx, "process-fixture", "one round across independent processes", nil); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(executable, "-test.run=^TestGoalClaimAcrossProcesses$", "-test.count=1")
			cmd.Env = append(os.Environ(), "PANDA_GOAL_CLAIM_FIXTURE="+path)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("claim subprocess: %v %s", err, output)
				return
			}
			if strings.Contains(string(output), "CLAIM_WON") {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM conversation_goal_rounds").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if wins.Load() != 1 || count != 1 {
		t.Fatalf("wins=%d rounds=%d", wins.Load(), count)
	}
}

func TestPlanningRejectsStaleProposalProducer(t *testing.T) {
	s := &Store{DB: testdb.Open(t)}
	ctx := context.Background()
	if err := s.SetPlanning(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProposal(ctx, "session", "newer result", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProposal(ctx, "session", "late older result", 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale result: %v", err)
	}
	state, _ := s.Get(ctx, "session")
	if state.Proposal != "newer result" {
		t.Fatalf("proposal overwritten: %+v", state)
	}
	oldGeneration := state.ModeGeneration
	if err := s.SetPlanning(ctx, "session", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanning(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProposal(ctx, "session", "old planning request", state.ProposalVersion, oldGeneration); !errors.Is(err, ErrConflict) {
		t.Fatalf("old mode producer accepted: %v", err)
	}
	if _, err := s.Confirm(ctx, "session", state.ProposalVersion); !errors.Is(err, ErrConflict) {
		t.Fatalf("proposal from a previous planning mode accepted: %v", err)
	}
	if _, err := s.SaveProposal(ctx, "session", "new mode proposal"); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	version, generation, err := s.BeginPlanningRequest(ctx, "session")
	if err != nil || version != state.ProposalVersion || generation <= state.ModeGeneration {
		t.Fatalf("new planning request: version=%d generation=%d error=%v", version, generation, err)
	}
	if _, err = s.Confirm(ctx, "session", version); !errors.Is(err, ErrConflict) {
		t.Fatalf("proposal from a superseded request accepted: %v", err)
	}
	if _, err = s.SaveProposal(ctx, "session", "late older result", version, state.ModeGeneration); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded producer published: %v", err)
	}
	if v, err := s.SaveProposal(ctx, "session", "latest request proposal", version, generation); err != nil || v != version+1 {
		t.Fatalf("current producer failed: version=%d error=%v", v, err)
	}
	var old string
	if err := s.DB.QueryRow("SELECT proposal FROM conversation_proposals WHERE session_id='session' AND version=1").Scan(&old); err != nil || old != "newer result" {
		t.Fatalf("immutable proposal lost: %q %v", old, err)
	}
}

func TestPlanVersionsAndGoalPause(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	g, err := s.Create(ctx, "session", "complete original objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetPlanning(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Get(ctx, "session")
	if !st.Planning || st.Goal.Status != "paused" || !st.Goal.PausedByPlan {
		t.Fatalf("%+v", st)
	}
	one, err := s.SaveProposal(ctx, "session", "version one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.SaveProposal(ctx, "session", "version two")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Confirm(ctx, "session", one); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale proposal accepted: %v", err)
	}
	text, err := s.Confirm(ctx, "session", two)
	if err != nil || text != "version two" {
		t.Fatalf("%q %v", text, err)
	}
	if _, err = s.Confirm(ctx, "session", two); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate confirmation accepted")
	}
	st, _ = s.Get(ctx, "session")
	if st.Planning || st.Proposal != "" || st.Goal.Status != "paused" || st.Goal.ID != g.ID {
		t.Fatalf("confirmation resumed before implementation: %+v", st)
	}
	if _, err = s.Claim(ctx, g, "owner", "en"); !errors.Is(err, ErrConflict) {
		t.Fatalf("goal ran ahead of implementation: %v", err)
	}
}

func TestEditingCompletedGoalInvalidatesAuditAndRequiresResume(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	g, err := s.Create(ctx, "session", "complete original objective", nil)
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
	if err = s.Report(ctx, g, "complete", "Verified the entire original objective with concrete evidence"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, "UPDATE ask_rounds SET status='closed' WHERE round_id=?", g.RoundID); err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(ctx, g, 0); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(ctx, "session")
	if err != nil || before.Goal.Status != "complete" {
		t.Fatalf("fixture did not complete: %+v %v", before, err)
	}
	if err = s.Control(ctx, "session", g.ID, "edit", "complete revised objective", nil); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if after.Goal.ID != g.ID || after.Goal.Status != "paused" || after.Goal.CompletionRequested || after.Goal.Generation != g.Generation+1 || after.Goal.Tokens != 10 {
		t.Fatalf("edited objective retained stale completion: %+v", after.Goal)
	}
	if err = s.Report(ctx, g, "complete", "Stale audit for the previous objective"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old audit accepted: %v", err)
	}
	if _, err = s.Claim(ctx, after.Goal, "owner", "en"); !errors.Is(err, ErrConflict) {
		t.Fatalf("editing implicitly resumed: %v", err)
	}
	if err = s.Control(ctx, "session", g.ID, "resume", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, after.Goal, "owner", "en"); err != nil {
		t.Fatalf("explicit resume did not allow revised objective: %v", err)
	}
}

func TestExplicitGoalResumeLeavesPlanningAndFencesOldProposal(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	g, err := s.Create(ctx, "session", "complete original objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetPlanning(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Control(ctx, "session", g.ID, "resume", "", nil); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if after.Planning || after.Goal.Status != "active" || after.Goal.PausedByPlan || after.ModeGeneration != before.ModeGeneration+1 || after.Goal.Objective != g.Objective {
		t.Fatalf("resume did not atomically leave Plan: %+v", after)
	}
	if _, err = s.SaveProposal(ctx, "session", "stale proposal", before.ProposalVersion, before.ModeGeneration); !errors.Is(err, ErrConflict) {
		t.Fatalf("old planning request published after resume: %v", err)
	}
	if _, err = s.Claim(ctx, after.Goal, "owner", "en"); err != nil {
		t.Fatalf("explicit resume did not restore runtime eligibility: %v", err)
	}
}

func TestGoalClaimAcrossDatabaseConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db := testdb.OpenFile(t, path)
	other, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a, b := &Store{DB: db}, &Store{DB: other}
	g, err := a.Create(ctx, "session", "same full goal", nil)
	if err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			if _, err := s.Claim(ctx, g, "process", "en"); err == nil {
				starts.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if starts.Load() != 1 {
		t.Fatalf("started %d rounds", starts.Load())
	}
	var rounds int
	if db.QueryRow("SELECT count(*) FROM ask_rounds").Scan(&rounds) != nil || rounds != 1 {
		t.Fatalf("round rows %d", rounds)
	}
}

func TestGoalBlockerThreeConsecutiveRoundsAndResume(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	g, err := s.Create(ctx, "session", "full original objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		round, err := s.Claim(ctx, g, "owner", "en")
		if err != nil {
			t.Fatal(err)
		}
		g.RoundID = round
		if err = s.Report(ctx, g, "blocked", "missing required user credential"); err != nil {
			t.Fatal(err)
		}
		if err = s.Report(ctx, g, "blocked", "missing required user credential"); err != nil {
			t.Fatal(err)
		}
		_, _ = s.DB.Exec("UPDATE ask_rounds SET status='closed' WHERE round_id=?", g.RoundID)
		if err = s.Settle(ctx, g, 0); err != nil {
			t.Fatal(err)
		}
		st, _ := s.Get(ctx, "session")
		g = st.Goal
		if g.BlockedStreak != i || (g.Status == "blocked") != (i == 3) {
			t.Fatalf("round %d: %+v", i, g)
		}
	}
	if err = s.Control(ctx, "session", g.ID, "resume", "", nil); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Get(ctx, "session")
	if st.Goal.BlockedStreak != 0 || st.Goal.Status != "active" {
		t.Fatalf("resume: %+v", st.Goal)
	}
}

func TestGoalAuditUsageIdempotencyBudgetAndStaleCallbacks(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	budget := int64(100)
	g, err := s.Create(ctx, "session", "audit all requirements", &budget)
	if err != nil {
		t.Fatal(err)
	}
	round, err := s.Claim(ctx, g, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID = round
	if err = s.Finish(ctx, round, 30); err != nil {
		t.Fatal(err)
	}
	_ = s.Finish(ctx, round, 30)
	if err = s.Report(ctx, g, "complete", "verified every original requirement with concrete evidence"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Get(ctx, "session")
	if st.Goal.Status != "active" {
		t.Fatal("completed before settlement")
	}
	if err = s.Control(ctx, "session", g.ID, "clear", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Report(ctx, g, "complete", "late callback"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale report: %v", err)
	}
	if err = s.Settle(ctx, g, 20); err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(ctx, g, 20); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate settlement: %v", err)
	}
	var status string
	var tokens int64
	if err = s.DB.QueryRow("SELECT status,tokens FROM conversation_goals WHERE id=?", g.ID).Scan(&status, &tokens); err != nil {
		t.Fatal(err)
	}
	if status != "cleared" || tokens != 50 {
		t.Fatalf("status=%s tokens=%d", status, tokens)
	}
	_, _ = s.DB.Exec("UPDATE ask_rounds SET status='closed' WHERE round_id=?", round)
	next, err := s.Create(ctx, "session", "new goal", &budget)
	if err != nil {
		t.Fatal(err)
	}
	round, err = s.Claim(ctx, next, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	next.RoundID = round
	_ = s.Finish(ctx, round, 100)
	if err = s.Settle(ctx, next, 0); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Get(ctx, "session")
	if st.Goal.Status != "budget_limited" {
		t.Fatalf("%+v", st.Goal)
	}
	if err = s.Control(ctx, "session", next.ID, "resume", "", nil); err == nil {
		t.Fatal("budget increased implicitly")
	}
}

func TestGoalDeletionAndForkDefaults(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	_, err := s.Create(ctx, "source", "original", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetPlanning(ctx, "source", true)
	fork, err := s.Get(ctx, "fork")
	if err != nil || fork.Goal != nil || fork.Planning {
		t.Fatalf("fork inherited active control: %+v %v", fork, err)
	}
	if err = s.DeleteSession(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, "source", "late recreation", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("resurrected deleted session: %v", err)
	}
}

func TestRevokedGoalDetachesRoundButRetainsLedger(t *testing.T) {
	for _, action := range []string{"clear", "delete", "replace"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			s := &Store{DB: testdb.Open(t)}
			g, err := s.Create(ctx, "session", "original objective", nil)
			if err != nil {
				t.Fatal(err)
			}
			g.RoundID, err = s.Claim(ctx, g, "owner", "en")
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "clear":
				err = s.Control(ctx, g.SessionID, g.ID, "clear", "", nil)
			case "delete":
				err = s.DeleteSession(ctx, g.SessionID)
			case "replace":
				_, err = s.Create(ctx, g.SessionID, "replacement objective", nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			goals, err := s.Runnable(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range goals {
				if candidate.ID == g.ID {
					t.Fatalf("revoked goal is still in the runnable scan: %+v", candidate)
				}
			}
			var round, owner string
			var lease int64
			if err = s.DB.QueryRow("SELECT round_id,lease_owner,lease_until FROM conversation_goals WHERE id=?", g.ID).Scan(&round, &owner, &lease); err != nil || round != "" || owner != "" || lease != 0 {
				t.Fatalf("revoked goal retained a lease: %q %q %d %v", round, owner, lease, err)
			}
			if err = s.Settle(ctx, g, 20); err != nil {
				t.Fatal(err)
			}
			if err = s.Finish(ctx, g.RoundID, 30); err != nil {
				t.Fatal(err)
			}
			var tokens int64
			if err = s.DB.QueryRow("SELECT tokens FROM conversation_goals WHERE id=?", g.ID).Scan(&tokens); err != nil || tokens != 50 {
				t.Fatalf("settled goal tokens = %d, %v", tokens, err)
			}
		})
	}
}

func TestConversationEventRetentionKeepsUnsettledBlocker(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	g, err := s.Create(ctx, "session", "original objective", nil)
	if err != nil {
		t.Fatal(err)
	}
	g.RoundID, err = s.Claim(ctx, g, "owner", "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Report(ctx, g, "blocked", "waiting for verified fixture evidence"); err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<?)
 INSERT INTO conversation_events(session_id,kind,payload,created_at)
 SELECT 'another-session','mode','{}',n FROM seq`, retainedConversationEvents+1)
	if err != nil {
		t.Fatal(err)
	}
	checkCount := func(want int) {
		t.Helper()
		var count int
		if err = s.DB.QueryRow("SELECT COUNT(*) FROM conversation_events").Scan(&count); err != nil || count != want {
			t.Fatalf("retained %d events, want %d: %v", count, want, err)
		}
	}
	if err = s.PruneEvents(ctx); err != nil {
		t.Fatal(err)
	}
	checkCount(retainedConversationEvents + 1)
	if err := s.Report(ctx, g, "blocked", "waiting for verified fixture evidence"); err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(ctx, g, 0); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, g.SessionID)
	if err != nil || state.Goal == nil || state.Goal.BlockedStreak != 1 {
		t.Fatalf("pruning affected blocker deduplication: %+v, %v", state.Goal, err)
	}
	if err = s.PruneEvents(ctx); err != nil {
		t.Fatal(err)
	}
	checkCount(retainedConversationEvents)
}

func TestLegacyProposalReviewAndExecutionUseSameMarkdown(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: testdb.Open(t)}
	body := "# Deliver\n\n1. Inspect.\n2. Check the confirmation dialog."
	legacy := body + "\n\n---\n\n以上只是提案，**在你确认版本之前我不会改动任何文件**。告诉我是否开始。"
	if err := s.SetPlanning(ctx, "session", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE conversation_controls SET proposal=?,proposal_version=1 WHERE session_id='session'", legacy); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, "session")
	if err != nil || state.Proposal != body {
		t.Fatalf("preview=%q err=%v", state.Proposal, err)
	}
	if _, err := s.Confirm(ctx, "session", 1); err != nil {
		t.Fatal(err)
	}
	var queued string
	if err := s.DB.QueryRow("SELECT proposal FROM conversation_plan_runs WHERE session_id='session'").Scan(&queued); err != nil || queued != body {
		t.Fatalf("queued=%q err=%v", queued, err)
	}
}
