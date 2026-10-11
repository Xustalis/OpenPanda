// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/trust"
)

// TestPeerTrustOfDefaultsMesh: absent any assertion every peer resolves to
// mesh — the pre-feature fleet semantics.
func TestPeerTrustOfDefaultsMesh(t *testing.T) {
	c := pinCore(t, "self", "marker")
	if got := c.peerTrustOf(context.Background(), "never-seen"); got != trust.Mesh {
		t.Fatalf("unpinned peer = %q, want mesh", got)
	}
}

// TestPeerTrustOfRowPin: a tier pinned on the directory row resolves.
func TestPeerTrustOfRowPin(t *testing.T) {
	c := pinCore(t, "self", "marker")
	db := c.db
	if _, err := db.Exec(
		`INSERT INTO employee_cache (id, trust) VALUES ('peer', 'guarded')`); err != nil {
		t.Fatal(err)
	}
	if got := c.peerTrustOf(context.Background(), "peer"); got != trust.Guarded {
		t.Fatalf("row pin = %q, want guarded", got)
	}
}

// TestPeerTrustOfSiblingKey: trust follows the proven key, so a sibling row
// carrying the same key inherits the least-trusting tier.
func TestPeerTrustOfSiblingKey(t *testing.T) {
	c := pinCore(t, "self", "marker")
	db := c.db
	const key = "deadbeef"
	if _, err := db.Exec(
		`INSERT INTO employee_cache (id, pub_key, trust) VALUES ('peer-a', ?, 'guarded'), ('peer-b', ?, '')`, key, key); err != nil {
		t.Fatal(err)
	}
	if got := c.peerTrustOf(context.Background(), "peer-b"); got != trust.Guarded {
		t.Fatalf("sibling key = %q, want guarded", got)
	}
}

// TestPeerTrustOfAddrHint: a configured addr hint applies only once the
// peer_addrs binding (recorded by a signed hello) maps addr → identity.
func TestPeerTrustOfAddrHint(t *testing.T) {
	c := pinCore(t, "self", "marker")
	db := c.db
	if _, err := db.Exec(`INSERT INTO employee_cache (id) VALUES ('peer')`); err != nil {
		t.Fatal(err)
	}
	c.SetPeerTrustHints(map[string]string{"ws://10.0.0.9:7836": "guarded"})
	// No binding yet: the hint must not fire.
	if got := c.peerTrustOf(context.Background(), "peer"); got != trust.Mesh {
		t.Fatalf("unbound hint = %q, want mesh", got)
	}
	if _, err := db.Exec(`INSERT INTO peer_addrs (node_id, addr, last_seen) VALUES ('peer', '10.0.0.9:7836', 1)`); err != nil {
		t.Fatal(err)
	}
	if got := c.peerTrustOf(context.Background(), "peer"); got != trust.Guarded {
		t.Fatalf("bound hint = %q, want guarded", got)
	}
}

// TestKeyChangeDemotesMesh: a key change under a mesh-pinned row drops it to
// guarded — the reinstall/MITM/rotation ambiguity must not inherit the old
// tier.
func TestKeyChangeDemotesMesh(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")
	pub, _, _ := bus.GenerateNodeKey()
	c.recordPeerPubKey(ctx, "peer", hex.EncodeToString(pub))
	if err := ledger.SetPeerTrust(c.db, "peer", string(trust.Mesh)); err != nil {
		t.Fatal(err)
	}
	pub2, _, _ := bus.GenerateNodeKey()
	c.recordPeerPubKey(ctx, "peer", hex.EncodeToString(pub2))
	lvl, _ := ledger.PeerTrust(c.db, "peer")
	if lvl != string(trust.Guarded) {
		t.Fatalf("key change trust = %q, want guarded", lvl)
	}
	var verified int
	if err := c.db.QueryRow(`SELECT key_verified FROM employee_cache WHERE id='peer'`).Scan(&verified); err != nil || verified != 0 {
		t.Fatalf("key change must clear verification, got verified=%d err=%v", verified, err)
	}
}

// TestAdmitDelegateTiers exercises the deterministic accept policy across
// tiers without any LLM configured.
func TestAdmitDelegateTiers(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")
	mkPeer := func(id, lvl string) {
		t.Helper()
		if _, err := c.db.Exec(`INSERT INTO employee_cache (id, trust) VALUES (?, ?)`, id, lvl); err != nil {
			t.Fatal(err)
		}
	}
	mkPeer("stranger", "open")
	mkPeer("buddy", "guarded")

	// Mesh origin admits unchanged.
	v := c.admitDelegate(ctx, "t1", bus.TaskDelegatePayload{TaskID: "t1"}, nil, "mesh-peer")
	if !v.admit || v.review || v.code != "" {
		t.Fatalf("mesh verdict = %+v, want admit", v)
	}
	// Open origin is refused with the machine-readable code.
	v = c.admitDelegate(ctx, "t2", bus.TaskDelegatePayload{TaskID: "t2"}, nil, "stranger")
	if v.admit || v.code != bus.DeclineCodeTrustOpen {
		t.Fatalf("open verdict = %+v, want decline trust_open_origin", v)
	}
	// Guarded defaults to auto-admit.
	v = c.admitDelegate(ctx, "t3", bus.TaskDelegatePayload{TaskID: "t3"}, nil, "buddy")
	if !v.admit {
		t.Fatalf("guarded auto verdict = %+v, want admit", v)
	}
	// A mesh origin relayed by a guarded sender inherits the sender's tier:
	// the last hop cannot launder a stranger's work as its own.
	mkPeer("mesh-claim", "")
	v = c.admitDelegate(ctx, "t4", bus.TaskDelegatePayload{TaskID: "t4"}, []string{"mesh-claim"}, "stranger")
	if v.code != bus.DeclineCodeTrustOpen {
		t.Fatalf("guarded-hop laundering = %+v, want trust_open_origin", v)
	}
	// The inverse laundering: an open origin mints a mesh chain[0] and rides a
	// mesh relay — chain = [forged-mesh, open-self, mesh-relay], sender is the
	// relay. Folding every claimed hop still surfaces the open id the wire
	// contract forces it to carry.
	mkPeer("mesh-relay", "")
	v = c.admitDelegate(ctx, "t5", bus.TaskDelegatePayload{TaskID: "t5"},
		[]string{"mesh-claim", "stranger", "mesh-relay"}, "mesh-relay")
	if v.code != bus.DeclineCodeTrustOpen {
		t.Fatalf("forged-origin relay = %+v, want trust_open_origin", v)
	}
	// A genuinely all-mesh chain through a relay admits unchanged.
	v = c.admitDelegate(ctx, "t6", bus.TaskDelegatePayload{TaskID: "t6"},
		[]string{"mesh-claim", "mesh-relay"}, "mesh-relay")
	if !v.admit {
		t.Fatalf("honest mesh relay = %+v, want admit", v)
	}
}

// TestAdmitDelegateConfiguredModes: operator-set accept modes drive the
// verdict — review parks, refuse declines.
func TestAdmitDelegateConfiguredModes(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")
	if _, err := c.db.Exec(`INSERT INTO employee_cache (id, trust) VALUES ('buddy', 'guarded')`); err != nil {
		t.Fatal(err)
	}
	c.SetTrustConfig(testTrustConfig("review", "off"))
	if v := c.admitDelegate(ctx, "t", bus.TaskDelegatePayload{TaskID: "t"}, nil, "buddy"); !v.review {
		t.Fatalf("review mode = %+v, want review", v)
	}
	c.SetTrustConfig(testTrustConfig("refuse", "off"))
	v := c.admitDelegate(ctx, "t", bus.TaskDelegatePayload{TaskID: "t"}, nil, "buddy")
	if v.admit || v.code != bus.DeclineCodeTrustRefused {
		t.Fatalf("refuse mode = %+v, want decline trust_policy_refused", v)
	}
}

// testTrustConfig builds a config with the guarded accept knobs set.
func testTrustConfig(mode, llm string) config.TrustConfig {
	return config.TrustConfig{
		Accept: config.TrustAcceptConfig{
			Guarded: config.TrustAcceptPolicy{Mode: mode, PolicyLLM: llm},
		},
	}
}

// TestParsePolicyVerdict: the classifier's reply is extracted from code
// fences and surrounding prose; anything else fails.
func TestParsePolicyVerdict(t *testing.T) {
	cases := []struct {
		raw     string
		mode    string
		wantErr bool
	}{
		{`{"decision":"accept","reason":"ok"}`, trust.AcceptAuto, false},
		{"```json\n{\"decision\":\"refuse\",\"reason\":\"spam\"}\n```", trust.AcceptRefuse, false},
		{"prose {\"decision\":\"review\",\"reason\":\"unclear\"} tail", trust.AcceptReview, false},
		{"{\"decision\":\"maybe\"}", "", true},
		{"no json at all", "", true},
	}
	for _, tc := range cases {
		m, _, err := parsePolicyVerdict(tc.raw)
		if tc.wantErr != (err != nil) {
			t.Errorf("parsePolicyVerdict(%q) err=%v", tc.raw, err)
			continue
		}
		if !tc.wantErr && m != tc.mode {
			t.Errorf("parsePolicyVerdict(%q) mode=%q want %q", tc.raw, m, tc.mode)
		}
	}
}

// TestVerifyTaskAcceptRules: unsigned frames stay accepted (legacy peers),
// every malformed/stale/mismatched signed variant is dropped.
func TestVerifyTaskAcceptRules(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")
	c.store = NewTaskStore(c.db, testLogger())
	pub, priv, _ := bus.GenerateNodeKey()
	pubHex := hex.EncodeToString(pub)
	now := time.Now().Unix()

	// Legacy frame: nothing signed, nothing asserted.
	if err := c.verifyTaskAccept(ctx, "peer", Task{TaskID: "t"}, bus.TaskAcceptPayload{TaskID: "t"}); err != nil {
		t.Fatalf("legacy frame rejected: %v", err)
	}
	// Partial: pub without sig is malformed, not legacy.
	err := c.verifyTaskAccept(ctx, "peer", Task{TaskID: "t"},
		bus.TaskAcceptPayload{TaskID: "t", Pub: pubHex})
	if err == nil {
		t.Fatal("partially signed frame must be dropped")
	}
	// Malformed pub.
	err = c.verifyTaskAccept(ctx, "peer", Task{TaskID: "t"},
		bus.TaskAcceptPayload{TaskID: "t", Sig: "aa", Pub: "zz", Ts: now})
	if err == nil {
		t.Fatal("malformed signer key must be dropped")
	}
	// Stale timestamp.
	sig := bus.SignAcceptance(priv, "t", "", "k:"+pubHex, now-3600)
	err = c.verifyTaskAccept(ctx, "peer", Task{TaskID: "t"},
		bus.TaskAcceptPayload{TaskID: "t", Sig: sig, Pub: pubHex, Ts: now - 3600})
	if err == nil {
		t.Fatal("stale acceptance must be dropped")
	}
	// Valid.
	sig = bus.SignAcceptance(priv, "t", "", "k:"+pubHex, now)
	if err := c.verifyTaskAccept(ctx, "peer", Task{TaskID: "t"},
		bus.TaskAcceptPayload{TaskID: "t", Sig: sig, Pub: pubHex, Ts: now}); err != nil {
		t.Fatalf("valid acceptance dropped: %v", err)
	}

	// A signature carried verbatim by a relay hop (sender != signer) verifies
	// on its self-contained proof — multi-hop accept forwarding must not
	// break because the forwarding identity differs from the signing one.
	if err := c.store.RecordEvent(ctx, "t", EvDelegate, map[string]any{"target": "relay"}); err != nil {
		t.Fatal(err)
	}
	if err := c.verifyTaskAccept(ctx, "relay", Task{TaskID: "t"},
		bus.TaskAcceptPayload{TaskID: "t", Sig: sig, Pub: pubHex, Ts: now}); err != nil {
		t.Fatalf("relayed acceptance must not bind signer to wire sender: %v", err)
	}
	// Task/attempt binding still holds for relayed frames: a signature for a
	// different attempt minted by the same signer must not transplant.
	sig2 := bus.SignAcceptance(priv, "t", "other-attempt", "k:"+pubHex, now)
	if err := c.verifyTaskAccept(ctx, "relay", Task{TaskID: "t"},
		bus.TaskAcceptPayload{TaskID: "t", AttemptID: "mine", Sig: sig2, Pub: pubHex, Ts: now}); err == nil {
		t.Fatal("acceptance signed for a different attempt must not verify")
	}
}

// TestReputationScoresLaplace: the smoothed rate keeps small samples near
// neutral and converges as outcomes accumulate; 'entry:' rows never count.
func TestReputationScoresLaplace(t *testing.T) {
	c := pinCore(t, "self", "marker")
	ins := func(executor string, success int) {
		t.Helper()
		if _, err := c.db.Exec(
			`INSERT INTO delegation_metrics (task_id, delegator, executor, success, latency_ms, created_at) VALUES ('t', 'self', ?, ?, 0, 0)`, executor, success); err != nil {
			t.Fatal(err)
		}
	}
	ins("good", 1)
	ins("good", 1)
	ins("good", 0)
	ins("bad", 0)
	ins("bad", 0)
	ins("bad", 0)
	ins("entry:self", 1) // self-row alias must not pollute peer scores
	got := c.reputationScores(context.Background())
	if _, ok := got["entry:self"]; ok {
		t.Fatal("entry:* rows must be excluded")
	}
	if got["good"] <= got["bad"] {
		t.Fatalf("good=%v bad=%v — ordering broken", got["good"], got["bad"])
	}
	if g := got["good"]; g < 0.6 || g > 0.8 {
		t.Fatalf("good = %v, want smoothed 3/5 = 0.6", g)
	}
}

// TestPeerTrustOfHintMergeFloor: a stricter config hint still folds over a
// looser row pin — least-trusting wins, so `nodes trust X guarded` can never
// soften a peer the operator already declared open in config.
func TestPeerTrustOfHintMergeFloor(t *testing.T) {
	c := pinCore(t, "self", "marker")
	db := c.db
	if _, err := db.Exec(`INSERT INTO employee_cache (id, trust) VALUES ('peer', 'guarded')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO peer_addrs (node_id, addr, last_seen) VALUES ('peer', '10.0.0.9:7836', 1)`); err != nil {
		t.Fatal(err)
	}
	c.SetPeerTrustHints(map[string]string{"ws://10.0.0.9:7836": "open"})
	if got := c.peerTrustOf(context.Background(), "peer"); got != trust.Open {
		t.Fatalf("hint+pin merge = %q, want open (least-trusting)", got)
	}
}

// TestAdmissionTierSkipsSelf: the stored chain ends at this node — folding
// its own pin would let an operator lock their own node out of all inbound
// work by pinning it guarded. The gate judges the path TO here, not here.
func TestAdmissionTierSkipsSelf(t *testing.T) {
	c := pinCore(t, "self", "marker")
	if err := ledger.SetPeerTrust(c.db, "self", "guarded"); err != nil {
		t.Fatal(err)
	}
	if got := c.admissionTier(context.Background(), []string{"buddy", "self"}, "buddy"); got != trust.Mesh {
		t.Fatalf("self-pin folded admission to %q, want mesh", got)
	}
}

// TestArtifactPullTrustGate: the inputs ceiling binds the pull channel by
// direction. A guarded puller downstream of this node may not fetch input
// material even with a hash learned out of band; an upstream peer reading
// its own task's outputs is unaffected (that read is an output, not an
// input); and a missing task row cannot prove "upstream", so grant-path
// pulls fail the same gate instead of routing around it.
func TestArtifactPullTrustGate(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")
	participantTask(t, c, "t-gate", "origin", []string{"origin", "self"})

	if _, err := c.db.Exec(
		`INSERT INTO employee_cache (id, trust) VALUES ('downstream', 'guarded'), ('origin', 'guarded')`); err != nil {
		t.Fatal(err)
	}

	// Downstream/non-upstream puller under inputs:false — denied.
	if c.artifactPullTrustOK(ctx, "t-gate", "downstream") {
		t.Fatal("guarded downstream puller was served input material")
	}
	// The same puller unpinned (mesh) is unaffected — the ceiling binds
	// only what the operator actually declared.
	if !c.artifactPullTrustOK(ctx, "t-gate", "mesh-puller") {
		t.Fatal("mesh puller was refused by a guarded peer's ceiling")
	}
	// Upstream delegator reading its own outputs — allowed regardless of
	// its pin: nothing upstream of us is ever "input material" to it.
	if !c.artifactPullTrustOK(ctx, "t-gate", "origin") {
		t.Fatal("upstream delegator was barred from its own task outputs")
	}
	// No task row (grant-path pull): direction cannot be proven, so the
	// pull is treated as a downstream read — gated for sub-mesh, open for
	// mesh.
	if c.artifactPullTrustOK(ctx, "t-missing", "downstream") {
		t.Fatal("guarded puller bypassed the ceiling via an unknown task id")
	}
	if !c.artifactPullTrustOK(ctx, "t-missing", "mesh-puller") {
		t.Fatal("mesh puller was gated on an unknown task id")
	}
}
