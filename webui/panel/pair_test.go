// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"gopkg.in/yaml.v3"
)

// TestPairListSeedsRequestsAndDiscovered covers GET /api/pair's read side:
// a ready pair_sessions row surfaces as an inbound request, and a
// pending_nodes row surfaces as a discovered device.
func TestPairListSeedsRequestsAndDiscovered(t *testing.T) {
	db := newMigratedDB(t)
	h := New(Deps{Store: newTestStore(t), DB: db, Cfg: config.Default(), StaticDir: t.TempDir(), Token: testToken})

	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO pair_sessions (id, peer_addr, peer_name, peer_pub, sas, state, created_at, expires_at)
		 VALUES ('sess-req-1', '10.0.0.9:7836', 'pi', 'pubkey', '123-456', 'ready', ?, ?)`, now, now+300); err != nil {
		t.Fatalf("seed pair session: %v", err)
	}
	if err := ledger.UpsertPending(db, ledger.PendingNode{
		ID: "pi@vm-aaa", Addr: "10.0.0.9:7836", Verified: true,
		FirstSeen: now, LastSeen: now,
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/pair", nil))
	if code != http.StatusOK {
		t.Fatalf("GET /api/pair = %d", code)
	}
	reqs, _ := out["requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("requests = %v, want 1 row", reqs)
	}
	row := reqs[0].(map[string]any)
	if row["code"] != "123-456" || row["peer_name"] != "pi" {
		t.Fatalf("request row = %v", row)
	}
	disc, _ := out["discovered"].([]any)
	if len(disc) != 1 || disc[0].(map[string]any)["id"] != "pi@vm-aaa" {
		t.Fatalf("discovered = %v", disc)
	}
}

// TestPairAnswerFlipsSession covers POST /api/pair/answer: confirm moves
// the row out of ready so the daemon's session goroutine can pick it up;
// reject lands terminal.
func TestPairAnswerFlipsSession(t *testing.T) {
	db := newMigratedDB(t)
	h := New(Deps{Store: newTestStore(t), DB: db, Cfg: config.Default(), StaticDir: t.TempDir(), Token: testToken})

	now := time.Now().Unix()
	for _, id := range []string{"sess-yes", "sess-no"} {
		if _, err := db.Exec(
			`INSERT INTO pair_sessions (id, peer_addr, peer_name, peer_pub, sas, state, created_at, expires_at)
			 VALUES (?, '10.0.0.9:7836', 'pi', 'pubkey', '111-222', 'ready', ?, ?)`, id, now, now+300); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	// Confirm: no daemon is running to finish the delivery, so the row
	// stays "confirmed" — the endpoint reports the flip it made.
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/pair/answer", `{"id":"sess-yes","confirm":true}`))
	if code != http.StatusOK || out["state"] != core.PairStateConfirmed {
		t.Fatalf("confirm = %d %v", code, out)
	}
	if st, _ := core.PairSessionState(db, "sess-yes"); st != core.PairStateConfirmed {
		t.Fatalf("sess-yes state = %q", st)
	}

	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/pair/answer", `{"id":"sess-no","confirm":false}`))
	if code != http.StatusOK || out["state"] != core.PairStateRejected {
		t.Fatalf("reject = %d %v", code, out)
	}
	if st, _ := core.PairSessionState(db, "sess-no"); st != core.PairStateRejected {
		t.Fatalf("sess-no state = %q", st)
	}

	// Re-answering a resolved session is a conflict, not a silent re-flip.
	if code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/pair/answer", `{"id":"sess-no","confirm":true}`)); code != http.StatusConflict {
		t.Fatalf("re-answer = %d, want 409", code)
	}
}

// TestPairInitiateEndToEnd runs the whole ceremony through the HTTP API:
// POST initiate dials a real responder core, the responder's operator
// confirms the row, and the console's goroutine lands the delivered secret
// in config + memory + outgoing state — the web twin of `panda pair`.
func TestPairInitiateEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// The responder daemon: a real core listening on loopback.
	respDB := newMigratedDB(t)
	resp := core.NewCore(respDB, "resp-node", ledger.Card{Device: "resp-node"}, 5,
		slog.New(slog.NewTextHandler(io.Discard, nil)), config.ModelConfig{})
	resp.SetSharedSecret("mesh-secret-xyz")
	if err := resp.Register(ctx); err != nil {
		t.Fatalf("resp register: %v", err)
	}
	respAddr := "127.0.0.1:18971"
	go func() { _ = resp.Listen(ctx, respAddr) }()
	time.Sleep(300 * time.Millisecond)

	// The console node: its own config file (the adoption must land here).
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Node.Name = "web-node"
	cfg.Network.ListenAddr = "127.0.0.1:18972"
	cfgYAML, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(cfgPath, cfgYAML, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	panelDB := newMigratedDB(t)
	h := New(Deps{
		Store: newTestStore(t), DB: panelDB, Cfg: cfg,
		ConfigPath: cfgPath, StaticDir: t.TempDir(), Token: testToken,
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/pair/initiate", `{"target":"`+respAddr+`"}`))
	if code != http.StatusOK {
		t.Fatalf("initiate = %d %v", code, out)
	}
	session, _ := out["session"].(string)
	sas, _ := out["code"].(string)
	if session == "" || len(sas) != 7 {
		t.Fatalf("initiate response = %v", out)
	}

	// The responder's row must show the SAME code — that equality is the
	// whole point of the ceremony.
	var respRows []core.PairSessionRow
	for i := 0; i < 40; i++ {
		respRows, _ = core.ListPairSessions(respDB)
		if len(respRows) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(respRows) == 0 {
		t.Fatal("responder never saw the request")
	}
	if respRows[0].SAS != sas {
		t.Fatalf("SAS mismatch: responder %q, initiator %q", respRows[0].SAS, sas)
	}

	// The responder's operator confirms — the row flip the web confirm
	// button (or `panda pair confirm`) performs on that machine.
	if err := core.AnswerPairSession(respDB, respRows[0].ID, core.PairStateConfirmed); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// The console's waiter lands the join: outgoing → done, secret+peer in
	// memory and on disk.
	var done bool
	for i := 0; i < 60; i++ {
		code, view := doJSON(t, h, authedReq(http.MethodGet, "/api/pair", nil))
		if code != http.StatusOK {
			t.Fatalf("poll pair = %d", code)
		}
		for _, o := range view["outgoing"].([]any) {
			m := o.(map[string]any)
			if m["session"] == session && m["state"] == "done" {
				done = true
			}
		}
		if done {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !done {
		t.Fatal("outgoing session never reached done")
	}
	if cfg.Network.SharedSecret != "mesh-secret-xyz" {
		t.Fatalf("in-memory secret = %q", cfg.Network.SharedSecret)
	}
	raw, _ := os.ReadFile(cfgPath)
	back, err := config.Load(cfgPath)
	if err != nil || back.Network.SharedSecret != "mesh-secret-xyz" {
		t.Fatalf("persisted secret missing: %v\n%s", err, raw)
	}
	if len(back.Network.Peers) == 0 || back.Network.Peers[0] != respAddr {
		t.Fatalf("persisted peers = %v", back.Network.Peers)
	}
}
