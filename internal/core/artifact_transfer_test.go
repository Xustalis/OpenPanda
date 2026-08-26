package core

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// artifactTree writes a tree whose bulk is incompressible, so the packed archive
// really is the size the test claims. Compressible filler would pack to a few
// kilobytes and the transfer would fit in a single chunk, quietly turning a test
// about chunking into a test about one frame.
func artifactTree(t *testing.T, bulk int) string {
	t.Helper()
	dir := t.TempDir()
	blob := make([]byte, bulk)
	rng := rand.New(rand.NewSource(20260826))
	if _, err := rng.Read(blob); err != nil {
		t.Fatalf("fill blob: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "weights.bin"), blob, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "train.py"), []byte("print('train')\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return dir
}

// withArtifactPool gives a core a pool of its own under the test's temp dir.
func withArtifactPool(t *testing.T, c *Core) *artifact.Store {
	t.Helper()
	s := artifact.NewStore(filepath.Join(t.TempDir(), "artifacts"))
	c.SetArtifactStore(s)
	return s
}

// participantTask puts a task row on a node with the given delegation chain, so
// the node will serve that task's artifacts to the other nodes in the chain. It
// is what handleDelegate would have created on a real worker.
func participantTask(t *testing.T, c *Core, taskID, owner string, chain []string) {
	t.Helper()
	if _, err := c.store.CreateWithID(context.Background(), taskID, "", "", "train a model", owner, chain); err != nil {
		t.Fatalf("create task on %s: %v", c.nodeID, err)
	}
}

// TestArtifactTransferRoundTrip is the data plane in the shape the flagship
// scenario needs it: the node that produced a multi-megabyte artifact is not the
// node that consumes it, and the bytes have to cross a transport that refuses
// any single frame over 4 MiB.
//
// 3.5 MiB of incompressible content is deliberately chosen: it exceeds one frame,
// so a non-chunked implementation could not pass, and it spans four 1 MiB chunks
// with a partial one at the end — the off-by-one case a chunk loop gets wrong.
func TestArtifactTransferRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	consumer := newCoreWithNative(t, "mac", "127.0.0.1:17960", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	producer := newCoreWithNative(t, "win", "127.0.0.1:17961", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	consumerPool := withArtifactPool(t, consumer)
	producerPool := withArtifactPool(t, producer)
	startPair(t, ctx, consumer, producer, "127.0.0.1:17960", "127.0.0.1:17961")

	// The producer packs its output; both nodes know the task, as they would
	// after a delegation.
	const taskID = "t-artifact-roundtrip"
	participantTask(t, consumer, taskID, consumer.nodeID, []string{consumer.nodeID})
	participantTask(t, producer, taskID, consumer.nodeID, []string{consumer.nodeID, producer.nodeID})

	m, err := producerPool.PackDir(artifactTree(t, 3584<<10)) // 3.5 MiB
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if m.Size <= 3<<20 {
		t.Fatalf("packed to %d bytes: too small to span four chunks", m.Size)
	}
	wantChunks := int((m.Size + bus.ArtifactChunkBytes - 1) / bus.ArtifactChunkBytes)
	if wantChunks != 4 {
		t.Fatalf("%d bytes spans %d chunks, want 4", m.Size, wantChunks)
	}

	got, err := consumer.FetchArtifact(ctx, producer.nodeID, taskID, m.Hash)
	if err != nil {
		t.Fatalf("fetch artifact: %v", err)
	}
	if got.Hash != m.Hash || got.Size != m.Size {
		t.Fatalf("fetched %s (%d bytes), want %s (%d)", got.Hash, got.Size, m.Hash, m.Size)
	}
	if size, ok := consumerPool.Has(m.Hash); !ok || size != m.Size {
		t.Fatalf("consumer pool has %d, %v after the transfer", size, ok)
	}

	// The point of moving the bytes is that the next stage can run on the tree.
	dst := filepath.Join(t.TempDir(), "input")
	if _, err := consumerPool.Extract(m.Hash, dst); err != nil {
		t.Fatalf("extract transferred artifact: %v", err)
	}
	script, err := os.ReadFile(filepath.Join(dst, "src", "train.py"))
	if err != nil {
		t.Fatalf("read extracted script: %v", err)
	}
	if string(script) != "print('train')\n" {
		t.Fatalf("extracted script = %q", script)
	}

	// A second fetch is free: content addressing means the node recognizes what
	// it already holds instead of moving 3.5 MiB again.
	again, err := consumer.FetchArtifact(ctx, producer.nodeID, taskID, m.Hash)
	if err != nil || again.Hash != m.Hash {
		t.Fatalf("re-fetch of a held artifact = %s, %v", again.Hash, err)
	}
}

// TestArtifactTransferRejectsTamperedChunk is the reason the transfer verifies at
// all. One chunk of the archive is altered in the producer's pool, so the bytes
// on the wire are not the bytes the hash names. The consumer must end up holding
// nothing: a stage that ran on a partially corrupted model would produce results
// nobody could distinguish from correct ones.
func TestArtifactTransferRejectsTamperedChunk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	consumer := newCoreWithNative(t, "mac", "127.0.0.1:17962", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	producer := newCoreWithNative(t, "win", "127.0.0.1:17963", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	consumerPool := withArtifactPool(t, consumer)
	producerPool := withArtifactPool(t, producer)
	startPair(t, ctx, consumer, producer, "127.0.0.1:17962", "127.0.0.1:17963")

	const taskID = "t-artifact-tampered"
	participantTask(t, consumer, taskID, consumer.nodeID, []string{consumer.nodeID})
	participantTask(t, producer, taskID, consumer.nodeID, []string{consumer.nodeID, producer.nodeID})

	m, err := producerPool.PackDir(artifactTree(t, 3584<<10))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}

	// Damage a byte inside the second chunk, leaving the archive's length — and
	// therefore every offset and the EOF boundary — exactly as advertised. Only
	// the content hash can catch this.
	raw, err := os.ReadFile(producerPool.Path(m.Hash))
	if err != nil {
		t.Fatalf("read stored artifact: %v", err)
	}
	raw[bus.ArtifactChunkBytes+7] ^= 0xff
	if err := os.WriteFile(producerPool.Path(m.Hash), raw, 0o644); err != nil {
		t.Fatalf("write tampered artifact: %v", err)
	}

	if _, err := consumer.FetchArtifact(ctx, producer.nodeID, taskID, m.Hash); err == nil {
		t.Fatalf("fetch accepted an artifact whose bytes do not hash to its name")
	} else if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("fetch error = %v, want a hash mismatch", err)
	}
	if _, ok := consumerPool.Has(m.Hash); ok {
		t.Fatalf("a tampered artifact is reachable under %s", m.Hash)
	}
	if hashes, err := consumerPool.List(); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(hashes) != 0 {
		t.Fatalf("consumer pool holds %v after a rejected transfer", hashes)
	}
}

// TestArtifactFetchFromNonParticipantRefused: the pool holds every artifact this
// node has produced, for every task it has run. Participation in the task being
// asked about is what authorizes a read, or one authenticated peer could
// enumerate and download another peer's work.
func TestArtifactFetchFromNonParticipantRefused(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "holder", "127.0.0.1:17964")
	pool := withArtifactPool(t, c)

	m, err := pool.PackDir(artifactTree(t, 4096))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	participantTask(t, c, "t-authz", "owner-node", []string{"owner-node", c.nodeID})

	// reply() needs a connection to the requester; there is none here, so the
	// refusal is observed as "nothing was read" rather than as a chunk. What
	// matters is that the authorization decision, not the transport, is what
	// stops it.
	if c.artifactPeerAuthorized(ctx, "t-authz", "mallory") {
		t.Fatalf("a node outside the chain was authorized to pull artifacts")
	}
	if !c.artifactPeerAuthorized(ctx, "t-authz", "owner-node") {
		t.Fatalf("the task owner was refused its own artifact")
	}
	// An unknown task authorizes nobody: a peer must not be able to probe the
	// pool by inventing a task id.
	if c.artifactPeerAuthorized(ctx, "t-does-not-exist", "owner-node") {
		t.Fatalf("an unknown task authorized a pull")
	}
	if _, ok := pool.Has(m.Hash); !ok {
		t.Fatalf("test artifact missing from the pool")
	}
}

// TestArtifactChunkFromNonSourceIgnored is the receiving half of the same
// reasoning (P1-11, as for context_ack): only the node a fetch was sent to may
// answer it. Otherwise any peer could pre-empt the real source — with OK:false to
// fail the stage, or with bytes of its own at the offset being awaited.
func TestArtifactChunkFromNonSourceIgnored(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "exec", "127.0.0.1:17965")
	withArtifactPool(t, c)

	tr := &artifactTransfer{source: "win", chunks: make(chan bus.ArtifactChunkPayload, 4)}
	hash := strings.Repeat("ab", 32)
	c.pendingArt.Store(artifactKey("t-chunk", hash), tr)

	forged, err := bus.NewEnvelope(bus.MsgArtifactChunk, "mallory", "m-forged", bus.ArtifactChunkPayload{
		TaskID: "t-chunk", Hash: hash, Offset: 0, OK: false, Reason: "artifact not held",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleArtifactChunk(ctx, forged)
	if len(tr.chunks) != 0 {
		t.Fatalf("forged chunk delivered to the transfer")
	}

	// Positive control: the source's chunk reaches the waiting transfer.
	legit, err := bus.NewEnvelope(bus.MsgArtifactChunk, "win", "m-legit", bus.ArtifactChunkPayload{
		TaskID: "t-chunk", Hash: hash, Offset: 0, OK: true, Data: []byte("x"), Total: 1, EOF: true,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleArtifactChunk(ctx, legit)
	if len(tr.chunks) != 1 {
		t.Fatalf("the source's chunk was dropped")
	}
}

// TestArtifactFetchFromUnreachableSourceFails: a stage must not hang because the
// node holding its input went away. The fetch fails promptly so the orchestrator
// can re-route, and leaves nothing behind in the pool.
func TestArtifactFetchFromUnreachableSourceFails(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "exec", "127.0.0.1:17966")
	pool := withArtifactPool(t, c)

	hash := strings.Repeat("cd", 32)
	start := time.Now()
	if _, err := c.FetchArtifact(ctx, "ghost", "t-missing-source", hash); err == nil {
		t.Fatalf("fetch from a disconnected node reported success")
	}
	if elapsed := time.Since(start); elapsed > artifactChunkTimeout {
		t.Fatalf("fetch took %s to notice an unreachable source", elapsed)
	}
	if hashes, err := pool.List(); err != nil || len(hashes) != 0 {
		t.Fatalf("pool holds %v after a failed fetch (%v)", hashes, err)
	}
	if _, busy := c.pendingArt.Load(artifactKey("t-missing-source", hash)); busy {
		t.Fatalf("the failed transfer was left registered, blocking a retry")
	}
}
