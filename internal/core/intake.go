// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/intake"
)

// intakeSeenRetention bounds the idempotence ledger. A source retries within
// minutes, so a week is far past the window in which a replay could mean
// anything; the entry exists only to make that retry cheap to answer.
const intakeSeenRetention = 7 * 24 * time.Hour

// ErrLegacyIntakeDelivery preserves the old deduplication boundary. A legacy
// claim has no execution association, so replaying it could run finished work
// twice; it cannot safely be treated as a recoverable new request.
var ErrLegacyIntakeDelivery = errors.New("delivery was recorded by legacy intake without a recoverable execution")

// ClaimIntake records one inbound delivery and reports whether this call
// recorded it first.
//
// The INSERT OR IGNORE plus RowsAffected is the whole concurrency story: two
// webhook requests carrying the same delivery race on the primary key, and
// exactly one of them sees a row inserted. A separate read-then-write would
// leave a window where both passed the check and both queued a task.
func (c *Core) ClaimIntake(ctx context.Context, source, key string) (bool, error) {
	if key == "" {
		// No delivery id means the source does not retry; nothing to dedupe.
		return true, nil
	}
	now := time.Now()
	res, err := c.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO intake_seen (source, key, seen_at) VALUES (?, ?, ?)`,
		source, key, now.Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	first := n == 1
	if first {
		// Best-effort retention on the write path only: idx_intake_seen_at
		// turns this into a range delete over a small table, and a failure
		// costs disk rather than correctness.
		_, _ = c.db.ExecContext(ctx,
			`DELETE FROM intake_seen WHERE seen_at < ?`,
			now.Add(-intakeSeenRetention).Unix())
	}
	return first, nil
}

// ReleaseIntake forgets a claim whose work never materialized — the webhook
// handler claims a delivery before enqueueing, so a transient enqueue failure
// would otherwise leave the id in the ledger and answer the source's retry
// "duplicate" for a task that was never created.
//
// The delete is unconditional: the key is the source's own delivery id, and
// only the handler holding the claim releases it. A retry that raced the
// release still loses that race, which is the same outcome as any other
// duplicate — but the next retry, and the one after it, get through.
//
// The trade is deliberate and lands on at-least-once: Enqueue commits its task
// row in one transaction, so an error normally means nothing was created, but
// a commit whose outcome is unknown could queue a second task on the retry.
// Losing the request outright is the worse failure for an inbound event.
func (c *Core) ReleaseIntake(ctx context.Context, source, key string) error {
	if key == "" {
		// No delivery id means nothing was recorded; see ClaimIntake.
		return nil
	}
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM intake_seen WHERE source = ? AND key = ?`,
		source, key); err != nil {
		return fmt.Errorf("release intake: %w", err)
	}
	return nil
}

// IntakeTask queues an admitted external event as work on this node.
//
// It never blocks on execution: Enqueue hands the task to the queue scheduler
// and returns, so the webhook handler answers in milliseconds while a long job
// starts behind it. A caller that needs the outcome watches the task like any
// other queued work — there is no separate result channel, and creating one
// here would be a second lifecycle to keep in step with the task row.
func (c *Core) IntakeTask(ctx context.Context, ev intake.Event, project string, priority int) (string, error) {
	if priority < PriorityHigh || priority > PriorityLow {
		priority = PriorityNormal
	}
	q := DefaultQueueSpec()
	q.Priority = priority
	in := TaskInput{
		Title:   ev.TaskTitle(),
		Project: project,
		Intent:  ev.Intent(),
		// The source facts ride the spec so `panda task show`, the panel and a
		// hook script can answer "where did this come from" without parsing the
		// intent's prose. Unknown keys are ignored by every spec reader.
		SpecJSON: intakeSpec(ev),
		// SessionID stays empty on purpose: inbound work has no conversation,
		// so it claims the project root lane and serializes with other
		// unpinned writers instead of running beside them in a lane nobody
		// owns.
	}
	if ev.Delivery != "" {
		// Hash the tuple rather than concatenating untrusted identifiers. This
		// keeps identities bounded and distinct even when either contains a
		// separator. Raw-body identity is carried by the fixed input spec.
		identity, _ := json.Marshal([]string{ev.Source, ev.Delivery})
		digest := sha256.Sum256(identity)
		key := "intake:" + hex.EncodeToString(digest[:])
		var legacy int
		if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM intake_seen
 WHERE source=? AND key=? AND NOT EXISTS (SELECT 1 FROM execution_requests WHERE request_id=?)`, ev.Source, ev.Delivery, key).Scan(&legacy); err != nil {
			return "", err
		}
		if legacy != 0 {
			return "", ErrLegacyIntakeDelivery
		}
		result, err := c.EnqueueRequest(ctx, ExecutionRequest{RequestID: key, TaskKey: key, TriggerKey: key, Purpose: "intake"}, in, q)
		return result.Task.TaskID, err
	}
	t, err := c.Enqueue(ctx, in, q)
	if err != nil {
		return "", err
	}
	return t.TaskID, nil
}

// intakeSpec records the provenance of an inbound task. The hook environment
// reads the same keys, so an operator can answer back to the thread a request
// came from without parsing prose.
func intakeSpec(ev intake.Event) string {
	facts := map[string]any{
		"source":        ev.Source,
		"source_repo":   ev.Repo,
		"source_url":    ev.URL,
		"source_kind":   ev.Kind,
		"source_thread": ev.Thread,
		"source_actor":  ev.Actor,
	}
	if ev.Number > 0 {
		facts["source_number"] = strconv.Itoa(ev.Number)
	}
	if ev.BodyHash != "" {
		facts["source_body_hash"] = ev.BodyHash
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return ""
	}
	return string(raw)
}
