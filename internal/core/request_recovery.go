// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"
)

// RecoverLocalRequests publishes locally reserved work before the scheduler
// starts. Remote reservations require live identity/lease checks in taskflow
// and are deliberately left for that service. Individual failures remain
// visible and retryable; one malformed request cannot strand all other work.
func (c *Core) RecoverLocalRequests(ctx context.Context) (int, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT request_id,connection_id,account_id,task_key,trigger_key,input_revision,purpose,local_todo_id,approval_mode,payload_kind,payload_json FROM execution_requests WHERE state='preparing' AND recovery_allowed=1 AND connection_id='' AND account_id='' ORDER BY created_at,request_id`)
	if err != nil {
		return 0, err
	}
	type pending struct {
		req           ExecutionRequest
		kind, payload string
	}
	var requests []pending
	for rows.Next() {
		var r pending
		err = rows.Scan(&r.req.RequestID, &r.req.ConnectionID, &r.req.AccountID, &r.req.TaskKey, &r.req.TriggerKey, &r.req.InputRevision, &r.req.Purpose, &r.req.LocalTodoID, &r.req.ApprovalMode, &r.kind, &r.payload)
		if err != nil {
			break
		}
		requests = append(requests, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close() // Publication needs the connection, including single-handle stores.
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, r := range requests {
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		var recoveryErr error
		switch r.kind {
		case "task":
			var p queuedRequestPayload
			if recoveryErr = json.Unmarshal([]byte(r.payload), &p); recoveryErr == nil {
				// Revalidate the stored hash through the normal request boundary.
				_, recoveryErr = c.EnqueueRequest(ctx, r.req, p.Input, p.Queue)
			}
		case "plan":
			var p planRequestPayload
			if recoveryErr = json.Unmarshal([]byte(r.payload), &p); recoveryErr == nil {
				_, recoveryErr = c.StartPlanRequest(ctx, r.req, p.Plan, p.Queue)
			}
		default:
			recoveryErr = fmt.Errorf("request has no replayable input; resubmit the original request")
		}
		message := ""
		if recoveryErr != nil {
			message = recoveryErr.Error()
			c.logger.Warn("business request recovery deferred", "request_id", r.req.RequestID, "err", recoveryErr)
		} else {
			recovered++
		}
		if _, err := c.db.ExecContext(ctx, `UPDATE execution_requests SET preparation_error=? WHERE request_id=?`, message, r.req.RequestID); err != nil {
			return recovered, err
		}
	}
	return recovered, nil
}
