// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestNodeKeyPairGeneratedAndPersisted: the identity is minted once and then
// survives restarts — a second Core over the same database must read back the
// same public key, or every signed grant minted before the restart would
// verify against a different key afterward.
func TestNodeKeyPairGeneratedAndPersisted(t *testing.T) {
	db := openTestDB(t)
	c := NewCore(db, "node-a", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())

	pub1, priv1, ok := c.nodeKeyPair()
	if !ok || len(priv1) == 0 {
		t.Fatal("nodeKeyPair did not yield a key")
	}
	pub2, _, ok := c.nodeKeyPair()
	if !ok || !pub1.Equal(pub2) {
		t.Fatal("nodeKeyPair is not stable across calls")
	}

	other := NewCore(db, "node-a", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	other.SetWorkDir(t.TempDir())
	pub3, _, ok := other.nodeKeyPair()
	if !ok || !pub1.Equal(pub3) {
		t.Fatal("a fresh Core did not recover the persisted node key")
	}
}

// TestHelloRecordsPeerPubKey: the signed hello's PubKey must land in the
// directory, or nothing downstream — consent grants, artifact grants — can be
// verified against the sender's identity.
func TestHelloRecordsPeerPubKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	entry := newCore(t, "entry")
	worker := newCore(t, "worker")
	startPair(t, ctx, entry, worker)

	// The pubkey lands from the hello handler's goroutine, not synchronously
	// with the dial: under the race detector's load the write can trail the
	// connect by a beat, so poll a bounded window instead of racing it.
	awaitPub := func(c *Core, id string) ed25519.PublicKey {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if pub, ok := c.peerPubKey(id); ok {
				return pub
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not record %s's pubkey from the signed hello", c.nodeID, id)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	workerPub := awaitPub(entry, "worker")
	entryPub := awaitPub(worker, "entry")
	// The recorded key must be the peer's real node key.
	if wpub, _, ok := worker.nodeKeyPair(); !ok || !wpub.Equal(workerPub) {
		t.Fatal("recorded worker pubkey differs from its node key")
	}
	if epub, _, ok := entry.nodeKeyPair(); !ok || !epub.Equal(entryPub) {
		t.Fatal("recorded entry pubkey differs from its node key")
	}

	// The X25519 half rides the same verified-hello gate: each side must
	// have stored the peer's x_pub (for transit sealing) bound to the
	// Ed25519 proof that just verified.
	awaitX := func(c *Core, id string) *ecdh.PublicKey {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if x, ok := c.peerXPub(context.Background(), id); ok {
				return x
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not record %s's x_pub from the signed hello", c.nodeID, id)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if _, wx, ok := worker.nodeXKey(); !ok || !awaitX(entry, "worker").Equal(wx) {
		t.Fatal("recorded worker x_pub differs from its node x key")
	}
	if _, ex, ok := entry.nodeXKey(); !ok || !awaitX(worker, "entry").Equal(ex) {
		t.Fatal("recorded entry x_pub differs from its node x key")
	}
}

// TestPeerKeyTofuLifecycle covers the trust-on-first-use contract the CLI
// exposes: a signed hello records the key UNVERIFIED, the human's `nodes
// verify` stamps it, and a later different key — reinstall, rotation or
// impersonation, indistinguishable at this layer — drops the stamp instead of
// inheriting it.
func TestPeerKeyTofuLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	entry := newCore(t, "entry")
	worker := newCore(t, "worker")
	startPair(t, ctx, entry, worker)

	// First use: the worker's key is on file but nobody has compared it.
	nodes, err := ledger.Query(entry.db, "", "worker")
	if err != nil || len(nodes) != 1 {
		t.Fatalf("query worker: %v len=%d", err, len(nodes))
	}
	w := nodes[0]
	if w.PubKey == "" {
		t.Fatal("worker's signed hello did not leave a pubkey")
	}
	if w.Verified() {
		t.Fatal("a TOFU-recorded key must not read as verified")
	}
	if len(w.Fingerprint()) != 16 {
		t.Fatalf("fingerprint %q — want 16 hex chars", w.Fingerprint())
	}

	// The self row verifies by construction: it is this process's own key.
	self, err := ledger.Query(entry.db, "", "entry")
	if err != nil || len(self) != 1 {
		t.Fatalf("query self: %v len=%d", err, len(self))
	}
	if !self[0].Verified() || self[0].PubKey == "" {
		t.Fatal("self row must carry its own key, verified by construction")
	}

	// Human compares fingerprints and verifies.
	ok, err := ledger.MarkVerified(entry.db, "worker")
	if err != nil || !ok {
		t.Fatalf("mark verified: %v ok=%v", err, ok)
	}
	if nodes, _ = ledger.Query(entry.db, "", "worker"); !nodes[0].Verified() {
		t.Fatal("verify stamp did not stick")
	}

	// A new key under the same ID loses the stamp — the human never saw it.
	newPub, _, _ := bus.GenerateNodeKey()
	entry.recordPeerPubKey(ctx, "worker", hex.EncodeToString(newPub), "")
	nodes, _ = ledger.Query(entry.db, "", "worker")
	if nodes[0].Verified() {
		t.Fatal("a changed key must not inherit the old key's verification")
	}
	if nodes[0].PubKey != hex.EncodeToString(newPub) {
		t.Fatal("the changed key was not recorded")
	}
}

// TestConsentGrantVerification pins the P2-8 fix: a consent grant minted by
// the origin's key verifies, while a grant signed by another key, presented
// under the wrong public key, or bound to a different task is rejected.
// Legacy peers may delegate tasks, but unsigned consent grants never pass.
func TestConsentGrantVerification(t *testing.T) {
	origin := newCore(t, "origin")
	exec := newCore(t, "exec")

	p := bus.TaskDelegatePayload{
		TaskID:     "t-consent",
		Chain:      []string{"origin"},
		Authorized: true,
	}
	origin.signConsentGrant(&p)
	if p.AuthSig == "" || p.AuthPub == "" || p.AuthTs == 0 {
		t.Fatal("signConsentGrant produced no grant")
	}

	// Executor learns the origin's key from its signed hello (recorded here).
	opub, _, _ := origin.nodeKeyPair()
	exec.recordPeerPubKey(context.Background(), "origin", hex.EncodeToString(opub), "")

	if !exec.consentGrantValid(p, "origin") {
		t.Fatal("a grant signed by the recorded origin key was rejected")
	}
	// Relay forwards the same grant verbatim: it still verifies on the next hop.
	if !exec.consentGrantValid(p, "relay-node") {
		t.Fatal("the origin's grant must verify regardless of the last hop")
	}

	forged := p
	_, mPriv, _ := bus.GenerateNodeKey()
	forged.AuthSig = bus.SignAuthorization(mPriv, p.TaskID, true, p.AuthTs, p.ConsentDigest())
	if exec.consentGrantValid(forged, "mallory") {
		t.Fatal("a consent grant signed by a non-origin key was accepted")
	}

	swapped := p
	otherPub, _, _ := bus.GenerateNodeKey()
	swapped.AuthPub = hex.EncodeToString(otherPub)
	if exec.consentGrantValid(swapped, "origin") {
		t.Fatal("a grant presented under a foreign AuthPub was accepted")
	}

	transplant := p
	transplant.TaskID = "t-other"
	if exec.consentGrantValid(transplant, "origin") {
		t.Fatal("a consent grant transplanted to another task was accepted")
	}

	// A transit edit of any intent-bearing field invalidates the grant: the
	// signature binds the task's ConsentDigest, so a relay cannot reshape the
	// task the origin approved.
	mutated := p
	mutated.Intent = "run something else entirely"
	if exec.consentGrantValid(mutated, "relay-node") {
		t.Fatal("a grant must not survive an intent rewrite in transit")
	}
	// Mutable transport fields stay covered by neither signature nor digest:
	// a relay that spent a budget hop did not forge anything.
	mutatedOK := p
	mutatedOK.Chain = []string{"origin", "relay-node"}
	hop := 3
	mutatedOK.DelegationBudget = &hop
	if !exec.consentGrantValid(mutatedOK, "relay-node") {
		t.Fatal("legitimate relay edits (chain, budgets) must not break the grant")
	}

	// Unsigned from a key-capable origin is a STRIPPED grant — refuse.
	stripped := bus.TaskDelegatePayload{TaskID: "t-stripped", Authorized: true, Chain: []string{"origin"}}
	if exec.consentGrantValid(stripped, "origin") {
		t.Fatal("an unsigned consent from a known-signing origin is a stripped grant, not legacy")
	}
	// A torn grant — some fields erased — fails closed rather than degrading.
	torn := p
	torn.AuthSig = ""
	if exec.consentGrantValid(torn, "origin") {
		t.Fatal("a half-present grant must be rejected, not treated as legacy")
	}
	// A key-less legacy origin cannot prove that its user approved the task.
	legacy := bus.TaskDelegatePayload{TaskID: "t-legacy", Authorized: true, Chain: []string{"oldtimer"}}
	if exec.consentGrantValid(legacy, "oldtimer") {
		t.Fatal("an unsigned legacy consent must require the executor's approval")
	}
}

// TestArtifactGrantDirectHandoff is the P2P stage handoff end to end: the
// consumer's task id resolves to nothing on the producer (it only holds its
// own stage's row), yet the orchestrator-signed grant still authorizes the
// pull. A forged grant must be refused.
func TestArtifactGrantDirectHandoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	consumer := newCoreWithNative(t, "consumer", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	producer := newCoreWithNative(t, "producer", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	consumerPool := withArtifactPool(t, consumer)
	producerPool := withArtifactPool(t, producer)
	startPair(t, ctx, consumer, producer)

	// The producer's own stage row: plan-scoped, chain rooted at the
	// orchestrator — exactly what a delegated stage leaves behind.
	const orchID, planID, prodTask, consTask = "orch", "plan-handoff", "t-producer", "t-consumer"
	participantTask(t, producer, prodTask, producer.nodeID, []string{orchID, producer.nodeID})
	if err := producer.store.SetStage(ctx, prodTask, planID, "stage-a", nil); err != nil {
		t.Fatalf("set stage: %v", err)
	}
	m, err := producerPool.PackDir(artifactTree(t, 4096))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := producer.store.SetOutputArtifact(ctx, prodTask, m.Hash); err != nil {
		t.Fatalf("record output: %v", err)
	}

	// The orchestrator is a fabricated third node: the producer recorded its
	// key from its hello, and it signed this input ref for the consumer.
	orchPub, orchPriv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("orch key: %v", err)
	}
	producer.recordPeerPubKey(ctx, orchID, hex.EncodeToString(orchPub), "")

	grant := bus.SignArtifactGrant(orchPriv, planID, consTask, prodTask, m.Hash)
	ref := bus.ArtifactRef{
		Hash: m.Hash, Source: producer.nodeID, OfTask: prodTask,
		Grant: grant, Issuer: orchID,
	}
	got, err := consumer.FetchArtifactRef(ctx, producer.nodeID, consTask, ref)
	if err != nil {
		t.Fatalf("direct handoff fetch: %v", err)
	}
	if got.Hash != m.Hash || got.Size != m.Size {
		t.Fatalf("fetched %s (%d), want %s (%d)", got.Hash, got.Size, m.Hash, m.Size)
	}
	if size, ok := consumerPool.Has(m.Hash); !ok || size != m.Size {
		t.Fatalf("consumer pool has %d after handoff", size)
	}

	// A grant signed by anyone but the orchestrator must not open the pool.
	m2, err := producerPool.PackDir(artifactTree(t, 1024))
	if err != nil {
		t.Fatalf("pack2: %v", err)
	}
	if err := producer.store.SetOutputArtifact(ctx, prodTask, m2.Hash); err != nil {
		t.Fatalf("record output2: %v", err)
	}
	_, malloryPriv, _ := bus.GenerateNodeKey()
	forged := bus.SignArtifactGrant(malloryPriv, planID, consTask, prodTask, m2.Hash)
	badRef := bus.ArtifactRef{
		Hash: m2.Hash, Source: producer.nodeID, OfTask: prodTask,
		Grant: forged, Issuer: orchID,
	}
	if _, err := consumer.FetchArtifactRef(ctx, producer.nodeID, consTask, badRef); err == nil {
		t.Fatal("a forged grant was served the artifact")
	}
}

// TestConsentDigestBindsScopeAndIsolation pins the two scope-bearing fields
// that were added after ConsentDigest's whitelist was written and silently
// went unbound: an authenticated relay could rewrite the task's write lanes
// (which decide sibling lock granularity) or flip worktree isolation, and the
// origin's Ed25519 grant still verified. Chain and budgets stay unbound —
// legitimate relay edits must not break consent.
func TestConsentDigestBindsScopeAndIsolation(t *testing.T) {
	p := bus.TaskDelegatePayload{
		TaskID: "scope-task", Intent: "edit the tree",
		Paths: []string{"internal/core"}, Isolate: true,
	}
	base := p.ConsentDigest()
	if base == "" {
		t.Fatal("empty digest")
	}

	scoped := p
	scoped.Paths = []string{"internal/commander"}
	if scoped.ConsentDigest() == base {
		t.Fatal("a relay rewrite of the declared write lanes did not change the digest")
	}
	shared := p
	shared.Isolate = false
	if shared.ConsentDigest() == base {
		t.Fatal("a relay flip of worktree isolation did not change the digest")
	}

	// The legitimate relay edits stay out of the digest.
	moved := p
	moved.Chain = []string{"origin", "relay"}
	moved.TimeoutMS = 1
	if moved.ConsentDigest() != base {
		t.Fatal("chain/timeout are relay-rewritten and must not move the digest")
	}
}
