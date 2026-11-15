// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

type TaskCreate struct {
	SpaceID   string  `json:"spaceId"`
	Title     string  `json:"title"`
	Body      *string `json:"body,omitempty"`
	Priority  *int    `json:"priority,omitempty"`
	ProjectID *string `json:"projectId,omitempty"`
	StartAt   *Time   `json:"startAt,omitempty"`
	DueAt     *Time   `json:"dueAt,omitempty"`
}

func mutationHeaders(key, revision string, conditional bool) error {
	if strings.TrimSpace(key) == "" || len(key) > 256 || strings.ContainsAny(key, "\r\n") {
		return taskproviders.ErrPrecondition
	}
	if conditional && (revision == "" || strings.ContainsAny(revision, "\"\r\n,")) {
		return taskproviders.ErrPrecondition
	}
	return nil
}

func (c *Client) CreateTask(ctx context.Context, input TaskCreate, command string) (Task, error) {
	var out Task
	if err := mutationHeaders(command, "", false); err != nil {
		return out, err
	}
	err := c.request(ctx, "POST", "v1/integration/tasks", nil, input, command, "", &out)
	return out, err
}
func (c *Client) PatchTask(ctx context.Context, key taskproviders.Key, revision string, patch json.RawMessage, command string) (Task, error) {
	var out Task
	if err := key.Validate(); err != nil {
		return out, err
	}
	if err := mutationHeaders(command, revision, true); err != nil {
		return out, err
	}
	err := c.request(ctx, "PATCH", "v1/integration/tasks/"+url.PathEscape(key.TaskID), nil, patch, command, revision, &out)
	if err == nil && out.Key != key {
		err = errors.New("task mutation identity mismatch")
	}
	return out, err
}

var _ taskproviders.TaskWriter = (*Provider)(nil)

func (p *Provider) writable(key taskproviders.Key) error {
	if err := p.caps.Require("taskWrite"); err != nil {
		return err
	}
	if err := key.Validate(); err != nil {
		return err
	}
	sp, err := p.space(key.SpaceID)
	if err != nil {
		return err
	}
	if p.me.Account.ID == "" || key.InstanceID != p.caps.InstanceID || key.WorkspaceID != sp.WorkspaceID || !slices.Contains(p.me.Scopes, "tasks:write") || !slices.Contains(sp.Scopes, "tasks:write") {
		return taskproviders.ErrAuthorizationChanged
	}
	for _, granted := range p.me.Spaces {
		if granted.SpaceID == sp.SpaceID && granted.WorkspaceID == sp.WorkspaceID && !granted.Archived && slices.Contains(granted.Scopes, "tasks:write") {
			return nil
		}
	}
	return taskproviders.ErrAuthorizationChanged
}
func wireTime(v *taskproviders.TypedTime) (*Time, error) {
	if v == nil {
		return nil, nil
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	out := &Time{Raw: v.Value}
	switch v.Kind {
	case "date":
		out.Kind = "date_only"
	case "floating_datetime":
		out.Kind = "floating"
	case "zoned_datetime":
		out.Kind = "offset"
		if v.Timezone != "" {
			out.Kind = "zoned"
			zone := v.Timezone
			out.Timezone = &zone
		}
	}
	return out, nil
}
func (p *Provider) CreateTask(ctx context.Context, s taskproviders.Snapshot, command string) (taskproviders.Snapshot, error) {
	k := s.Key
	k.TaskID = "new"
	if err := p.writable(k); err != nil {
		return taskproviders.Snapshot{}, err
	}
	if strings.TrimSpace(s.Title) == "" || len(s.Title) > 4096 || len(s.Description) > 200000 {
		return taskproviders.Snapshot{}, errors.New("invalid task title or body")
	}
	if _, err := taskproviders.QueuePriority(s.Priority); err != nil {
		return taskproviders.Snapshot{}, err
	}
	if s.Status != "" && s.Status != "todo" {
		return taskproviders.Snapshot{}, taskproviders.ErrUnsupported
	}
	in := TaskCreate{SpaceID: k.SpaceID, Title: s.Title, Body: &s.Description, Priority: &s.Priority}
	if s.ProjectID != "" {
		in.ProjectID = &s.ProjectID
	}
	var err error
	if in.StartAt, err = wireTime(s.StartAt); err != nil {
		return taskproviders.Snapshot{}, err
	}
	if in.DueAt, err = wireTime(s.DueAt); err != nil {
		return taskproviders.Snapshot{}, err
	}
	wire, err := p.Client.CreateTask(ctx, in, command)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	return p.snapshot(wire, k.SpaceID)
}

// Completion and automation preferences use human or verified-delivery paths.
// Model data cannot grant local power through a business task patch.
func (p *Provider) PatchTask(ctx context.Context, key taskproviders.Key, revision string, raw json.RawMessage, command string) (taskproviders.Snapshot, error) {
	if err := p.writable(key); err != nil {
		return taskproviders.Snapshot{}, err
	}
	fields, err := taskPatch(raw)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	encoded, err := JSONBytes(fields)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	wire, err := p.Client.PatchTask(ctx, key, revision, encoded, command)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	return p.snapshot(wire, key.SpaceID)
}
func taskPatch(raw json.RawMessage) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if len(raw) > 1<<20 || json.Unmarshal(raw, &fields) != nil || len(fields) == 0 {
		return nil, errors.New("expected nonempty task patch")
	}
	out := map[string]any{}
	for name, value := range fields {
		switch name {
		case "title", "description", "projectId", "status":
			var text string
			if name == "projectId" && string(value) == "null" {
				out[name] = nil
				continue
			}
			if json.Unmarshal(value, &text) != nil || string(value) == "null" {
				return nil, errors.New("invalid task text field")
			}
			if name == "projectId" && text == "" {
				out[name] = nil
				continue
			}
			if name == "title" && (strings.TrimSpace(text) == "" || len(text) > 4096) || name == "description" && len(text) > 200000 {
				return nil, errors.New("invalid task title or body")
			}
			if name == "status" && !slices.Contains([]string{"todo", "doing", "blocked", "cancelled"}, text) {
				return nil, taskproviders.ErrUnsupported
			}
			target := name
			if name == "description" {
				target = "body"
			}
			out[target] = text
		case "priority":
			var n int
			if string(value) == "null" || json.Unmarshal(value, &n) != nil {
				return nil, errors.New("invalid task priority")
			}
			if _, err := taskproviders.QueuePriority(n); err != nil {
				return nil, err
			}
			out[name] = n
		case "startAt", "dueAt":
			if string(value) == "null" {
				out[name] = nil
				continue
			}
			var typed taskproviders.TypedTime
			decoder := json.NewDecoder(strings.NewReader(string(value)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&typed); err != nil {
				return nil, err
			}
			wire, err := wireTime(&typed)
			if err != nil {
				return nil, err
			}
			out[name] = wire
		default:
			return nil, errors.New("unknown or protected task field")
		}
	}
	return out, nil
}
