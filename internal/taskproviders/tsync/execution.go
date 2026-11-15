// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

type Fence struct {
	Epoch   string `json:"leaseEpoch"`
	Counter uint64 `json:"counter"`
}
type ExecutionScope struct {
	FullGoal   bool     `json:"fullGoal"`
	Objectives []string `json:"objectives"`
}
type PandaRefs struct {
	TaskIDs    []string `json:"taskIds"`
	PlanIDs    []string `json:"planIds"`
	AttemptIDs []string `json:"attemptIds"`
}
type ExecutionRequest struct {
	RequestID          string                     `json:"requestId"`
	TriggerKey         string                     `json:"triggerKey"`
	InputRevision      string                     `json:"inputRevision"`
	AssignmentRevision string                     `json:"assignmentRevision"`
	Generation         uint64                     `json:"automationGeneration"`
	Purpose            string                     `json:"purpose"`
	Scope              ExecutionScope             `json:"scope"`
	Mode               taskproviders.ApprovalMode `json:"mode"`
	DeviceID           string                     `json:"deviceId"`
}
type ExecutionRecord struct {
	ExecutionID string                       `json:"executionId"`
	RequestID   string                       `json:"requestId"`
	TaskKey     taskproviders.Key            `json:"taskKey"`
	AccountID   string                       `json:"accountId"`
	DeviceID    string                       `json:"deviceId"`
	TriggerKey  string                       `json:"triggerKey"`
	Versions    Versions                     `json:"versions"`
	Purpose     string                       `json:"purpose"`
	Scope       ExecutionScope               `json:"scope"`
	Mode        taskproviders.ApprovalMode   `json:"mode"`
	Status      taskproviders.ExecutionState `json:"status"`
	Fence       Fence                        `json:"fence"`
	LeaseUntil  *string                      `json:"leaseUntil"`
	ServerTime  string                       `json:"serverTime"`
	PandaRefs   PandaRefs                    `json:"pandaRefs"`
}

// RemainingLease uses the server's clock. The caller subtracts the full
// round-trip time before using this duration as a local monotonic deadline.
func (r ExecutionRecord) RemainingLease() (time.Duration, error) {
	if r.ExecutionID == "" || r.RequestID == "" || r.AccountID == "" || r.DeviceID == "" || r.Fence.Epoch == "" || r.Fence.Counter == 0 || r.LeaseUntil == nil {
		return 0, errors.New("incomplete execution lease")
	}
	server, err := time.Parse(time.RFC3339Nano, r.ServerTime)
	if err != nil {
		return 0, err
	}
	until, err := time.Parse(time.RFC3339Nano, *r.LeaseUntil)
	if err != nil {
		return 0, err
	}
	d := until.Sub(server)
	if d <= 0 || d > time.Hour {
		return 0, errors.New("expired or invalid execution lease")
	}
	return d, r.TaskKey.Validate()
}
func executionPath(id, suffix string) string {
	return "v1/integration/executions/" + url.PathEscape(id) + suffix
}
func (c *Client) ClaimExecution(ctx context.Context, task taskproviders.Key, req ExecutionRequest) (ExecutionRecord, error) {
	return c.ClaimExecutionWithKey(ctx, task, req, req.RequestID)
}

// ClaimExecutionWithKey separates the logical request from a lease acquisition.
// Recovering an expired lease retains requestId but persists a new command key;
// retrying a lost response reuses that command key and the exact same body.
func (c *Client) ClaimExecutionWithKey(ctx context.Context, task taskproviders.Key, req ExecutionRequest, commandID string) (ExecutionRecord, error) {
	var out ExecutionRecord
	if err := task.Validate(); err != nil {
		return out, err
	}
	if req.RequestID == "" || commandID == "" || req.DeviceID == "" || req.InputRevision == "" || req.AssignmentRevision == "" || req.Purpose == "" || req.TriggerKey == "" || (!req.Scope.FullGoal && len(req.Scope.Objectives) == 0) || req.Mode != taskproviders.BeforeSubmit && req.Mode != taskproviders.AfterExecution {
		return out, errors.New("incomplete execution claim")
	}
	if req.Scope.Objectives == nil {
		req.Scope.Objectives = []string{}
	}
	err := c.request(ctx, "POST", "v1/integration/tasks/"+url.PathEscape(task.TaskID)+"/executions", nil, req, commandID, "", &out)
	return out, err
}
func (c *Client) Execution(ctx context.Context, id string) (ExecutionRecord, error) {
	var out ExecutionRecord
	if id == "" {
		return out, errors.New("execution ID required")
	}
	err := c.request(ctx, "GET", executionPath(id, ""), nil, nil, "", "", &out)
	return out, err
}
func (c *Client) HeartbeatExecution(ctx context.Context, id string, fence Fence, eventID string) (ExecutionRecord, error) {
	var out ExecutionRecord
	if id == "" || fence.Epoch == "" || fence.Counter == 0 || eventID == "" {
		return out, errors.New("incomplete execution heartbeat")
	}
	err := c.request(ctx, "POST", executionPath(id, "/heartbeat"), nil, struct {
		Fence Fence `json:"fence"`
	}{fence}, eventID, "", &out)
	return out, err
}
func (c *Client) ReleaseExecution(ctx context.Context, id string, fence Fence, reason, eventID string) (ExecutionRecord, error) {
	var out ExecutionRecord
	if id == "" || fence.Epoch == "" || fence.Counter == 0 || eventID == "" {
		return out, errors.New("incomplete execution release")
	}
	switch reason {
	case "waiting_context", "waiting_login", "awaiting_submit", "awaiting_acceptance", "paused", "cancelled":
	default:
		return out, errors.New("invalid execution release reason")
	}
	err := c.request(ctx, "POST", executionPath(id, "/release"), nil, struct {
		Fence  Fence  `json:"fence"`
		Reason string `json:"reason"`
	}{fence, reason}, eventID, "", &out)
	return out, err
}

type ExecutionEvent struct {
	EventID     string                       `json:"eventId"`
	RequestID   string                       `json:"requestId"`
	ExecutionID string                       `json:"executionId"`
	Fence       Fence                        `json:"fence"`
	CausedBy    *string                      `json:"causedBy"`
	Status      taskproviders.ExecutionState `json:"status"`
	Summary     string                       `json:"summary"`
	PandaRefs   PandaRefs                    `json:"pandaRefs"`
}

func (c *Client) ReportExecution(ctx context.Context, event ExecutionEvent) (ExecutionRecord, error) {
	var out ExecutionRecord
	err := c.request(ctx, "POST", executionPath(event.ExecutionID, "/events"), nil, event, event.EventID, "", &out)
	return out, err
}
