package core

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// pollArtifact polls the pool until the hash lands or the deadline passes —
// pushes stream asynchronously through the outbox flush, so assertions about
// arrival are always eventually-consistent.
func pollArtifact(t *testing.T, c *Core, hash string, want int64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if size, ok := c.artifacts.Has(hash); ok && size == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	size, ok := c.artifacts.Has(hash)
	t.Fatalf("artifact never landed: has=%v size=%d want=%d", ok, size, want)
}

// TestArtifactPushTransfersEndToEnd is the chunked fat-push (§8.3 v2) over a
// real connection: custody enqueued on the sender, staged on the receiver,
// verified into the pool, and the sender's row retired by the done verdict.
// 3.5 MiB of incompressible content spans four chunks plus a partial — small
// enough for a test, large enough to exercise resumable streaming.
func TestArtifactPushTransfersEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sender := newCoreWithNative(t, "push-a", "127.0.0.1:17970", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	receiver := newCoreWithNative(t, "push-b", "127.0.0.1:17971", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	senderPool := withArtifactPool(t, sender)
	recvPool := withArtifactPool(t, receiver)
	startPair(t, ctx, sender, receiver, "127.0.0.1:17970", "127.0.0.1:17971")

	const taskID = "t-push-roundtrip"
	participantTask(t, sender, taskID, sender.nodeID, []string{sender.nodeID})
	participantTask(t, receiver, taskID, sender.nodeID, []string{sender.nodeID, receiver.nodeID})

	m, err := senderPool.PackDir(artifactTree(t, 3584<<10))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := sender.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, ""); err != nil {
		t.Fatalf("index: %v", err)
	}
	// The receiver's authorization binds pushed hashes to the task that names
	// them — as a real delegate would via Inputs.
	if err := receiver.store.SetStageInputs(ctx, taskID, []bus.ArtifactRef{{Hash: m.Hash, Source: sender.nodeID}}); err != nil {
		t.Fatalf("bind input: %v", err)
	}
	ttl := time.Now().Add(time.Hour).Unix()
	sender.artifactPushEnqueue(ctx, receiver.nodeID, taskID, m.Hash, ttl)
	sender.outboxFlush(ctx, receiver.nodeID)

	pollArtifact(t, receiver, m.Hash, m.Size)

	// The done verdict retired the sender's custody row.
	var rows int
	if err := sender.db.QueryRowContext(ctx,
		`SELECT count(*) FROM artifact_push_outbox WHERE peer = ? AND hash = ?`,
		receiver.nodeID, m.Hash).Scan(&rows); err != nil {
		t.Fatalf("count push rows: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for rows != 0 && time.Now().Before(deadline) {
		_ = sender.db.QueryRowContext(ctx,
			`SELECT count(*) FROM artifact_push_outbox WHERE peer = ? AND hash = ?`,
			receiver.nodeID, m.Hash).Scan(&rows)
		time.Sleep(50 * time.Millisecond)
	}
	if rows != 0 {
		t.Fatalf("push custody row never retired (%d rows)", rows)
	}

	// The transferred archive must extract into a usable tree.
	dst := recvPool.Root() + "-extract"
	if _, err := recvPool.Extract(m.Hash, dst); err != nil {
		t.Fatalf("extract pushed artifact: %v", err)
	}
	if _, err := os.Stat(dst + "/src/train.py"); err != nil {
		t.Fatalf("extracted tree missing script: %v", err)
	}
}

// TestArtifactPushResumesFromReceiverOffset proves the resumable half of the
// protocol: when the receiver already staged the first half of the archive,
// the sender's stream honours the reported waterline — the transfer
// completes with the same end state instead of restarting from byte zero.
func TestArtifactPushResumesFromReceiverOffset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sender := newCoreWithNative(t, "push-c", "127.0.0.1:17972", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	receiver := newCoreWithNative(t, "push-d", "127.0.0.1:17973", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	senderPool := withArtifactPool(t, sender)
	recvPool := withArtifactPool(t, receiver)
	startPair(t, ctx, sender, receiver, "127.0.0.1:17972", "127.0.0.1:17973")

	const taskID = "t-push-resume"
	participantTask(t, receiver, taskID, sender.nodeID, []string{sender.nodeID, receiver.nodeID})

	m, err := senderPool.PackDir(artifactTree(t, 1536<<10))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := sender.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, ""); err != nil {
		t.Fatalf("index: %v", err)
	}
	if err := receiver.store.SetStageInputs(ctx, taskID, []bus.ArtifactRef{{Hash: m.Hash, Source: sender.nodeID}}); err != nil {
		t.Fatalf("bind input: %v", err)
	}

	// The receiver stages the first chunk-and-a-half out of band — the state
	// a link drop would have left behind.
	head := make([]byte, bus.ArtifactChunkBytes+bus.ArtifactChunkBytes/2)
	sf, err := senderPool.Open(m.Hash)
	if err != nil {
		t.Fatalf("open sender copy: %v", err)
	}
	n, err := sf.ReadAt(head, 0)
	sf.Close()
	if err != nil {
		t.Fatalf("read head: %v", err)
	}
	if _, err := recvPool.StageChunk(m.Hash, 0, head[:n], m.Size); err != nil {
		t.Fatalf("pre-stage: %v", err)
	}
	pre, ok := recvPool.StagedProgress(m.Hash)
	if !ok || pre.ReceivedThrough != int64(n) {
		t.Fatalf("pre-stage waterline = %+v ok=%v", pre, ok)
	}

	ttl := time.Now().Add(time.Hour).Unix()
	sender.artifactPushEnqueue(ctx, receiver.nodeID, taskID, m.Hash, ttl)
	sender.outboxFlush(ctx, receiver.nodeID)

	pollArtifact(t, receiver, m.Hash, m.Size)
	// The receiver staged a head start, so the sender's stream had to honour
	// the reported waterline rather than replay from zero — proven by the
	// transfer completing at all: a from-scratch resend would have left the
	// staging file's tail unfilled and the commit unverifiable.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var rows int
		if err := sender.db.QueryRowContext(ctx,
			`SELECT count(*) FROM artifact_push_outbox WHERE peer = ? AND hash = ?`,
			receiver.nodeID, m.Hash).Scan(&rows); err == nil && rows == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("custody row never retired after resumed push")
}

// TestDtnTaskParksUntilPushedInputsArrive is the receiving side of §8.3: a
// dtn delegate whose inputs are not yet local must not fail — the bundle
// promises the payload follows — so the task parks in waiting_context and
// resumes the moment the pushed artifact verifies.
func TestDtnTaskParksUntilPushedInputsArrive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sender := newCoreWithNative(t, "push-e", "127.0.0.1:17974", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	worker := newCoreWithNative(t, "push-f", "127.0.0.1:17975", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	senderPool := withArtifactPool(t, sender)
	withArtifactPool(t, worker)
	startPair(t, ctx, sender, worker, "127.0.0.1:17974", "127.0.0.1:17975")

	m, err := senderPool.PackDir(artifactTree(t, 512<<10))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	const taskID = "t-push-park"
	if err := sender.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, ""); err != nil {
		t.Fatalf("index: %v", err)
	}

	// The dtn delegate arrives with the input declared but not yet present.
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, sender.nodeID, "d-park", bus.TaskDelegatePayload{
		TaskID:    taskID,
		Title:     "dtn pushed-input task",
		Intent:    "x",
		Requires:  []string{"sys:info"},
		Chain:     []string{sender.nodeID},
		Transport: "dtn",
		Inputs:    []bus.ArtifactRef{{Hash: m.Hash, Source: sender.nodeID}},
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	worker.handleDelegate(ctx, env)

	tk, err := worker.store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("task missing: %v", err)
	}
	if tk.State != StateWaitingCtx {
		t.Fatalf("task state = %s, want waiting_context (parked for push)", tk.State)
	}
	if _, ok := worker.pendingCtx.Load(taskID); !ok {
		t.Fatal("no pending entry for the parked task")
	}

	// Now the payload follows: the sender streams it through the outbox.
	ttl := time.Now().Add(time.Hour).Unix()
	sender.artifactPushEnqueue(ctx, worker.nodeID, taskID, m.Hash, ttl)
	sender.outboxFlush(ctx, worker.nodeID)

	// The task wakes and runs to completion — the native ability is a real
	// command, so 'done' is reachable rather than a synthetic assert.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := worker.store.Get(ctx, taskID)
		if err == nil && Terminal(got.State) {
			if got.State != StateDone {
				t.Fatalf("task finished as %s, want done", got.State)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	got, err := worker.store.Get(ctx, taskID)
	if err != nil || !Terminal(got.State) {
		t.Fatalf("task never resumed: state=%v err=%v", got, err)
	}
	if _, ok := worker.pendingCtx.Load(taskID); ok {
		t.Fatal("pending entry survived the resume")
	}
}

// TestPushEnqueueKeepsProgressOnRepush: a repeated enqueue for the same
// (peer, hash) must not reset the acknowledged waterline — custody is
// cumulative, or a re-delegation would restart a nearly-finished transfer.
func TestPushEnqueueKeepsProgressOnRepush(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "push-g", "")
	pool := withArtifactPool(t, c)

	m, err := pool.PackDir(artifactTree(t, 64<<10))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	ttl := time.Now().Add(time.Hour).Unix()
	c.artifactPushEnqueue(ctx, "peer-x", "t-1", m.Hash, ttl)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE artifact_push_outbox SET sent_through = 100, acked_through = 90 WHERE peer = ? AND hash = ?`,
		"peer-x", m.Hash); err != nil {
		t.Fatalf("seed progress: %v", err)
	}
	c.artifactPushEnqueue(ctx, "peer-x", "t-2", m.Hash, ttl)
	var sent, acked int64
	if err := c.db.QueryRowContext(ctx,
		`SELECT sent_through, acked_through FROM artifact_push_outbox WHERE peer = ? AND hash = ?`,
		"peer-x", m.Hash).Scan(&sent, &acked); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if sent != 100 || acked != 90 {
		t.Fatalf("re-enqueue reset progress to sent=%d acked=%d", sent, acked)
	}
	var taskID string
	if err := c.db.QueryRowContext(ctx,
		`SELECT task_id FROM artifact_push_outbox WHERE peer = ? AND hash = ?`,
		"peer-x", m.Hash).Scan(&taskID); err != nil {
		t.Fatalf("read task_id: %v", err)
	}
	if taskID != "t-2" {
		t.Fatalf("task_id = %q, want the latest enqueue t-2", taskID)
	}
}
