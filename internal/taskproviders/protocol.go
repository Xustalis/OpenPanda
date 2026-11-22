// SPDX-License-Identifier: AGPL-3.0-or-later

// Package taskproviders defines business-task integrations. Model providers
// and browser execution capabilities deliberately have separate interfaces.
package taskproviders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const IntegrationVersion = 1

var (
	ErrUnsupported          = errors.New("task provider capability unavailable")
	ErrConflict             = errors.New("task integration conflict")
	ErrReconnect            = errors.New("task connection requires authorization")
	ErrPrecondition         = errors.New("task revision and idempotency key are required")
	ErrCursorExpired        = errors.New("task sync cursor expired")
	ErrAuthorizationChanged = errors.New("task authorization changed during sync")
	// ErrAutomationNotAuthorized is a local refusal, not a grant change: the
	// space automation policy never enabled this operation. It must not be
	// reported as an authorization change, which sends the reader looking for a
	// revoked grant instead of the policy switch that actually blocks the run.
	ErrAutomationNotAuthorized = errors.New("local automation policy does not authorize this operation")
)

// Key components are complete provider identifiers, never display names or
// short IDs. Connections additionally isolate cached data by account ID.
type Key struct {
	InstanceID  string `json:"instanceId"`
	WorkspaceID string `json:"workspaceId"`
	SpaceID     string `json:"spaceId"`
	TaskID      string `json:"taskId"`
}

func (k Key) String() string {
	return strings.Join([]string{k.InstanceID, k.WorkspaceID, k.SpaceID, k.TaskID}, "/")
}

func (k Key) Validate() error {
	for _, part := range []string{k.InstanceID, k.WorkspaceID, k.SpaceID, k.TaskID} {
		if part == "" || strings.TrimSpace(part) != part || strings.ContainsAny(part, "/\\?#\x00\r\n") || part == "." || part == ".." {
			return errors.New("invalid full task identifier")
		}
	}
	return nil
}

func ParseKey(raw string) (Key, error) {
	parts := strings.Split(raw, "/")
	if len(parts) != 4 {
		return Key{}, errors.New("task key requires four identifiers")
	}
	k := Key{parts[0], parts[1], parts[2], parts[3]}
	return k, k.Validate()
}

// Endpoint appends a relative path beneath the configured service root. A
// leading slash must never reset a deployment prefix such as /tsync/.
func Endpoint(root, path string) (string, error) {
	u, err := url.Parse(root)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid task service root URL")
	}
	for _, part := range strings.Split(path, "/") {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == ".." || decoded == "." || strings.ContainsAny(decoded, "\\?#\x00\r\n") {
			return "", errors.New("invalid integration endpoint")
		}
	}
	if strings.Contains(path, "://") {
		return "", errors.New("endpoint must be relative")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	return u.String(), nil
}

type Capabilities struct {
	IntegrationVersion int              `json:"integrationVersion"`
	InstanceID         string           `json:"instanceId"`
	FeedEpoch          string           `json:"feedEpoch"`
	MinClientVersion   string           `json:"minClientVersion"`
	Capabilities       []string         `json:"capabilities"`
	Limits             map[string]int64 `json:"limits"`
}

func (c Capabilities) Supports(name string) bool {
	if c.IntegrationVersion != IntegrationVersion || c.InstanceID == "" || c.FeedEpoch == "" {
		return false
	}
	for _, v := range c.Capabilities {
		if v == name {
			return true
		}
	}
	return false
}

// TypedTime preserves the source's exact representation. Floating wall time
// has no implied device timezone and cannot be automatically scheduled.
type TypedTime struct {
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	Timezone string `json:"timezone,omitempty"`
}

func (t TypedTime) Validate() error {
	var err error
	if t.Kind != "zoned_datetime" && t.Timezone != "" {
		return errors.New("task timezone requires a zoned time")
	}
	switch t.Kind {
	case "date":
		_, err = time.Parse("2006-01-02", t.Value)
	case "floating_datetime":
		_, err = parseWallTime(t.Value)
	case "zoned_datetime":
		if t.Timezone != "" {
			_, err = parseWallTime(t.Value)
			if strings.TrimSpace(t.Timezone) != t.Timezone || strings.ContainsAny(t.Timezone, "\r\n\t ") {
				return errors.New("invalid task timezone")
			}
		} else {
			_, err = parseOffsetTime(t.Value)
		}
	default:
		return errors.New("unknown task time kind")
	}
	return err
}

func (t TypedTime) ScheduleTime() (time.Time, error) {
	if t.Kind != "zoned_datetime" {
		return time.Time{}, errors.New("task time needs an explicit scheduling timezone")
	}
	if err := t.Validate(); err != nil {
		return time.Time{}, err
	}
	if t.Timezone == "" {
		return parseOffsetTime(t.Value)
	}
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return time.Time{}, errors.New("task timezone is unavailable")
	}
	wall, err := parseWallTime(t.Value)
	if err != nil {
		return time.Time{}, err
	}
	resolved := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), loc)
	if resolved.Format("2006-01-02T15:04:05.999999999") != wall.Format("2006-01-02T15:04:05.999999999") {
		return time.Time{}, errors.New("task time falls in a timezone transition gap")
	}
	// Both offsets near a transition may name the same wall clock. Require
	// an explicit offset instead of silently selecting one occurrence.
	for _, adjacent := range []time.Time{resolved.Add(-24 * time.Hour), resolved.Add(24 * time.Hour)} {
		_, offset := adjacent.Zone()
		candidate := wall.Add(-time.Duration(offset) * time.Second).In(loc)
		if !candidate.Equal(resolved) && candidate.Format("2006-01-02T15:04:05.999999999") == wall.Format("2006-01-02T15:04:05.999999999") {
			return time.Time{}, errors.New("task time is ambiguous; supply an explicit offset")
		}
	}
	return resolved, nil
}

func parseWallTime(value string) (time.Time, error) {
	for _, separator := range []string{"T", "t", " "} {
		for _, clock := range []string{"15:04:05", "15:04"} {
			if t, err := time.Parse("2006-01-02"+separator+clock, value); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, errors.New("invalid task wall time")
}

func parseOffsetTime(value string) (time.Time, error) {
	if strings.HasSuffix(value, "z") {
		value = strings.TrimSuffix(value, "z") + "Z"
	}
	for _, separator := range []string{"T", "t", " "} {
		for _, clock := range []string{"15:04:05", "15:04"} {
			for _, zone := range []string{"Z07:00", "Z0700"} {
				if t, err := time.Parse("2006-01-02"+separator+clock+zone, value); err == nil {
					return t, nil
				}
			}
		}
	}
	return time.Time{}, errors.New("invalid task offset time")
}

// QueuePriority keeps the provider's raw priority on Snapshot; only this
// explicit mapping is used when constructing an execution queue request.
func QueuePriority(raw int) (string, error) {
	switch raw {
	case 0, 2:
		return "normal", nil
	case 1:
		return "low", nil
	case 3, 4:
		return "high", nil
	default:
		return "", fmt.Errorf("unsupported task priority %d", raw)
	}
}

type Permissions struct {
	AuthEpoch string `json:"authEpoch"`
	Read      bool   `json:"read"`
	Edit      bool   `json:"edit"`
	Execute   bool   `json:"execute"`
	Deliver   bool   `json:"deliver"`
	Accept    bool   `json:"accept"`
}

type SourceRef struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	URL      string `json:"url,omitempty"`
}

type CompletionRule struct {
	RequiresReceipt    bool     `json:"requiresReceipt"`
	RequiresAcceptance bool     `json:"requiresAcceptance"`
	Scope              []string `json:"scope"`
}

type Snapshot struct {
	Key                  Key            `json:"key"`
	Title                string         `json:"title"`
	Description          string         `json:"description"`
	Status               string         `json:"status"`
	Priority             int            `json:"priority"`
	SpaceName            string         `json:"spaceName"`
	ProjectID            string         `json:"projectId"`
	ProjectName          string         `json:"projectName"`
	AssigneeAccountIDs   []string       `json:"assigneeAccountIds"`
	SourceRef            SourceRef      `json:"sourceRef"`
	Dependencies         []Key          `json:"dependencies"`
	StartAt              *TypedTime     `json:"startAt"`
	DueAt                *TypedTime     `json:"dueAt"`
	TaskRevision         string         `json:"taskRevision"`
	InputRevision        string         `json:"inputRevision"`
	AssignmentRevision   string         `json:"assignmentRevision"`
	AutomationGeneration string         `json:"automationGeneration"`
	Permissions          Permissions    `json:"permissions"`
	CompletionRule       CompletionRule `json:"completionRule"`
	// ProviderData retains protocol fields such as checklist, subtasks and
	// completion ownership without turning them into local todo data.
	ProviderData json.RawMessage `json:"providerData,omitempty"`
}

func (s Snapshot) Validate() error {
	if err := s.Key.Validate(); err != nil {
		return err
	}
	if s.Title == "" || s.TaskRevision == "" || s.InputRevision == "" || s.AssignmentRevision == "" || s.AutomationGeneration == "" || s.Permissions.AuthEpoch == "" {
		return errors.New("incomplete task snapshot")
	}
	if _, err := QueuePriority(s.Priority); err != nil {
		return err
	}
	for _, k := range s.Dependencies {
		if err := k.Validate(); err != nil {
			return err
		}
		if k.InstanceID != s.Key.InstanceID || k.WorkspaceID != s.Key.WorkspaceID || k.SpaceID != s.Key.SpaceID {
			return errors.New("cross-space task dependency")
		}
	}
	for _, t := range []*TypedTime{s.StartAt, s.DueAt} {
		if t != nil {
			if err := t.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// TriggerKey excludes display/task revision and business-status changes.
// JSON framing prevents ambiguous concatenation of opaque revision strings.
func (s Snapshot) TriggerKey(purpose string) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if purpose == "" {
		return "", errors.New("execution purpose required")
	}
	b, err := json.Marshal([]string{s.Key.String(), s.InputRevision, s.AssignmentRevision, s.AutomationGeneration, purpose})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

type ExecutionState string

const (
	Queued              ExecutionState = "queued"
	Running             ExecutionState = "running"
	WaitingContext      ExecutionState = "waiting_context"
	WaitingLogin        ExecutionState = "waiting_login"
	AwaitingSubmit      ExecutionState = "awaiting_submit"
	AwaitingAcceptance  ExecutionState = "awaiting_acceptance"
	Paused              ExecutionState = "paused"
	Succeeded           ExecutionState = "succeeded"
	Failed              ExecutionState = "failed"
	Cancelled           ExecutionState = "cancelled"
	Superseded          ExecutionState = "superseded"
	NeedsReconciliation ExecutionState = "needs_reconciliation"
)

type ApprovalMode string

const (
	BeforeSubmit   ApprovalMode = "before_submit"
	AfterExecution ApprovalMode = "after_execution"
)

type Account struct {
	AccountID string   `json:"accountId"`
	DeviceID  string   `json:"deviceId,omitempty"`
	AuthEpoch string   `json:"authEpoch"`
	Scopes    []string `json:"scopes"`
}
type Space struct {
	WorkspaceID string   `json:"workspaceId"`
	SpaceID     string   `json:"spaceId"`
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes,omitempty"`
}
type Member struct {
	AccountID string `json:"accountId"`
	Name      string `json:"name"`
	Role      string `json:"role"`
}
type SnapshotPage struct {
	SnapshotID string     `json:"snapshotId"`
	FeedEpoch  string     `json:"feedEpoch"`
	Cursor     string     `json:"cursor"`
	NextPage   string     `json:"nextPage"`
	Tasks      []Snapshot `json:"tasks"`
	AuthEpoch  string     `json:"authEpoch"`
	TotalTasks uint64     `json:"totalTasks"`
	ExpiresAt  time.Time  `json:"expiresAt"`
}
type ChangeEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}
type Changes struct {
	FeedEpoch      string        `json:"feedEpoch"`
	Cursor         string        `json:"cursor"`
	Tasks          []Snapshot    `json:"tasks"`
	Removed        []Key         `json:"removed"`
	HasMore        bool          `json:"hasMore"`
	AuthEpoch      string        `json:"authEpoch"`
	MembersChanged bool          `json:"membersChanged"`
	NeedsSnapshot  bool          `json:"needsSnapshot"`
	Events         []ChangeEvent `json:"events"`
}

// TaskProvider is the read-only minimum. Optional interfaces must be
// explicitly negotiated; an unsupported operation must return ErrUnsupported.
type TaskProvider interface {
	Capabilities(context.Context) (Capabilities, error)
	Me(context.Context) (Account, error)
	Spaces(context.Context) ([]Space, error)
	Members(context.Context, string) ([]Member, error)
	BeginSnapshot(context.Context, string) (SnapshotPage, error)
	SnapshotPage(context.Context, string, string) (SnapshotPage, error)
	Changes(context.Context, string, string, string) (Changes, error)
	Task(context.Context, Key) (Snapshot, error)
}

type TaskWriter interface {
	CreateTask(context.Context, Snapshot, string) (Snapshot, error)
	PatchTask(context.Context, Key, string, json.RawMessage, string) (Snapshot, error)
}

type Lease struct {
	ExecutionID string         `json:"executionId"`
	RequestID   string         `json:"requestId"`
	Fence       string         `json:"fence"`
	ExpiresAt   time.Time      `json:"expiresAt"`
	State       ExecutionState `json:"state"`
}
type ExecutionRequest struct {
	RequestID            string       `json:"requestId"`
	TriggerKey           string       `json:"triggerKey"`
	InputRevision        string       `json:"inputRevision"`
	AssignmentRevision   string       `json:"assignmentRevision"`
	AutomationGeneration string       `json:"automationGeneration"`
	ApprovalMode         ApprovalMode `json:"approvalMode"`
	DeviceID             string       `json:"deviceId"`
}
type ExecutionCoordinator interface {
	Claim(context.Context, Key, ExecutionRequest) (Lease, error)
	Execution(context.Context, string) (Lease, error)
	Heartbeat(context.Context, Lease) (Lease, error)
	Release(context.Context, Lease, string) error
	Event(context.Context, Lease, string, json.RawMessage) error
}
