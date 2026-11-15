// SPDX-License-Identifier: AGPL-3.0-or-later

package taskproviders

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/todos"
)

// Local projects the authoritative todo; it never makes another editable copy.
type Local struct{ Store *todos.Store }

func LocalSnapshot(t todos.Todo) Snapshot {
	priority := 2
	if t.Priority == todos.PriorityHigh {
		priority = 3
	}
	if t.Priority == todos.PriorityLow {
		priority = 1
	}
	s := Snapshot{Key: Key{"local", "local", "local", strconv.FormatInt(t.ID, 10)}, Title: t.Title, Description: t.Description, Status: t.Status, Priority: priority,
		SpaceName: "local", ProjectID: t.Project, ProjectName: t.Project, AssigneeAccountIDs: []string{"local"}, SourceRef: SourceRef{Provider: "local", ID: strconv.FormatInt(t.ID, 10)},
		TaskRevision: fmt.Sprintf("local:%d", t.Revision), InputRevision: todos.ExecutionRevision(t), AssignmentRevision: "local", AutomationGeneration: "0",
		Permissions: Permissions{AuthEpoch: "local", Read: true, Edit: true, Execute: true, Deliver: true, Accept: true}, CompletionRule: CompletionRule{RequiresAcceptance: true}}
	if t.DueAt != 0 {
		s.DueAt = &TypedTime{Kind: "zoned_datetime", Value: time.Unix(t.DueAt, 0).UTC().Format(time.RFC3339)}
	}
	return s
}

func LocalID(k Key) (int64, error) {
	if k.InstanceID != "local" || k.WorkspaceID != "local" || k.SpaceID != "local" {
		return 0, errors.New("not a local task")
	}
	id, err := strconv.ParseInt(k.TaskID, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid local task ID")
	}
	return id, nil
}
func (p Local) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{IntegrationVersion: 1, InstanceID: "local", FeedEpoch: "local", Capabilities: []string{"tasks:read", "tasks:write"}}, nil
}
func (p Local) Me(context.Context) (Account, error) {
	return Account{AccountID: "local", AuthEpoch: "local", Scopes: []string{"tasks:read", "tasks:write", "tasks:execute"}}, nil
}
func (p Local) Spaces(context.Context) ([]Space, error) {
	return []Space{{WorkspaceID: "local", SpaceID: "local", Name: "local"}}, nil
}
func (p Local) Members(context.Context, string) ([]Member, error) {
	return []Member{{AccountID: "local", Name: "local", Role: "owner"}}, nil
}
func (p Local) BeginSnapshot(ctx context.Context, _ string) (SnapshotPage, error) {
	ts, err := p.Store.List(ctx, todos.Filter{})
	page := SnapshotPage{SnapshotID: "local", FeedEpoch: "local", Tasks: []Snapshot{}}
	for _, t := range ts {
		page.Tasks = append(page.Tasks, LocalSnapshot(t))
	}
	return page, err
}
func (p Local) SnapshotPage(context.Context, string, string) (SnapshotPage, error) {
	return SnapshotPage{}, ErrUnsupported
}
func (p Local) Changes(context.Context, string, string, string) (Changes, error) {
	return Changes{}, ErrUnsupported
}
func (p Local) Task(ctx context.Context, k Key) (Snapshot, error) {
	id, err := LocalID(k)
	if err != nil {
		return Snapshot{}, err
	}
	t, err := p.Store.Get(ctx, id)
	return LocalSnapshot(t), err
}

var _ TaskProvider = Local{}
var _ TaskWriter = Local{}
