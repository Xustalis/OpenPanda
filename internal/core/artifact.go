// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
)

// Artifact transfer is the data plane the delegation chain was missing. Until it
// existed a delegated task carried only a *path*, which means nothing on the
// machine that receives it: the Mac could develop a training script and the
// Windows node could have the GPU, but the script never crossed the gap.
//
// The transfer is a pull, in chunks, driven by the node that needs the artifact:
//
//   - Pull, because the consumer is the only node that knows whether it already
//     holds the artifact — content addressing makes a re-delegation to a node
//     that has the bytes cost nothing on the wire.
//   - Chunked, because bus.readLimit caps one frame at 4 MiB while an artifact is
//     a build tree or a trained model.
//   - Requester-driven, because then a lost or corrupt chunk is a re-request at a
//     known offset rather than a failed transfer: the receiver's own offset is
//     the resume point, and no sender-side session state has to survive.
//
// Verification is the load-bearing part. Chunks stream into Store.Put, which
// hashes them and only gives the artifact its content-addressed name if the hash
// matches what was asked for. A transfer that is truncated, reordered or tampered
// with therefore leaves nothing in the pool: the next stage can never consume a
// half-received archive as if it were verified input.
const (
	// artifactChunkTimeout bounds the wait for one chunk before the fetch for
	// that offset is repeated. It is per chunk, not per transfer, so a slow link
	// does not fail a large artifact.
	artifactChunkTimeout = 30 * time.Second

	// artifactChunkRetries is how many times one offset is re-requested before
	// the transfer gives up and the caller tries a different node.
	artifactChunkRetries = 2

	// artifactTransferBudget is the ceiling on a whole transfer, so a peer that
	// answers every fetch with a stale or empty chunk cannot park a goroutine
	// forever by keeping the per-chunk timer armed.
	artifactTransferBudget = 30 * time.Minute

	// artifactFetchWindow is the number of chunk requests allowed in flight
	// at once. Stop-and-wait made every chunk cost a full round trip — a
	// 500 MiB tree was ~500 serialized RTTs, so a WAN link capped the
	// transfer at chunkSize/RTT no matter its bandwidth. Four in flight
	// keeps a ~4 MiB pipe filling without a flood: large enough that a
	// 100 ms link reaches line rate, small enough that a refusal still
	// costs little.
	artifactFetchWindow = 4
)

// artifactTransfer is one in-flight inbound pull. source is the node the fetches
// were sent to, and the only node whose chunks are accepted: any authenticated
// peer could otherwise answer a fetch it did not receive and feed the transfer
// bytes of its choosing (the same reasoning as pendingContext.source, P1-11).
type artifactTransfer struct {
	source string
	chunks chan bus.ArtifactChunkPayload
}

// chunkBufPool reuses the 1 MiB read buffers the artifact plane fills with
// Store.ReadAt and hands to the JSON encoder. A buffer is live only until its
// envelope is marshaled (NewEnvelope copies the bytes into the JSON payload),
// so pooling removes one 1 MiB allocation per transferred chunk — the data
// plane's dominant transient allocation, and a chunk transfer can run for
// thousands of chunks.
var chunkBufPool = sync.Pool{
	New: func() any { b := make([]byte, bus.ArtifactChunkBytes); return &b },
}

// getChunkBuf borrows a pooled chunk buffer; release must run after the
// buffer's last reader (the envelope marshal).
func getChunkBuf() ([]byte, func()) {
	bp := chunkBufPool.Get().(*[]byte)
	return *bp, func() { chunkBufPool.Put(bp) }
}

// SetArtifactStore attaches the node's artifact pool. Nil leaves the node without
// a data plane: it can still delegate and execute, but a stage whose input is an
// artifact hash will fail rather than silently run on missing input.
func (c *Core) SetArtifactStore(s *artifact.Store) { c.artifacts = s }

// Artifacts returns the node's artifact pool, or nil if none is configured.
func (c *Core) Artifacts() *artifact.Store { return c.artifacts }

// ImportFatBundleArtifacts imports an array of proactive bundled artifacts directly into the node's
// local artifact pool, verifying SHA256 integrity and eliminating round-trip pulls (whitepaper §8.3).
func (c *Core) ImportFatBundleArtifacts(ctx context.Context, artifacts []bus.FatBundleArtifact) ([]artifact.Manifest, error) {
	if c.artifacts == nil {
		return nil, errors.New("core: no artifact pool configured")
	}
	var manifests []artifact.Manifest
	for _, a := range artifacts {
		if size, ok := c.artifacts.Has(a.Hash); ok {
			manifests = append(manifests, artifact.Manifest{Hash: a.Hash, Size: size})
			continue
		}
		r := bytes.NewReader(a.Data)
		m, err := c.artifacts.Put(a.Hash, r)
		if err != nil {
			return nil, fmt.Errorf("import fat bundle artifact %s: %w", a.Hash, err)
		}
		manifests = append(manifests, m)
		c.EvTrace(ctx, "", EvArtifactTransfer, map[string]any{
			"from_node":  "fat_bundle_push",
			"to_node":    c.nodeID,
			"hash":       a.Hash,
			"ok":         true,
			"size_bytes": m.Size,
			"mode":       "push",
		})
	}
	return manifests, nil
}

// fatBundleCap bounds the total bytes one delegate's BundledArtifacts may
// carry (§8.3). JSON base64 inflates a byte to 4/3 on the wire and a frame
// must stay under readLimit, so 2 MiB of raw artifact stays comfortably
// deliverable inside one envelope.
const fatBundleCap = 2 << 20

// attachFatBundle populates p.BundledArtifacts with the artifacts the payload
// already references — the context snapshot and every declared input — so a
// DTN delegate arrives self-contained and the executor can start with zero
// network round trips (§8.3). Hashes this node does not hold are skipped:
// content addressing means the receiver can still pull them later from any
// node that does, and a missing bundle entry degrades to the fetch path
// rather than failing the delegation.
//
// The inline cap makes anything past fatBundleCap a deferred push candidate:
// the returned hashes are artifacts this node holds that did not fit the
// envelope and should stream to the peer on the push lane instead. The
// caller enqueues one custody row per hash — small inputs still ride the
// delegate for free while a multi-hundred-MiB pack follows in chunks.
func (c *Core) attachFatBundle(ctx context.Context, p *bus.TaskDelegatePayload) []string {
	if c.artifacts == nil {
		return nil
	}
	total := 0
	var deferred []string
	add := func(hash string) {
		if hash == "" {
			return
		}
		for _, b := range p.BundledArtifacts {
			if b.Hash == hash {
				return // already bundled by an earlier step
			}
		}
		for _, d := range deferred {
			if d == hash {
				return
			}
		}
		f, err := c.artifacts.Open(hash)
		if err != nil {
			return // not held: nothing to inline or push
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, int64(fatBundleCap-total)+1))
		if err != nil || len(data) == 0 {
			return
		}
		if len(data) > fatBundleCap-total {
			// Held but does not fit the envelope: custody for the chunked
			// push lane rather than silence.
			deferred = append(deferred, hash)
			return
		}
		p.BundledArtifacts = append(p.BundledArtifacts, bus.FatBundleArtifact{Hash: hash, Data: data})
		total += len(data)
	}
	add(p.ContextHash)
	for _, in := range p.Inputs {
		add(in.Hash)
	}
	return deferred
}

// artifactKey names an in-flight transfer. The pair is the key, not the hash
// alone: two stages of different plans may legitimately pull the same artifact,
// and each drives its own offset.
func artifactKey(taskID, hash string) string { return taskID + "|" + hash }

// FetchArtifact pulls the artifact named by hash from source into this node's
// pool and returns its manifest. An artifact the node already holds is returned
// without any bytes on the wire — the hash is proof enough, which is what makes
// re-delegating a stage to a node that ran an earlier one nearly free.
//
// The thin wrapper exists so every exit path — the local-hit fast return, every
// abort inside the transfer loop, and success — lands one artifact_transfer
// trace event without sprinkling emissions through the loop.
func (c *Core) FetchArtifact(ctx context.Context, source, taskID, hash string) (artifact.Manifest, error) {
	return c.FetchArtifactRef(ctx, source, taskID, bus.ArtifactRef{Hash: hash})
}

// FetchArtifactRef is FetchArtifact with the full input reference: a ref that
// carries the orchestrator's OfTask/Grant/Issuer asks the producer to serve on
// the strength of the signed grant rather than on the consumer's own task row
// — the difference that makes direct stage-to-stage handoff possible when the
// producer never saw the consumer's task.
func (c *Core) FetchArtifactRef(ctx context.Context, source, taskID string, ref bus.ArtifactRef) (artifact.Manifest, error) {
	start := time.Now()
	m, err := c.fetchArtifact(ctx, source, taskID, ref)
	ev := map[string]any{
		"from_node":  source,
		"to_node":    c.nodeID,
		"hash":       ref.Hash,
		"ok":         err == nil,
		"elapsed_ms": time.Since(start).Milliseconds(),
	}
	if err != nil {
		ev["error"] = err.Error()
	} else {
		ev["size_bytes"] = m.Size
	}
	c.EvTrace(ctx, taskID, EvArtifactTransfer, ev)
	return m, err
}

func (c *Core) fetchArtifact(ctx context.Context, source, taskID string, ref bus.ArtifactRef) (artifact.Manifest, error) {
	hash := ref.Hash
	if c.artifacts == nil {
		return artifact.Manifest{}, errors.New("core: no artifact pool configured")
	}
	if source == "" {
		return artifact.Manifest{}, errors.New("core: no source node for artifact fetch")
	}
	if size, ok := c.artifacts.Has(hash); ok {
		return artifact.Manifest{Hash: hash, Size: size}, nil
	}

	key := artifactKey(taskID, hash)
	tr := &artifactTransfer{source: source, chunks: make(chan bus.ArtifactChunkPayload, artifactFetchWindow)}
	if _, busy := c.pendingArt.LoadOrStore(key, tr); busy {
		return artifact.Manifest{}, fmt.Errorf("core: artifact %s for task %s is already being fetched", hash, taskID)
	}
	defer c.pendingArt.Delete(key)

	// The pool's writer runs the verification, so the bytes are hashed as they
	// arrive and never buffered whole in memory. Closing the pipe with an error
	// is how an aborted transfer guarantees nothing lands in the pool.
	pr, pw := io.Pipe()
	type putResult struct {
		m   artifact.Manifest
		err error
	}
	done := make(chan putResult, 1)
	go func() {
		m, err := c.artifacts.Put(hash, pr)
		// Unblock the writer if Put returned before the stream ended (a bad
		// hash, a full disk): otherwise the fetch loop would block on Write.
		pr.CloseWithError(err)
		done <- putResult{m: m, err: err}
	}()
	abort := func(err error) (artifact.Manifest, error) {
		pw.CloseWithError(err)
		<-done
		return artifact.Manifest{}, err
	}

	budget := time.NewTimer(artifactTransferBudget)
	defer budget.Stop()

	var total int64 = -1
	var writeOff int64 // next byte offset the pipe is waiting for
	nextOff := int64(0)
	eofSeen := false
	finished := false
	inflight := map[int64]time.Time{} // requested offset -> last send time
	retries := map[int64]int{}
	have := map[int64]bus.ArtifactChunkPayload{} // received, awaiting its write turn

	// checkTotal applies the two size invariants every chunk repeats: the
	// advertised archive length must fit this pool, and it must never
	// change mid-transfer — the archive is immutable once named by its
	// hash, so a moving Total means the peer is not serving what it claims.
	checkTotal := func(t int64) error {
		if lim := c.artifacts.Limit(); lim > 0 && t > lim {
			return fmt.Errorf("%w: %s advertises %d bytes", artifact.ErrTooLarge, hash, t)
		}
		if lim := c.artifacts.Limit(); lim <= 0 {
			// Unbounded pool: the advertised total is still attacker input,
			// and the volumes are the bound — refuse before the first byte
			// lands when no pool root could hold the archive.
			if room, ok := c.artifacts.MaxStorable(); ok && t > room {
				return fmt.Errorf("%w: %s advertises %d bytes with %d usable", artifact.ErrNoSpace, hash, t, room)
			}
		}
		if total < 0 {
			total = t
		} else if t != total {
			return fmt.Errorf("core: artifact %s changed size mid-transfer (%d then %d)", hash, total, t)
		}
		return nil
	}

	for !finished {
		// Refill the request window. Offsets are server-authoritative — a
		// fetch at O answers the bytes [O, O+ArtifactChunkBytes) — so the
		// next request offsets are known before the first reply teaches us
		// Total; requests past Total come back as empty EOF markers the
		// drain discards. When the window is empty but the stream is not
		// finished, probe the frontier once: the only legitimate missing
		// reply is a lost EOF marker, and re-asking writeOff re-elicits it.
		for len(inflight) < artifactFetchWindow && !eofSeen {
			off := nextOff
			if total >= 0 && nextOff >= total {
				if len(inflight) > 0 {
					break
				}
				off = writeOff
			}
			if err := c.sendArtifactFetch(source, taskID, ref, off); err != nil {
				return abort(fmt.Errorf("core: request artifact %s at %d: %w", hash, off, err))
			}
			inflight[off] = time.Now()
			if off == nextOff {
				nextOff += bus.ArtifactChunkBytes
			}
			if off == writeOff && total >= 0 && nextOff >= total {
				break // frontier probe sent; wait for its answer
			}
		}
		if len(inflight) == 0 {
			// Every request was answered but the drain could not finish:
			// a gap the peer never made good on (an empty non-EOF answer
			// aborts at receive time, so only a skipped offset lands here).
			// Nothing more can legitimately arrive — waiting would just
			// burn the transfer budget.
			return abort(fmt.Errorf("core: artifact %s stalled at %d bytes with all requests answered", hash, writeOff))
		}
		// Wake when the oldest unanswered request ages out; sooner arrivals
		// land through the channel case.
		earliest := time.Now()
		for _, t := range inflight {
			if t.Before(earliest) {
				earliest = t
			}
		}
		wakeIn := artifactChunkTimeout - time.Since(earliest)
		if wakeIn < 0 {
			wakeIn = 0
		}
		wait := time.NewTimer(wakeIn)
		select {
		case <-ctx.Done():
			wait.Stop()
			return abort(ctx.Err())
		case <-budget.C:
			wait.Stop()
			return abort(fmt.Errorf("core: artifact %s exceeded its transfer budget at %d bytes", hash, writeOff))
		case res := <-done:
			// Put gave up on its own (invalid hash, write failure).
			wait.Stop()
			pw.CloseWithError(res.err)
			if res.err == nil {
				res.err = errors.New("core: artifact pool closed the transfer early")
			}
			return artifact.Manifest{}, res.err
		case <-wait.C:
			// Re-request every expired offset; each keeps its own retry
			// count, so one black-holed range fails without resending the
			// answers already in flight.
			for off, t := range inflight {
				if time.Since(t) < artifactChunkTimeout {
					continue
				}
				retries[off]++
				if retries[off] > artifactChunkRetries {
					return abort(fmt.Errorf("core: no chunk of artifact %s at offset %d after %d attempts", hash, off, artifactChunkRetries+1))
				}
				if err := c.sendArtifactFetch(source, taskID, ref, off); err != nil {
					return abort(fmt.Errorf("core: request artifact %s at %d: %w", hash, off, err))
				}
				inflight[off] = time.Now()
			}
		case p := <-tr.chunks:
			wait.Stop()
			if !p.OK {
				reason := p.Reason
				if reason == "" {
					reason = "declined"
				}
				return abort(fmt.Errorf("core: %s cannot serve artifact %s: %s", source, hash, reason))
			}
			if _, ok := inflight[p.Offset]; !ok {
				// A stale chunk — the answer to a request already served,
				// or one nobody asked — must not be mistaken for a gap
				// fill. Dropping keeps the invariant that only requested
				// offsets ever reach the pipe.
				c.logger.Debug("stale artifact chunk", "hash", hash, "offset", p.Offset)
				continue
			}
			delete(inflight, p.Offset)
			if err := checkTotal(p.Total); err != nil {
				return abort(err)
			}
			if p.EOF {
				eofSeen = true
			}
			have[p.Offset] = p
			// Drain every contiguous chunk the pipe can take. Bytes hash
			// as they stream (Store.Put reads the other end), so a
			// reordered arrival costs only its buffer slot.
			for {
				ch, ok := have[writeOff]
				if !ok {
					break
				}
				delete(have, writeOff)
				if len(ch.Data) > 0 {
					if _, err := pw.Write(ch.Data); err != nil {
						return abort(fmt.Errorf("core: buffer artifact %s: %w", hash, err))
					}
					writeOff += int64(len(ch.Data))
				}
				if ch.EOF {
					finished = true
					break
				}
				if len(ch.Data) == 0 {
					// Not EOF and no bytes: the stream cannot advance past
					// this offset, and re-asking would spin on the same
					// answer.
					return abort(fmt.Errorf("core: empty non-final chunk of artifact %s at %d", hash, writeOff))
				}
			}
		}
	}

	if err := pw.Close(); err != nil {
		return abort(fmt.Errorf("core: finish artifact %s: %w", hash, err))
	}
	res := <-done
	if res.err != nil {
		return artifact.Manifest{}, fmt.Errorf("core: verify artifact %s: %w", hash, res.err)
	}
	c.logger.Info("artifact fetched", "task", taskID, "hash", hash, "bytes", res.m.Size, "from", source)
	// Index what the pool now holds. Without this the artifacts table would only
	// ever list locally produced trees, and a pull would be invisible to listing
	// and pruning even though the bytes are on this disk.
	if manifestJSON, err := json.Marshal(res.m); err != nil {
		c.logger.Warn("marshal fetched manifest", "hash", hash, "err", err)
	} else if err := c.store.RecordArtifact(ctx, res.m.Hash, res.m.Size, taskID, string(manifestJSON)); err != nil {
		c.logger.Warn("index fetched artifact", "hash", hash, "err", err)
	}
	return res.m, nil
}

// sendArtifactFetch asks source for the chunk of the ref's hash starting at
// off. The ref's OfTask/Grant/Issuer ride along: a producer that cannot resolve
// the consumer's task id (it holds only its own stage's row) falls back to
// verifying the orchestrator-signed grant before serving.
func (c *Core) sendArtifactFetch(source, taskID string, ref bus.ArtifactRef, off int64) error {
	msgID, err := newUUID()
	if err != nil {
		return err
	}
	env, err := bus.NewEnvelope(bus.MsgArtifactFetch, c.nodeID, msgID, bus.ArtifactFetchPayload{
		TaskID: taskID, Hash: ref.Hash, Offset: off,
		OfTask: ref.OfTask, Grant: ref.Grant, Issuer: ref.Issuer,
	})
	if err != nil {
		return err
	}
	env.To = source
	return c.sendTo(source, env)
}

// handleArtifactFetch serves one chunk of a locally held artifact. Every answer
// is a chunk message, including a refusal: a requester that hears "not held" can
// ask another node, while silence would leave it retrying until its budget ran
// out.
func (c *Core) handleArtifactFetch(ctx context.Context, env bus.Envelope) {
	var p bus.ArtifactFetchPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad artifact_fetch", "err", err)
		return
	}
	deny := func(reason string) {
		_ = c.reply(ctx, env, bus.MsgArtifactChunk, bus.ArtifactChunkPayload{
			TaskID: p.TaskID, Hash: p.Hash, Offset: p.Offset, OK: false, Reason: reason,
		})
	}
	if c.artifacts == nil {
		deny("no artifact pool")
		return
	}
	// The pool holds other tasks' outputs, so participation in *this* task is
	// what authorizes the read — and the hash must belong to that task, or one
	// known task id would open the whole pool. Without the check any
	// authenticated peer could enumerate and download every artifact this node
	// has ever produced.
	//
	// The grant path is the direct stage-to-stage handoff: the consumer's task
	// id resolves to nothing here (this node produced a sibling stage and never
	// saw the consumer's row), but the orchestrator's signed grant attests the
	// pull was issued for this plan, this consumer, this hash.
	authorized := c.artifactPeerAuthorized(ctx, p.TaskID, p.Hash, env.From)
	if !authorized && p.OfTask != "" && p.Grant != "" && p.Issuer != "" {
		authorized = c.artifactGrantAuthorized(ctx, p)
	}
	if !authorized {
		c.logger.Warn("artifact_fetch from non-participant", "task", p.TaskID, "from", env.From)
		deny("not a participant in this task")
		return
	}
	size, ok := c.artifacts.Has(p.Hash)
	if !ok {
		deny("artifact not held")
		return
	}
	buf, release := getChunkBuf()
	defer release()
	n, eof, err := c.artifacts.ReadAt(p.Hash, p.Offset, buf)
	if err != nil {
		c.logger.Warn("read artifact chunk", "hash", p.Hash, "offset", p.Offset, "err", err)
		deny("read failed")
		return
	}
	cp := bus.ArtifactChunkPayload{
		TaskID: p.TaskID, Hash: p.Hash, Offset: p.Offset,
		Data: buf[:n], Total: size, EOF: eof, OK: true,
	}
	head := cp
	head.Data = nil
	if err := c.sendDataFrame(env.From, bus.MsgArtifactChunk, head, cp, buf[:n]); err != nil {
		c.logger.Warn("send artifact chunk", "hash", p.Hash, "offset", p.Offset, "err", err)
	}
}

// handleArtifactChunk hands one chunk to the transfer that asked for it. A chunk
// nobody is waiting for is dropped rather than buffered: an unsolicited chunk is
// either a late duplicate or an injection attempt, and neither has a transfer to
// belong to.
func (c *Core) handleArtifactChunk(ctx context.Context, env bus.Envelope) {
	var p bus.ArtifactChunkPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad artifact_chunk", "err", err)
		return
	}
	if len(env.BinaryPayload) > 0 {
		p.Data = env.BinaryPayload
	}
	v, ok := c.pendingArt.Load(artifactKey(p.TaskID, p.Hash))
	if !ok {
		c.logger.Debug("artifact_chunk for no transfer", "task", p.TaskID, "hash", p.Hash)
		return
	}
	tr, ok := v.(*artifactTransfer)
	if !ok {
		c.logger.Warn("bad pending artifact entry", "task", p.TaskID)
		return
	}
	if tr.source != "" && env.From != tr.source {
		// Only the node we asked may answer. Otherwise a peer could pre-empt the
		// real source with OK:false to fail the stage, or race in bytes of its
		// own choosing at the offset being awaited (P1-11).
		c.logger.Warn("artifact_chunk from non-source ignored", "task", p.TaskID,
			"from", env.From, "source", tr.source)
		return
	}
	select {
	case tr.chunks <- p:
	default:
		// The receiver is not waiting on this offset; it will re-request.
		c.logger.Debug("artifact chunk dropped", "task", p.TaskID, "offset", p.Offset)
	}
}

// artifactPeerAuthorized reports whether from may pull the artifact named by
// hash under taskID from this node. Two conditions must both hold: from is a
// participant in the task (the delegation chain is the participant list —
// owner, any node the task travelled through, or the node it is currently
// dispatched to), and the hash legitimately belongs to that task (its context
// snapshot, a declared input, its produced output, or an artifact this node
// indexed under it). Without the binding, one task's participant could
// enumerate and download every artifact the pool holds by quoting a single
// task id it belongs to. An unknown task authorizes nobody.
func (c *Core) artifactPeerAuthorized(ctx context.Context, taskID, hash, from string) bool {
	if from == "" || taskID == "" {
		return false
	}
	t, err := c.store.Get(ctx, taskID)
	if err != nil {
		return false
	}
	participant := from == t.OwnerNode || slices.Contains(t.Chain, from)
	if !participant {
		target, terr := c.store.DispatchTarget(ctx, taskID)
		participant = terr == nil && target != "" && from == target
	}
	// Direct Stage P2P Handoff: if the requesting peer participates in any stage of the same plan,
	// authorize direct artifact transfer without routing back through the plan root.
	if !participant && t.PlanID != "" {
		if stages, err := c.store.PlanStages(ctx, t.PlanID); err == nil {
			for _, st := range stages {
				if st.OwnerNode == from || slices.Contains(st.Chain, from) {
					participant = true
					break
				}
			}
		}
	}
	return participant && c.artifactBoundToTask(ctx, t, hash)
}

// artifactGrantAuthorized serves a fetch the participant check could not
// resolve: the consumer's task id means nothing on a producer that only holds
// its own stage's row. Instead the fetch names the producing task (OfTask — a
// row this node owns) plus the orchestrator's Ed25519 grant. The grant is
// valid only when the issuer is the orchestrator of record on the producing
// task's chain, the issuer's key is on file from its signed hello, and the
// signature binds this plan, this consumer task, this producing task and this
// hash — so a grant minted for one input can open exactly one artifact.
func (c *Core) artifactGrantAuthorized(ctx context.Context, p bus.ArtifactFetchPayload) bool {
	t, err := c.store.Get(ctx, p.OfTask)
	if err != nil || t.PlanID == "" {
		return false
	}
	issuer := t.OwnerNode
	if len(t.Chain) > 0 && t.Chain[0] != "" {
		issuer = t.Chain[0]
	}
	if issuer == "" || issuer != p.Issuer {
		return false
	}
	pub, ok := c.peerPubKey(issuer)
	if !ok {
		return false
	}
	if !bus.VerifyArtifactGrant(pub, t.PlanID, p.TaskID, p.OfTask, p.Hash, p.Grant) {
		return false
	}
	return c.artifactBoundToTask(ctx, t, p.Hash)
}

// artifactBoundToTask reports whether hash legitimately belongs to task t on
// this node: its context snapshot, one of its declared inputs, the tree it
// produced, or an entry this node indexed under the task when packing it for
// the delegation or pulling it as a consumer.
func (c *Core) artifactBoundToTask(ctx context.Context, t Task, hash string) bool {
	if hash == "" {
		return false
	}
	if t.ContextHash == hash || t.OutputArtifact == hash {
		return true
	}
	for _, in := range t.Inputs {
		if in.Hash == hash {
			return true
		}
	}
	indexed, err := c.store.ArtifactIndexedFor(ctx, hash, t.TaskID)
	if err != nil {
		// Fail closed: a lost index lookup must not widen authorization back
		// to "any participant pulls anything".
		c.logger.Warn("artifact index lookup", "hash", hash, "task", t.TaskID, "err", err)
		return false
	}
	return indexed
}
