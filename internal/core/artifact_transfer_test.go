// SPDX-License-Identifier: AGPL-3.0-or-later

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
	if _, err := c.store.CreateWithID(context.Background(), taskID, "", "", "train a model", owner, chain, true); err != nil {
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
	// A real producer indexes the artifact under its task before serving it
	// (packStageOutput); the authorization binding requires that link.
	if err := producer.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, ""); err != nil {
		t.Fatalf("index artifact: %v", err)
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
	if err := producer.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, ""); err != nil {
		t.Fatalf("index artifact: %v", err)
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
	// The pool index ties the artifact to the task — the same record a real
	// producer writes before serving it.
	if err := c.store.RecordArtifact(ctx, m.Hash, m.Size, "t-authz", ""); err != nil {
		t.Fatalf("index artifact: %v", err)
	}

	// reply() needs a connection to the requester; there is none here, so the
	// refusal is observed as "nothing was read" rather than as a chunk. What
	// matters is that the authorization decision, not the transport, is what
	// stops it.
	if c.artifactPeerAuthorized(ctx, "t-authz", m.Hash, "mallory") {
		t.Fatalf("a node outside the chain was authorized to pull artifacts")
	}
	if !c.artifactPeerAuthorized(ctx, "t-authz", m.Hash, "owner-node") {
		t.Fatalf("the task owner was refused its own artifact")
	}
	// An unknown task authorizes nobody: a peer must not be able to probe the
	// pool by inventing a task id.
	if c.artifactPeerAuthorized(ctx, "t-does-not-exist", m.Hash, "owner-node") {
		t.Fatalf("an unknown task authorized a pull")
	}
	// Participation alone is not enough: the hash must belong to the task, or
	// one task's participant could enumerate every artifact the pool holds.
	if c.artifactPeerAuthorized(ctx, "t-authz", strings.Repeat("ef", 32), "owner-node") {
		t.Fatalf("a participant was authorized for an artifact the task does not own")
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

// TestArtifactFetchPastEndAnswersEOF: the fetch window opens speculative
// requests past the archive's end before Total is known — on a small
// artifact that means offsets like 2 MiB for a 1 KiB pack. The serving side
// must answer those with the empty EOF marker the protocol promises
// (OK:true, EOF:true, no data), not the WARN-plus-refusal it logged before,
// which flooded operator logs on every small-artifact pull.
func TestArtifactFetchPastEndAnswersEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	consumer := newCoreWithNative(t, "cons", "127.0.0.1:17970", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	producer := newCoreWithNative(t, "prod", "127.0.0.1:17971", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	withArtifactPool(t, consumer)
	producerPool := withArtifactPool(t, producer)
	startPair(t, ctx, consumer, producer, "127.0.0.1:17970", "127.0.0.1:17971")

	const taskID = "t-past-end"
	participantTask(t, consumer, taskID, consumer.nodeID, []string{consumer.nodeID})
	participantTask(t, producer, taskID, consumer.nodeID, []string{consumer.nodeID, producer.nodeID})

	m, err := producerPool.PackDir(artifactTree(t, 1024))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if m.Size > bus.ArtifactChunkBytes {
		t.Fatalf("test artifact is %d bytes — the past-end case needs it under one chunk", m.Size)
	}
	if err := producer.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, ""); err != nil {
		t.Fatalf("index artifact: %v", err)
	}

	// The transfer the reply must find: source is the producer, exactly what
	// a real FetchArtifact registers before its first request.
	tr := &artifactTransfer{source: producer.nodeID, chunks: make(chan bus.ArtifactChunkPayload, 4)}
	consumer.pendingArt.Store(artifactKey(taskID, m.Hash), tr)

	env, err := bus.NewEnvelope(bus.MsgArtifactFetch, consumer.nodeID, "m-past-end",
		bus.ArtifactFetchPayload{TaskID: taskID, Hash: m.Hash, Offset: m.Size + bus.ArtifactChunkBytes})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := consumer.sendTo(producer.nodeID, env); err != nil {
		t.Fatalf("send fetch: %v", err)
	}
	select {
	case cp := <-tr.chunks:
		if !cp.OK || !cp.EOF || len(cp.Data) != 0 || cp.Total != m.Size || cp.Offset != m.Size+bus.ArtifactChunkBytes {
			t.Fatalf("past-end reply = %+v, want OK EOF empty-data total=%d", cp, m.Size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no chunk answered the past-end probe")
	}

	// A denied probe is the control: an offset that wraps negative or a hash
	// the producer does not hold still gets OK:false — the EOF marker is for
	// legitimate over-read, not a blanket accept.
	env2, err := bus.NewEnvelope(bus.MsgArtifactFetch, consumer.nodeID, "m-missing",
		bus.ArtifactFetchPayload{TaskID: taskID, Hash: strings.Repeat("ff", 32), Offset: 0})
	if err != nil {
		t.Fatalf("envelope2: %v", err)
	}
	tr2 := &artifactTransfer{source: producer.nodeID, chunks: make(chan bus.ArtifactChunkPayload, 4)}
	consumer.pendingArt.Store(artifactKey(taskID, strings.Repeat("ff", 32)), tr2)
	if err := consumer.sendTo(producer.nodeID, env2); err != nil {
		t.Fatalf("send fetch2: %v", err)
	}
	select {
	case cp := <-tr2.chunks:
		if cp.OK {
			t.Fatalf("unheld artifact answered OK: %+v", cp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no denial answered the unheld-hash fetch")
	}
}

// TestArtifactFetchRelaysAcrossHops is the partial-mesh data plane: the
// consumer (pin-dest) is two hops from the producer (pin-root) and can only
// reach the relay. The direct pull fails — as it must — and the pull through
// the chain predecessor succeeds: the relay forwards the fetch upstream and
// fans the chunks back, with each hop's authorization checked against its own
// task row (the chain is the participant list at every level).
func TestArtifactFetchRelaysAcrossHops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	root := newCoreWithNative(t, "pin-root", "127.0.0.1:17980", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	relay := newCoreWithNative(t, "pin-relay", "127.0.0.1:17981", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	dest := newCoreWithNative(t, "pin-dest", "127.0.0.1:17982", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	rootPool := withArtifactPool(t, root)
	destPool := withArtifactPool(t, dest)
	withArtifactPool(t, relay)

	for _, c := range []*Core{root, relay, dest} {
		if err := c.Register(ctx); err != nil {
			t.Fatalf("register %s: %v", c.nodeID, err)
		}
	}
	go func() { _ = root.Listen(ctx, "127.0.0.1:17980") }()
	go func() { _ = relay.Listen(ctx, "127.0.0.1:17981") }()
	go func() { _ = dest.Listen(ctx, "127.0.0.1:17982") }()
	time.Sleep(150 * time.Millisecond)
	if err := root.DialPeer(ctx, "127.0.0.1:17981"); err != nil {
		t.Fatalf("root dial: %v", err)
	}
	if err := relay.DialPeer(ctx, "127.0.0.1:17982"); err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	waitPeer(t, root, "pin-relay")
	waitPeer(t, relay, "pin-root")
	waitPeer(t, relay, "pin-dest")
	waitPeer(t, dest, "pin-relay")

	// The producer holds the artifact under its own row; every hop's row
	// carries the same task id and chain, as a real delegation leaves them.
	const taskID = "t-relay-pull"
	m, err := rootPool.PackDir(artifactTree(t, 4096))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	participantTask(t, root, taskID, "pin-root", []string{"pin-root"})
	// The origin's own row names its dispatch target — the first hop — which
	// is how the relay is a participant on the producer's row (its own chain
	// is just itself, as a real origin's is).
	if err := root.store.Queue(ctx, taskID, "pin-root"); err != nil {
		t.Fatalf("queue on root: %v", err)
	}
	if err := root.store.Dispatch(ctx, taskID, "pin-root", "pin-relay"); err != nil {
		t.Fatalf("dispatch on root: %v", err)
	}
	participantTask(t, relay, taskID, "pin-root", []string{"pin-root", "pin-relay", "pin-dest"})
	participantTask(t, dest, taskID, "pin-root", []string{"pin-root", "pin-relay", "pin-dest"})
	if err := root.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, ""); err != nil {
		t.Fatalf("index artifact: %v", err)
	}
	if err := root.store.SetOutputArtifact(ctx, taskID, m.Hash); err != nil {
		t.Fatalf("set output artifact: %v", err)
	}
	// The relay's row must bind the hash too: authorization is per-hop and
	// reads each node's own row, so the input ref travels as the stage's
	// declared input exactly as a real delegation persists it.
	if err := relay.store.SetStageInputs(ctx, taskID,
		[]bus.ArtifactRef{{Hash: m.Hash, Source: "pin-root"}}); err != nil {
		t.Fatalf("set relay inputs: %v", err)
	}

	// The direct pull is impossible on this topology — that is the premise.
	if _, err := dest.FetchArtifact(ctx, "pin-root", taskID, m.Hash); err == nil {
		t.Fatal("dest pulled from a node it has no link to")
	}
	// Through the relay hop it works: fetch from the predecessor, which
	// forwards upstream and fans the chunks back.
	got, err := dest.FetchArtifact(ctx, "pin-relay", taskID, m.Hash)
	if err != nil {
		t.Fatalf("relayed fetch: %v", err)
	}
	if got.Hash != m.Hash || got.Size != m.Size {
		t.Fatalf("relayed fetch got %s (%d), want %s (%d)", got.Hash, got.Size, m.Hash, m.Size)
	}
	if size, ok := destPool.Has(m.Hash); !ok || size != m.Size {
		t.Fatalf("consumer pool has %d, %v after the relayed transfer", size, ok)
	}
	// The relay cached nothing of its own — it moved bytes, it did not
	// accumulate them (the requester's pool is the destination).
	if _, ok := relay.Artifacts().Has(m.Hash); ok {
		t.Fatal("relay kept a copy — relaying must not double-store")
	}
}

func TestImportFatBundleArtifacts(t *testing.T) {
	ctx := context.Background()
	tree := artifactTree(t, 2048)

	sourcePool := artifact.NewStore(filepath.Join(t.TempDir(), "source-artifacts"))
	manifest, err := sourcePool.PackDir(tree)
	if err != nil {
		t.Fatalf("pack tree: %v", err)
	}

	data, err := os.ReadFile(sourcePool.Path(manifest.Hash))
	if err != nil {
		t.Fatalf("read packed artifact: %v", err)
	}

	receiver := newCore(t, "receiver", "127.0.0.1:17967")
	recPool := withArtifactPool(t, receiver)

	// Before import, receiver does not have it
	if _, ok := recPool.Has(manifest.Hash); ok {
		t.Fatalf("receiver should not have artifact before import")
	}

	// Push-based import (Fat Bundle)
	imported, err := receiver.ImportFatBundleArtifacts(ctx, []bus.FatBundleArtifact{
		{Hash: manifest.Hash, Data: data},
	})
	if err != nil {
		t.Fatalf("import fat bundle: %v", err)
	}
	if len(imported) != 1 || imported[0].Hash != manifest.Hash {
		t.Fatalf("unexpected imported manifests: %v", imported)
	}

	// Now receiver has it directly in the pool without any network pull
	if size, ok := recPool.Has(manifest.Hash); !ok || size != manifest.Size {
		t.Fatalf("artifact not present or size mismatch: ok=%v, size=%d, want %d", ok, size, manifest.Size)
	}
}
