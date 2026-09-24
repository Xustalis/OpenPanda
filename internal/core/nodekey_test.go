package core

import (
	"context"
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

	pub1, priv1, ok := c.nodeKeyPair()
	if !ok || len(priv1) == 0 {
		t.Fatal("nodeKeyPair did not yield a key")
	}
	pub2, _, ok := c.nodeKeyPair()
	if !ok || !pub1.Equal(pub2) {
		t.Fatal("nodeKeyPair is not stable across calls")
	}

	other := NewCore(db, "node-a", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
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

	entry := newCore(t, "entry", "127.0.0.1:17970")
	worker := newCore(t, "worker", "127.0.0.1:17971")
	startPair(t, ctx, entry, worker, "127.0.0.1:17970", "127.0.0.1:17971")

	workerPub, ok := entry.peerPubKey("worker")
	if !ok {
		t.Fatal("entry did not record worker's pubkey from the signed hello")
	}
	entryPub, ok := worker.peerPubKey("entry")
	if !ok {
		t.Fatal("worker did not record entry's pubkey from the hello reply")
	}
	// The recorded key must be the peer's real node key.
	if wpub, _, ok := worker.nodeKeyPair(); !ok || !wpub.Equal(workerPub) {
		t.Fatal("recorded worker pubkey differs from its node key")
	}
	if epub, _, ok := entry.nodeKeyPair(); !ok || !epub.Equal(entryPub) {
		t.Fatal("recorded entry pubkey differs from its node key")
	}
}

// TestConsentGrantVerification pins the P2-8 fix: a consent grant minted by
// the origin's key verifies, while a grant signed by another key, presented
// under the wrong public key, or bound to a different task is rejected — and
// an unsigned legacy flag still passes for mixed-version meshes.
func TestConsentGrantVerification(t *testing.T) {
	origin := newCore(t, "origin", "127.0.0.1:17972")
	exec := newCore(t, "exec", "127.0.0.1:17973")

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
	exec.recordPeerPubKey(context.Background(), "origin", hex.EncodeToString(opub))

	if !exec.consentGrantValid(p, "origin") {
		t.Fatal("a grant signed by the recorded origin key was rejected")
	}
	// Relay forwards the same grant verbatim: it still verifies on the next hop.
	if !exec.consentGrantValid(p, "relay-node") {
		t.Fatal("the origin's grant must verify regardless of the last hop")
	}

	forged := p
	_, mPriv, _ := bus.GenerateNodeKey()
	forged.AuthSig = bus.SignAuthorization(mPriv, p.TaskID, true, p.AuthTs)
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

	legacy := bus.TaskDelegatePayload{TaskID: "t-legacy", Authorized: true, Chain: []string{"origin"}}
	if !exec.consentGrantValid(legacy, "origin") {
		t.Fatal("an unsigned legacy consent must still pass")
	}
}

// TestArtifactGrantDirectHandoff is the P2P stage handoff end to end: the
// consumer's task id resolves to nothing on the producer (it only holds its
// own stage's row), yet the orchestrator-signed grant still authorizes the
// pull. A forged grant must be refused.
func TestArtifactGrantDirectHandoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	consumer := newCoreWithNative(t, "consumer", "127.0.0.1:17974", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	producer := newCoreWithNative(t, "producer", "127.0.0.1:17975", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	consumerPool := withArtifactPool(t, consumer)
	producerPool := withArtifactPool(t, producer)
	startPair(t, ctx, consumer, producer, "127.0.0.1:17974", "127.0.0.1:17975")

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
	producer.recordPeerPubKey(ctx, orchID, hex.EncodeToString(orchPub))

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
