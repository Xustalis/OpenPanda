package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/ctxstore"
)

// pendingContext is the execution context saved when a task pauses in
// waiting_context to fetch its full snapshot. handleContextAck uses it to
// resume execution. source is the node the fetch was sent to (chain[0]);
// a context_ack from any other node is rejected (P1-11).
//
// release frees the capacity reservation handleLocalDelegate took for this
// task. The parked row already occupies a CountActive slot (waiting_context),
// so the reservation is redundant while parked — but it must still be freed
// exactly once, either when the ack arrives (the row stays countable from
// there on) or when the fetch fails and the task dies.
type pendingContext struct {
	intent   string
	required []string
	ctxType  string
	source   string
	release  func()
	// waitHashes is non-empty when the entry parks a dtn task for pushed
	// artifacts rather than a context fetch: the input hashes still missing
	// from the pool. resume re-drives execution once every one of them
	// verifies — from a push commit, a pull, or a bundled import alike.
	waitHashes []string
	resume     func()
}

// packContext builds and stores the full context snapshot for a local-origin
// task, returning the wire hash and level. It is the MVP of pointer/summary/
// full packing (design doc §12.3):
//
//   - A caller that already packed the snapshot (ContextHash set) is trusted
//     and passed through.
//   - A file task with a known repo path is packed into a FileContext snapshot
//     and cached locally, advertised as "pointer".
//   - Everything else is "summary": the intent/spec already on the wire is the
//     whole context, and no snapshot transfer occurs.
func (c *Core) packContext(ctx context.Context, in TaskInput) (hash, level string, err error) {
	if in.ContextHash != "" {
		lvl := in.ContextLevel
		if lvl == "" {
			lvl = "pointer"
		}
		return in.ContextHash, lvl, nil
	}
	if in.ContextType == "file" && in.RepoPath != "" {
		fc := commander.FileContext{Type: "file", Repo: in.RepoPath}
		data, err := json.Marshal(fc)
		if err != nil {
			return "", "", fmt.Errorf("marshal file context: %w", err)
		}
		snap := ctxstore.Snapshot{Type: "file", Data: data}
		hash, blob, err := ctxstore.Pack(snap)
		if err != nil {
			return "", "", err
		}
		if err := c.ctx.Put(ctx, hash, "file", blob, nil); err != nil {
			return "", "", err
		}
		return hash, "pointer", nil
	}
	return "", "summary", nil
}

// sendContextFetch asks the source node (the packer, chain[0]) for the full
// snapshot. On a send failure the task is failed rather than left hanging in
// waiting_context.
func (c *Core) sendContextFetch(ctx context.Context, source, taskID, hash, ctxType string) {
	msgID, err := newUUID()
	if err != nil {
		c.failPendingContext(ctx, taskID, "mint message id: "+err.Error())
		return
	}
	env, err := bus.NewEnvelope(bus.MsgContextFetch, c.nodeID, msgID, bus.ContextFetchPayload{
		TaskID: taskID, Hash: hash, ContextType: ctxType,
	})
	if err != nil {
		c.failPendingContext(ctx, taskID, "build context_fetch: "+err.Error())
		return
	}
	env.To = source
	if err := c.sendTo(source, env); err != nil {
		c.logger.Warn("context fetch", "task", taskID, "source", source, "err", err)
		c.failPendingContext(ctx, taskID, "context source unreachable: "+err.Error())
	}
}

// failPendingContext removes a pending entry and fails the task.
func (c *Core) failPendingContext(ctx context.Context, taskID, reason string) {
	if c.dropPendingContext(taskID) {
		if err := c.store.ForceFail(ctx, taskID, reason); err != nil {
			c.logger.Warn("fail pending context task", "task", taskID, "err", err)
		}
	}
}

// dropPendingContext removes a parked entry and frees the capacity reservation
// it was holding, reporting whether an entry existed. Every site that drops an
// entry without resuming its run — a failed fetch, a cancel, an expiry sweep —
// must go through here: a bare Delete strands the reservation and slowly eats
// the node's MaxConcurrent budget (the row never reaches a countable state, so
// nothing else ever releases it). The release is sync.Once-guarded, so firing
// it here and again from a racing resume is still exactly-once.
func (c *Core) dropPendingContext(taskID string) bool {
	v, ok := c.pendingCtx.LoadAndDelete(taskID)
	if !ok {
		return false
	}
	if pc, ok := v.(*pendingContext); ok && pc.release != nil {
		pc.release()
	}
	return true
}

// handleContextFetch serves a peer's request for a full snapshot. If this node
// has it, it replies with the data; otherwise a non-OK ack so the executor can
// fail fast rather than guess.
func (c *Core) handleContextFetch(ctx context.Context, env bus.Envelope) {
	var p bus.ContextFetchPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad context_fetch", "err", err)
		return
	}
	// Authorization after authentication (review P1-3): a context snapshot is
	// source code, specs and conversation history, so it must not be enumerable
	// by any peer that learned a hash from a plaintext delegate. Only a node
	// actually part of this task's delegation — owner, chain member, or the
	// dispatch target — may pull it; the same criteria that guard artifact
	// fetches. Unknown task or unknown peer fails closed with a non-OK ack.
	if !c.artifactPeerAuthorized(ctx, p.TaskID, p.Hash, env.From) {
		c.logger.Warn("context_fetch from unauthorized peer", "task", p.TaskID,
			"hash", p.Hash, "from", env.From)
		_ = c.reply(ctx, env, bus.MsgContextAck, bus.ContextAckPayload{
			TaskID: p.TaskID, Hash: p.Hash, OK: false,
		})
		return
	}
	e, ok, err := c.ctx.Get(ctx, p.Hash)
	if err != nil || !ok {
		c.logger.Debug("context miss on fetch", "hash", p.Hash, "err", err)
		_ = c.reply(ctx, env, bus.MsgContextAck, bus.ContextAckPayload{
			TaskID: p.TaskID, Hash: p.Hash, OK: false,
		})
		return
	}
	_ = c.reply(ctx, env, bus.MsgContextAck, bus.ContextAckPayload{
		TaskID: p.TaskID, Hash: p.Hash, OK: true, Data: e.Data, Refs: e.Refs,
	})
}

// handleContextAck processes a context_fetch response. It verifies the snapshot
// hash, caches it, and resumes the paused task.
func (c *Core) handleContextAck(ctx context.Context, env bus.Envelope) {
	var p bus.ContextAckPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad context_ack", "err", err)
		return
	}
	v, ok := c.pendingCtx.Load(p.TaskID)
	if !ok {
		c.logger.Debug("context_ack for unknown task", "task", p.TaskID)
		return
	}
	pc, ok := v.(*pendingContext)
	if !ok {
		c.logger.Warn("bad pending context entry", "task", p.TaskID)
		return
	}
	if pc.source != "" && env.From != pc.source {
		// Only the node we actually asked may answer the fetch; otherwise any
		// authenticated peer could reply OK:false and force-fail an arbitrary
		// parked task, or inject attacker-controlled context data (P1-11).
		c.logger.Warn("context_ack from non-source ignored", "task", p.TaskID,
			"from", env.From, "source", pc.source)
		return
	}
	// The entry must be claimed atomically: a duplicated ack (re-sent after a
	// connection flap) racing this handler would otherwise let two goroutines
	// both pass the source check and both resume the same task (M-double-run).
	if !c.pendingCtx.CompareAndDelete(p.TaskID, v) {
		c.logger.Debug("context_ack superseded by a concurrent ack", "task", p.TaskID)
		return
	}
	release := pc.release
	if release == nil {
		release = func() {}
	}
	if !p.OK {
		c.logger.Warn("context fetch declined", "task", p.TaskID)
		release()
		_ = c.store.ForceFail(ctx, p.TaskID, "context unavailable")
		return
	}
	if ctxstore.Hash(p.Data) != p.Hash {
		c.logger.Warn("context hash mismatch", "task", p.TaskID)
		release()
		_ = c.store.ForceFail(ctx, p.TaskID, "context hash mismatch")
		return
	}
	if err := c.ctx.Put(ctx, p.Hash, pc.ctxType, p.Data, p.Refs); err != nil {
		c.logger.Warn("store fetched context", "task", p.TaskID, "err", err)
		release()
		_ = c.store.ForceFail(ctx, p.TaskID, "store context: "+err.Error())
		return
	}
	// The parked row has been occupying a CountActive slot since
	// SetWaitingContext, so the reservation is redundant now — free it before
	// the resume, not inside run(): in the gap between this ack and Resume the
	// slot would otherwise be double-counted (row + reservation) against
	// MaxConcurrent, shrinking real capacity by one for every parked fetch.
	release()
	// Resume the paused task. Its result (or decline) must reach the parent so
	// the root scheduler is unblocked, exactly as the synchronous execute path
	// reports back via reply(). The execution ctx is detached from the handler's
	// ctx: that ctx dies with the peer's websocket connection (the ack reader
	// loop), and cancelling it mid-run would kill the resumed agent subprocess
	// the moment the sender closes or drops the link — an executor-side failure
	// no caller asked for. WithoutCancel keeps the run alive; explicit cancels
	// still reach it via the running map's CancelFunc.
	runCtx := context.WithoutCancel(ctx)
	go func() {
		result, err := c.run(runCtx, p.TaskID, pc.intent, pc.required, nil)
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				c.logger.Info("task cancelled during execution", "task", p.TaskID)
				return
			}
			c.logger.Warn("run fetched-context task", "task", p.TaskID, "err", err)
			if t, gerr := c.store.Get(runCtx, p.TaskID); gerr == nil {
				c.relayToParent(runCtx, bus.MsgTaskDecline, t.Chain, bus.TaskDeclinePayload{
					TaskID: p.TaskID, Reason: err.Error(),
				})
			}
			// Same as the synchronous path: the local row must not linger in a
			// pre-terminal state for Recover to resurrect as an orphan (P1-9).
			c.terminalizeDeclined(runCtx, p.TaskID, err.Error())
			return
		}
		if t, gerr := c.store.Get(runCtx, p.TaskID); gerr == nil {
			c.relayToParent(runCtx, bus.MsgTaskResult, t.Chain, result)
		}
	}()
}
