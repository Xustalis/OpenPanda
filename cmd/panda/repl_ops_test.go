package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// newOpsTestRepl builds a repl backed by a real in-memory store: enough for
// the store-facing verbs (metrics, audit, reminder, session, task edits,
// nodes remove) to run their happy and refusal paths.
func newOpsTestRepl(t *testing.T) (*repl, *core.TaskStore) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := &repl{
		loc:        i18n.English,
		db:         db,
		store:      core.NewTaskStore(db, nil),
		sessionsSt: sessions.NewStore(t.TempDir()),
		cfg: &config.Config{
			Node: config.NodeConfig{Name: "test-node", Kind: "desktop", Identity: "test-ident"},
		},
	}
	return r, r.store
}

// dispatchCapture runs one slash line through the scoped dispatcher and
// returns what the command wrote — the transcript the TUI would render.
func dispatchCapture(t *testing.T, r *repl, line string) string {
	t.Helper()
	var out bytes.Buffer
	r.dispatchWithIO(context.Background(), line, strings.NewReader(""), &out, &out)
	return out.String()
}

func TestReplMetricsEmptyAndReport(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	out := dispatchCapture(t, r, "/metrics")
	if !strings.Contains(out, "no metrics") {
		t.Fatalf("/metrics on an empty store should say so, got %q", out)
	}
}

func TestReplAuditVerifyAndEntries(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	out := dispatchCapture(t, r, "/audit")
	if !strings.Contains(out, "audit chain: OK") {
		t.Fatalf("/audit verify should report OK on an empty chain, got %q", out)
	}
	out = dispatchCapture(t, r, "/audit entries")
	if !strings.Contains(out, "no audit") {
		t.Fatalf("/audit entries on an empty log should say so, got %q", out)
	}
}

func TestReplReminderLifecycle(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	out := dispatchCapture(t, r, "/reminder")
	if !strings.Contains(out, "no reminders") {
		t.Fatalf("/reminder on an empty store should say so, got %q", out)
	}
	out = dispatchCapture(t, r, `/reminder add --after 10m "standup sync"`)
	if !strings.Contains(out, "scheduled") {
		t.Fatalf("/reminder add should confirm, got %q", out)
	}
	out = dispatchCapture(t, r, "/reminder list")
	if !strings.Contains(out, "standup sync") {
		t.Fatalf("/reminder list should show the new reminder, got %q", out)
	}
	// rm needs the numeric id — parse it out of the added message.
	id := ""
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "#") {
			id = strings.TrimPrefix(tok, "#")
		}
	}
	if id == "" {
		// the added line carries the id plainly: "reminder N scheduled…"
		fields := strings.Fields(dispatchCapture(t, r, "/reminder list"))
		if len(fields) > 0 {
			id = strings.TrimPrefix(fields[0], "#")
		}
	}
	out = dispatchCapture(t, r, "/reminder rm "+id)
	if !strings.Contains(out, "removed") {
		t.Fatalf("/reminder rm %s should confirm, got %q", id, out)
	}
}

func TestReplSessionVerbs(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	out := dispatchCapture(t, r, `/session new --title "ops thread"`)
	// The created id is the first token of the output line.
	fields := strings.Fields(out)
	if len(fields) == 0 {
		t.Fatalf("/session new should print the new id, got %q", out)
	}
	id := fields[0]

	out = dispatchCapture(t, r, "/session show "+id)
	if !strings.Contains(out, "ops thread") {
		t.Fatalf("/session show should print the title, got %q", out)
	}

	out = dispatchCapture(t, r, "/session mv "+id+" infra")
	if !strings.Contains(out, "infra") {
		t.Fatalf("/session mv should confirm the move, got %q", out)
	}

	// Removing the attached session is refused (the detach is a front-end
	// action); a detached one deletes.
	r.activeSess = id
	out = dispatchCapture(t, r, "/session rm "+id)
	if !strings.Contains(out, "/resume -") {
		t.Fatalf("/session rm on the attached session should refuse, got %q", out)
	}
	r.activeSess = ""
	out = dispatchCapture(t, r, "/session rm "+id)
	if !strings.Contains(out, "deleted") {
		t.Fatalf("/session rm should confirm, got %q", out)
	}
	if _, err := r.sessionsSt.Get(id); err == nil {
		t.Fatal("removed session should be gone from the store")
	}
}

func TestReplTaskVerbs(t *testing.T) {
	r, st := newOpsTestRepl(t)
	task, err := st.Create(context.Background(), "", "", "demo task", "node-a", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	out := dispatchCapture(t, r, "/task priority "+task.TaskID+" high")
	if !strings.Contains(out, "high") {
		t.Fatalf("/task priority should confirm, got %q", out)
	}
	out = dispatchCapture(t, r, "/task move "+task.TaskID+" 7")
	if !strings.Contains(out, "7") {
		t.Fatalf("/task move should confirm, got %q", out)
	}
	out = dispatchCapture(t, r, "/task delete "+task.TaskID)
	if !strings.Contains(out, "deleted") {
		t.Fatalf("/task delete should confirm, got %q", out)
	}

	// /task add needs the ask engine — without one it must report, not panic.
	out = dispatchCapture(t, r, "/task add --title demo")
	if !strings.Contains(out, "engine") {
		t.Fatalf("/task add without an engine should name the gap, got %q", out)
	}
}

func TestReplNodesRemove(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	out := dispatchCapture(t, r, "/nodes remove ghost-node")
	if !strings.Contains(out, "no such node") {
		t.Fatalf("/nodes remove on a missing node should say so, got %q", out)
	}
	// Removing the local row is refused — the runtime id for the test
	// node's name resolves through the same helper the CLI uses.
	out = dispatchCapture(t, r, "/nodes remove "+core.RuntimeNodeID("test-node", "desktop", "test-ident"))
	if !strings.Contains(out, "local node") || strings.Contains(out, "no such node") {
		t.Fatalf("/nodes remove self should refuse, got %q", out)
	}
}

// TestSplitArgsQuotes pins the tokenizer every verb shares: double quotes group
// a payload, an unmatched quote soaks the rest of the line, backslashes stay
// literal (a Windows path must not eat the following quote).
func TestSplitArgsQuotes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`add --title "fix the thing" --priority high`, []string{"add", "--title", "fix the thing", "--priority", "high"}},
		{`set user "likes tea"`, []string{"set", "user", "likes tea"}},
		{`  spaced   out  `, []string{"spaced", "out"}},
		{`say "unterminated`, []string{"say", "unterminated"}},
		{`path C:\dir "x`, []string{`path`, `C:\dir`, "x"}},
		{"", nil},
	} {
		if got := splitArgs(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("splitArgs(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestReplCardSet covers /card set end-to-end: a valid assignment lands on
// disk, a bad one names the syntax, and the reload notice is scoped to the
// command stream (no daemon here → the restart-hint branch runs).
func TestReplCardSet(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	seed := ledger.Card{Device: "node-a", ResourceClass: "Standard",
		Capacity:        ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2},
		ResourceProfile: ledger.ResourceProfile{CPU: 4, RAMGB: 8, DurationHint: "short"}}
	if err := writeCard(cardPath, seed, false); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	r.cardPath = cardPath

	out := dispatchCapture(t, r, "/card set device=renamed capacity.ram_gb=32")
	if !strings.Contains(out, "updated") {
		t.Fatalf("/card set should confirm the write, got %q", out)
	}
	got, err := ledger.LoadCard(cardPath)
	if err != nil || got.Device != "renamed" || got.Capacity.RAMGB != 32 {
		t.Fatalf("card on disk not updated: %+v, %v", got, err)
	}
	if !strings.Contains(out, i18n.T(i18n.English, "repl.card.noEngine")) {
		t.Fatalf("no engine → the reload notice should be reported, got %q", out)
	}

	out = dispatchCapture(t, r, "/card set bogusassign")
	if !strings.Contains(out, "is not") {
		t.Fatalf("/card set without '=' should explain the syntax, got %q", out)
	}
	out = dispatchCapture(t, r, "/card set resource_class=Enormous")
	if !strings.Contains(out, "invalid") {
		t.Fatalf("/card set with a bad value should reject, got %q", out)
	}
}

// TestReplCardRescan exercises the dry-run default and the --write path.
// detectCard runs against the real host, so the assertions stay loose: the
// contract is "reports, and only --write may persist".
func TestReplCardRescan(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	seed := ledger.Card{Device: "ancient-laptop", ResourceClass: "Standard",
		Capacity:        ledger.Capacity{CPUCores: 1, RAMGB: 1, MaxConcurrent: 1},
		ResourceProfile: ledger.ResourceProfile{CPU: 1, RAMGB: 1, DurationHint: "short"}}
	if err := writeCard(cardPath, seed, false); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	r.cardPath = cardPath
	before, _ := os.ReadFile(cardPath)

	out := dispatchCapture(t, r, "/card rescan")
	if !strings.Contains(out, "dry run") && !strings.Contains(out, "already matches") {
		t.Fatalf("/card rescan must report diffs or parity, got %q", out)
	}
	if strings.Contains(out, "dry run") {
		after, _ := os.ReadFile(cardPath)
		if !bytes.Equal(before, after) {
			t.Fatal("a dry-run rescan rewrote the card")
		}
	}

	out = dispatchCapture(t, r, "/card rescan --write")
	if !strings.Contains(out, "updated") && !strings.Contains(out, "already matches") {
		t.Fatalf("/card rescan --write should report the outcome, got %q", out)
	}
	if _, err := ledger.LoadCard(cardPath); err != nil {
		t.Fatalf("card unreadable after rescan --write: %v", err)
	}
}

// TestReplMemorySetRm covers the two new memory verbs: inline content (quoted
// or bare), --file, the read-only refusal, and topic-only removal.
func TestReplMemorySetRm(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	r.cfg.Storage.MemoryPath = t.TempDir()
	r.cfg.Storage.ProjectsPath = t.TempDir()

	out := dispatchCapture(t, r, `/memory set user "likes tea a lot"`)
	if !strings.Contains(out, "saved") {
		t.Fatalf("/memory set should confirm, got %q", out)
	}
	data, err := os.ReadFile(memory.UserPath(r.cfg.Storage.MemoryPath))
	if err != nil || !strings.Contains(string(data), "likes tea a lot") {
		t.Fatalf("USER.md missing the stored text: %q, %v", string(data), err)
	}
	if strings.Contains(string(data), `"likes tea`) {
		t.Fatal("the quote marks leaked into memory content")
	}

	src := filepath.Join(t.TempDir(), "blob.md")
	if err := os.WriteFile(src, []byte("file body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = dispatchCapture(t, r, "/memory set topic:grocery --file "+src)
	if !strings.Contains(out, "saved") {
		t.Fatalf("/memory set --file should confirm, got %q", out)
	}

	// Read-only and non-topic targets refuse; topics delete.
	out = dispatchCapture(t, r, "/memory set dreams nope")
	if !strings.Contains(strings.ToLower(out), "read-only") && !strings.Contains(out, "dreams") {
		t.Fatalf("/memory set on a read-only target should refuse, got %q", out)
	}
	out = dispatchCapture(t, r, "/memory rm user")
	if !strings.Contains(strings.ToLower(out), "topic") {
		t.Fatalf("/memory rm user should explain the topic-only rule, got %q", out)
	}
	out = dispatchCapture(t, r, "/memory rm topic:grocery")
	if !strings.Contains(out, "removed") {
		t.Fatalf("/memory rm should confirm, got %q", out)
	}
	out = dispatchCapture(t, r, "/memory get topic:grocery")
	if strings.Contains(out, "file body") {
		t.Fatal("the removed topic still reads back")
	}
}
