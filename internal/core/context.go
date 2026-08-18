package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xenith/openpanda/internal/bus"
	"github.com/xenith/openpanda/internal/commander"
	"github.com/xenith/openpanda/internal/ctxstore"
)

// pendingContext is the execution context saved when a task pauses in
// waiting_context to fetch its full snapshot. handleContextAck uses it to
// resume execution. source is the node the fetch was sent to (chain[0]);
// a context_ack from any other node is rejected (P1-11).
type pendingContext struct {
	intent   string
	required []string
	ctxType  string
	source   string
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
	if _, ok := c.pendingCtx.LoadAndDelete(taskID); ok {
		if err := c.store.ForceFail(ctx, taskID, reason); err != nil {
			c.logger.Warn("fail pending context task", "task", taskID, "err", err)
		}
	}
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
	c.pendingCtx.Delete(p.TaskID)
	if !p.OK {
		c.logger.Warn("context fetch declined", "task", p.TaskID)
		_ = c.store.ForceFail(ctx, p.TaskID, "context unavailable")
		return
	}
	if ctxstore.Hash(p.Data) != p.Hash {
		c.logger.Warn("context hash mismatch", "task", p.TaskID)
		_ = c.store.ForceFail(ctx, p.TaskID, "context hash mismatch")
		return
	}
	if err := c.ctx.Put(ctx, p.Hash, pc.ctxType, p.Data, p.Refs); err != nil {
		c.logger.Warn("store fetched context", "task", p.TaskID, "err", err)
		_ = c.store.ForceFail(ctx, p.TaskID, "store context: "+err.Error())
		return
	}
	// Resume the paused task. Its result (or decline) must reach the parent so
	// the root scheduler is unblocked, exactly as the synchronous execute path
	// reports back via reply().
	go func() {
		result, err := c.run(ctx, p.TaskID, pc.intent, pc.required)
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				c.logger.Info("task cancelled during execution", "task", p.TaskID)
				return
			}
			c.logger.Warn("run fetched-context task", "task", p.TaskID, "err", err)
			if t, gerr := c.store.Get(ctx, p.TaskID); gerr == nil {
				c.relayToParent(ctx, bus.MsgTaskDecline, t.Chain, bus.TaskDeclinePayload{
					TaskID: p.TaskID, Reason: err.Error(),
				})
			}
			// Same as the synchronous path: the local row must not linger in a
			// pre-terminal state for Recover to resurrect as an orphan (P1-9).
			c.terminalizeDeclined(ctx, p.TaskID, err.Error())
			return
		}
		if t, gerr := c.store.Get(ctx, p.TaskID); gerr == nil {
			c.relayToParent(ctx, bus.MsgTaskResult, t.Chain, result)
		}
	}()
}
