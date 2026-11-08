// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// newPairTUI builds a model whose repl owns a real database — the store the
// pairing queue lives in. Unlike newNoteTUI it wires r.db rather than a
// TaskStore: the request table is read through the plain handle, which is
// exactly what the banner and /pair do.
func newPairTUI(t *testing.T) (tuiModel, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Node:  config.NodeConfig{Name: "pair-test", Kind: "vm", ResourceClass: "Standard"},
		UI:    config.UIConfig{Locale: "en"},
		Model: config.ModelConfig{BaseURL: "http://127.0.0.1:9", Model: "test-model"},
		Storage: config.StorageConfig{
			DBPath: filepath.Join(dir, "panda.db"),
		},
	}
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := &repl{loc: i18n.English, cfg: cfg, db: db, interactive: true}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width, m.height = 100, 30
	return m, db
}

// offerPair writes the row a responder's daemon writes when a hello arrives.
func offerPair(t *testing.T, db *sql.DB, id, name, sas string) {
	t.Helper()
	now := time.Now()
	_, err := db.Exec(
		`INSERT INTO pair_sessions (id, peer_addr, peer_name, peer_pub, sas, state, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "10.86.36.57:7836", name, "00", sas, core.PairStateReady,
		now.Unix(), now.Add(4*time.Minute).Unix())
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
}

func pairState(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT state FROM pair_sessions WHERE id=?`, id).Scan(&state); err != nil {
		t.Fatalf("state: %v", err)
	}
	return state
}

// TestTUIAdoptPairsAnnouncesOnce pins the two halves of "ambient, not spam":
// the banner appears as soon as a request exists, and the transcript note
// fires once per request rather than on every two-second poll.
func TestTUIAdoptPairsAnnouncesOnce(t *testing.T) {
	m, db := newPairTUI(t)
	offerPair(t, db, "a1b2c3d4e5f6a7b8", "opi3b", "482-913")

	if rows := pendingPairs(m.r); len(rows) != 1 {
		t.Fatalf("pending should see the request, got %+v", rows)
	}

	cmds := m.adoptPairs(pendingPairs(m.r))
	if m.pairReq == nil {
		t.Fatal("the banner has no request to render")
	}
	if len(cmds) != 1 {
		t.Fatalf("the first sighting should print exactly one note, got %d", len(cmds))
	}
	banner := m.pairBanner()
	for _, want := range []string{"opi3b", "482-913", "/pair"} {
		if !strings.Contains(banner, want) {
			t.Fatalf("banner = %q, want it to mention %q", banner, want)
		}
	}

	// The same request on the next poll: the banner stays, the note does not
	// repeat — four minutes of a note every two seconds is 120 copies.
	if again := m.adoptPairs(pendingPairs(m.r)); len(again) != 0 {
		t.Fatalf("the second poll re-announced the request: %d notes", len(again))
	}
	if m.pairBanner() == "" {
		t.Fatal("the banner must survive while the request does")
	}

	// Answered elsewhere (the console, another shell): the banner clears.
	if _, err := db.Exec(`UPDATE pair_sessions SET state=? WHERE id=?`,
		core.PairStateRejected, "a1b2c3d4e5f6a7b8"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m.adoptPairs(pendingPairs(m.r))
	if m.pairReq != nil {
		t.Fatalf("a resolved request must drop the banner, still showing %+v", m.pairReq)
	}
}

// TestTUIPairCommandOpensCardAndConfirms walks the decision the card exists
// for: `/pair` raises it with the code on screen, [y] flips the row, and the
// banner goes away because there is nothing left to answer.
func TestTUIPairCommandOpensCardAndConfirms(t *testing.T) {
	m, db := newPairTUI(t)
	const id = "0123456789abcdef0123456789abcdef"
	offerPair(t, db, id, "opi3b", "482-913")

	next, cmd := m.pairCommand("")
	got := next.(tuiModel)
	if cmd != nil {
		t.Fatalf("opening the card should not print anything, got %q", collectPrinted(cmd))
	}
	if got.mode != modeConfirm {
		t.Fatalf("bare /pair should raise the yes/no card, mode=%v", got.mode)
	}
	for _, want := range []string{"opi3b", "482-913", i18n.T(i18n.English, "tui.pair.cardHint")} {
		if !strings.Contains(got.confirmText, want) {
			t.Fatalf("card text = %q, want %q", got.confirmText, want)
		}
	}
	if got.confirmRun == nil {
		t.Fatal("the card has no [y] action wired")
	}

	next, cmd = got.confirmRun(&got)
	got = next.(tuiModel)
	if state := pairState(t, db, id); state != core.PairStateConfirmed {
		t.Fatalf("confirming should flip the row, got state %q", state)
	}
	if want := i18n.T(i18n.English, "tui.pair.confirmed"); !strings.Contains(collectPrinted(cmd), want) {
		t.Fatalf("the confirmation note = %q, want %q", collectPrinted(cmd), want)
	}
	if got.pairReq != nil {
		t.Fatal("the banner should clear once the request is answered")
	}
}

// TestTUIPairRejectVerbSkipsTheCard covers the explicit form: `/pair reject
// <id>` answers without a card, which is what a reader who already read the
// banner wants, and what a headless operator scripting the shell expects.
func TestTUIPairRejectVerbSkipsTheCard(t *testing.T) {
	m, db := newPairTUI(t)
	const id = "fedcba9876543210fedcba9876543210"
	offerPair(t, db, id, "laptop", "111-222")

	next, cmd := m.submitSlash("pair", "reject "+id, "/pair reject "+id)
	got := next.(tuiModel)
	if got.mode != modeIdle {
		t.Fatalf("a verb should not raise the card, mode=%v", got.mode)
	}
	if state := pairState(t, db, id); state != core.PairStateRejected {
		t.Fatalf("row state = %q, want rejected", state)
	}
	if want := i18n.T(i18n.English, "tui.pair.rejected"); !strings.Contains(collectPrinted(cmd), want) {
		t.Fatalf("note = %q, want %q", collectPrinted(cmd), want)
	}
}

// TestTUIPairWithNothingWaitingSaysSo keeps the empty case honest: an operator
// who typed /pair hoping to answer something should be told the queue is
// empty, not handed a card for a request that does not exist.
func TestTUIPairWithNothingWaitingSaysSo(t *testing.T) {
	m, _ := newPairTUI(t)
	next, cmd := m.pairCommand("")
	got := next.(tuiModel)
	if got.mode != modeIdle {
		t.Fatalf("an empty queue must not raise a card, mode=%v", got.mode)
	}
	if want := i18n.T(i18n.English, "tui.pair.none"); !strings.Contains(collectPrinted(cmd), want) {
		t.Fatalf("note = %q, want %q", collectPrinted(cmd), want)
	}
}

// TestREPLPairListingShowsTheCodeAtTheCLI is the headless half: the classic
// shell prints the same six digits the banner shows, so a request answered
// over SSH without a TUI or a browser is the same ceremony.
func TestREPLPairListingShowsTheCodeAtTheCLI(t *testing.T) {
	m, db := newPairTUI(t)
	offerPair(t, db, "deadbeefcafe1234", "opi3b", "482-913")

	var out strings.Builder
	m.r.commandOut = &out
	m.r.cmdPair("")
	printed := out.String()
	for _, want := range []string{"opi3b", "482-913", "deadbeef"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("listing = %q, want %q", printed, want)
		}
	}
	if !strings.Contains(printed, i18n.T(i18n.English, "repl.pair.howto")) {
		t.Fatalf("listing should say how to answer, got %q", printed)
	}

	out.Reset()
	m.r.cmdPair("confirm deadbeef")
	if state := pairState(t, db, "deadbeefcafe1234"); state != core.PairStateConfirmed {
		t.Fatalf("the short id should resolve, row state %q", state)
	}
	if !strings.Contains(out.String(), i18n.T(i18n.English, "repl.pair.confirmed")) {
		t.Fatalf("note = %q", out.String())
	}
}

// TestREPLPairRefusesABareToken: confirming hands the mesh secret to another
// machine, so a token that is neither a verb nor a command must not be read as
// consent — it prints usage.
func TestREPLPairRefusesABareToken(t *testing.T) {
	m, db := newPairTUI(t)
	offerPair(t, db, "aaaaaaaaaaaaaaaa", "stranger", "000-111")

	var out strings.Builder
	m.r.commandOut = &out
	m.r.cmdPair("aaaaaaaa")
	if !strings.Contains(out.String(), i18n.T(i18n.English, "repl.pair.usage")) {
		t.Fatalf("a bare token should print usage, got %q", out.String())
	}
	if state := pairState(t, db, "aaaaaaaaaaaaaaaa"); state != core.PairStateReady {
		t.Fatalf("nothing should have been answered, row state %q", state)
	}
}
