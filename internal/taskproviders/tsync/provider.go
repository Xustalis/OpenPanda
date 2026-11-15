// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

// Provider is scoped to one synchronization round. A round pins the subject,
// authorization epoch and snapshot manifests; it must never combine pages
// obtained under different grants. The deterministic runtime creates a fresh
// adapter each round, while DeviceStore alone owns rotating credentials.
type Provider struct {
	Client    *Client
	caps      Capabilities
	me        Me
	spaces    map[string]Space
	manifests map[string]Manifest
}

var _ taskproviders.TaskProvider = (*Provider)(nil)

func NewProvider(c *Client) *Provider {
	return &Provider{Client: c, spaces: map[string]Space{}, manifests: map[string]Manifest{}}
}
func (p *Provider) Capabilities(ctx context.Context) (taskproviders.Capabilities, error) {
	c, err := p.Client.Capabilities(ctx)
	if err != nil {
		return taskproviders.Capabilities{}, err
	}
	if err = c.Require(); err != nil {
		return taskproviders.Capabilities{}, err
	}
	p.caps = c
	names := []string{}
	for k, v := range c.Features {
		if v {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return taskproviders.Capabilities{IntegrationVersion: c.IntegrationVersion, InstanceID: c.InstanceID, FeedEpoch: c.FeedEpoch, MinClientVersion: c.MinimumVersion, Capabilities: names, Limits: c.Limits}, nil
}
func (p *Provider) Me(ctx context.Context) (taskproviders.Account, error) {
	me, err := p.Client.Me(ctx)
	if err != nil {
		return taskproviders.Account{}, err
	}
	if me.Account.ID == "" || !me.Account.Active || me.Device == nil || me.Device.ID == "" || me.Device.Revoked {
		return taskproviders.Account{}, taskproviders.ErrReconnect
	}
	p.me = me
	return taskproviders.Account{AccountID: me.Account.ID, DeviceID: me.Device.ID, AuthEpoch: strconv.FormatUint(me.AuthEpoch, 10), Scopes: me.Scopes}, nil
}
func (p *Provider) checkEpoch(epoch uint64) error {
	if p.me.Account.ID == "" {
		return errors.New("task identity must be loaded before data")
	}
	if epoch != p.me.AuthEpoch {
		return taskproviders.ErrAuthorizationChanged
	}
	return nil
}
func (p *Provider) Spaces(ctx context.Context) ([]taskproviders.Space, error) {
	v, err := p.Client.Spaces(ctx)
	if err != nil {
		return nil, err
	}
	if err = p.checkEpoch(v.AuthEpoch); err != nil {
		return nil, err
	}
	out := []taskproviders.Space{}
	p.spaces = map[string]Space{}
	granted := map[string]Space{}
	for _, s := range p.me.Spaces {
		granted[s.SpaceID] = s
	}
	for _, s := range v.Spaces {
		g, ok := granted[s.SpaceID]
		if !ok || g.WorkspaceID != s.WorkspaceID {
			return nil, taskproviders.ErrAuthorizationChanged
		}
		if s.Archived {
			continue
		}
		key := taskproviders.Key{InstanceID: p.caps.InstanceID, WorkspaceID: s.WorkspaceID, SpaceID: s.SpaceID, TaskID: "validation"}
		if err = key.Validate(); err != nil {
			return nil, err
		}
		if _, ok = p.spaces[s.SpaceID]; ok {
			return nil, errors.New("duplicate integration space")
		}
		p.spaces[s.SpaceID] = s
		out = append(out, taskproviders.Space{SpaceID: s.SpaceID, WorkspaceID: s.WorkspaceID, Name: s.Name, Scopes: s.Scopes})
	}
	return out, nil
}
func (p *Provider) space(id string) (Space, error) {
	s, ok := p.spaces[id]
	if !ok {
		return s, taskproviders.ErrAuthorizationChanged
	}
	return s, nil
}
func (p *Provider) Members(ctx context.Context, space string) ([]taskproviders.Member, error) {
	if err := p.caps.Require("members"); err != nil {
		return nil, err
	}
	if _, err := p.space(space); err != nil {
		return nil, err
	}
	out := []taskproviders.Member{}
	seen := map[string]bool{}
	accounts := map[string]bool{}
	token := ""
	for pages := 0; pages < 10000; pages++ {
		page, err := p.Client.Members(ctx, space, token)
		if err != nil {
			return nil, err
		}
		if err = p.checkEpoch(page.AuthEpoch); err != nil {
			return nil, err
		}
		for _, m := range page.Members {
			if m.AccountID == "" || accounts[m.AccountID] {
				return nil, errors.New("invalid member directory")
			}
			accounts[m.AccountID] = true
			if m.Active {
				out = append(out, taskproviders.Member{AccountID: m.AccountID, Name: m.DisplayName, Role: m.Role})
			}
		}
		if len(accounts) > 100000 {
			return nil, errors.New("member directory exceeds limit")
		}
		if page.NextPage == nil {
			return out, nil
		}
		token = *page.NextPage
		if token == "" || seen[token] {
			return nil, errors.New("member pagination did not advance")
		}
		seen[token] = true
	}
	return nil, errors.New("member pagination exceeds limit")
}
func (p *Provider) BeginSnapshot(ctx context.Context, space string) (taskproviders.SnapshotPage, error) {
	if err := p.caps.Require("taskRead", "snapshots"); err != nil {
		return taskproviders.SnapshotPage{}, err
	}
	if _, err := p.space(space); err != nil {
		return taskproviders.SnapshotPage{}, err
	}
	m, err := p.Client.BeginSnapshot(ctx, space)
	if err != nil {
		return taskproviders.SnapshotPage{}, err
	}
	if err = p.checkEpoch(m.AuthEpoch); err != nil {
		return taskproviders.SnapshotPage{}, err
	}
	expires, e := time.Parse(time.RFC3339, m.ExpiresAt)
	if e != nil || !time.Now().Before(expires) || m.SnapshotID == "" || m.SpaceID != space || m.Watermark == "" || m.TotalTasks > 100000 {
		return taskproviders.SnapshotPage{}, errors.New("invalid snapshot manifest")
	}
	p.manifests[m.SnapshotID] = m
	return p.SnapshotPage(ctx, m.SnapshotID, "")
}
func (p *Provider) SnapshotPage(ctx context.Context, id, token string) (taskproviders.SnapshotPage, error) {
	m, ok := p.manifests[id]
	if !ok {
		return taskproviders.SnapshotPage{}, errors.New("unknown snapshot manifest")
	}
	expires, _ := time.Parse(time.RFC3339, m.ExpiresAt)
	if !time.Now().Before(expires) {
		return taskproviders.SnapshotPage{}, taskproviders.ErrCursorExpired
	}
	raw, err := p.Client.Page(ctx, id, token)
	if err != nil {
		return taskproviders.SnapshotPage{}, err
	}
	if err = p.checkEpoch(raw.AuthEpoch); err != nil {
		return taskproviders.SnapshotPage{}, err
	}
	if raw.SnapshotID != id || raw.Watermark != m.Watermark {
		return taskproviders.SnapshotPage{}, errors.New("snapshot page identity mismatch")
	}
	out := taskproviders.SnapshotPage{SnapshotID: id, Cursor: m.Watermark, FeedEpoch: p.caps.FeedEpoch, AuthEpoch: strconv.FormatUint(m.AuthEpoch, 10), TotalTasks: m.TotalTasks, ExpiresAt: expires, Tasks: []taskproviders.Snapshot{}}
	if raw.NextPage != nil {
		if *raw.NextPage == "" {
			return out, errors.New("empty snapshot continuation")
		}
		out.NextPage = *raw.NextPage
	}
	for _, t := range raw.Tasks {
		s, err := p.snapshot(t, m.SpaceID)
		if err != nil {
			return out, err
		}
		out.Tasks = append(out.Tasks, s)
	}
	return out, nil
}
func (p *Provider) snapshot(t Task, space string) (taskproviders.Snapshot, error) {
	s, err := p.space(space)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	if t.Key.InstanceID != p.caps.InstanceID || t.Key.WorkspaceID != s.WorkspaceID || t.Key.SpaceID != space || !t.Permissions.Read {
		return taskproviders.Snapshot{}, errors.New("task outside authorized snapshot")
	}
	return t.Snapshot(p.me.AuthEpoch, s.Name)
}
func (p *Provider) Changes(ctx context.Context, space, epoch, cursor string) (taskproviders.Changes, error) {
	if err := p.caps.Require("taskRead", "changes"); err != nil {
		return taskproviders.Changes{}, err
	}
	s, err := p.space(space)
	if err != nil {
		return taskproviders.Changes{}, err
	}
	if epoch != p.caps.FeedEpoch {
		return taskproviders.Changes{}, taskproviders.ErrCursorExpired
	}
	raw, err := p.Client.Changes(ctx, space, cursor)
	if err != nil {
		return taskproviders.Changes{}, err
	}
	if err = p.checkEpoch(raw.AuthEpoch); err != nil {
		return taskproviders.Changes{}, err
	}
	out := taskproviders.Changes{FeedEpoch: epoch, Cursor: raw.Cursor, AuthEpoch: strconv.FormatUint(raw.AuthEpoch, 10), HasMore: raw.HasMore}
	// A feed page can delete and later recreate the same task. Its last
	// occurrence wins; splitting operations must preserve that ordering.
	tasks := map[string]taskproviders.Snapshot{}
	removed := map[string]taskproviders.Key{}
	if raw.Cursor == "" || raw.HasMore && raw.Cursor == cursor {
		return out, errors.New("change cursor did not advance")
	}
	for _, c := range raw.Changes {
		switch c.Type {
		case "task_upsert":
			var t Task
			if err = json.Unmarshal(c.Data, &t); err != nil {
				return out, err
			}
			snap, e := p.snapshot(t, space)
			if e != nil {
				return out, e
			}
			tasks[snap.Key.String()] = snap
			delete(removed, snap.Key.String())
		case "task_tombstone":
			var k taskproviders.Key
			if err = json.Unmarshal(c.Data, &k); err != nil {
				return out, err
			}
			if err = k.Validate(); err != nil {
				return out, err
			}
			if k.InstanceID != p.caps.InstanceID || k.WorkspaceID != s.WorkspaceID || k.SpaceID != space {
				return out, errors.New("tombstone outside authorized space")
			}
			removed[k.String()] = k
			delete(tasks, k.String())
		case "members_changed":
			out.MembersChanged = true
		case "project_changed", "project_removed":
			out.NeedsSnapshot = true
		case "authorization_changed", "space_removed":
			return out, taskproviders.ErrAuthorizationChanged
		case "execution_updated", "delivery_updated", "fact_updated":
			// Preserve authoritative facts for later business reconciliation, even
			// when task inputs themselves have not changed.
		default:
			return out, taskproviders.ErrUnsupported
		}
		out.Events = append(out.Events, taskproviders.ChangeEvent{Type: c.Type, Data: c.Data})
	}
	keys := make([]string, 0, len(tasks)+len(removed))
	for k := range tasks {
		keys = append(keys, k)
	}
	for k := range removed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if t, ok := tasks[k]; ok {
			out.Tasks = append(out.Tasks, t)
		} else {
			out.Removed = append(out.Removed, removed[k])
		}
	}
	return out, nil
}
func (p *Provider) Task(ctx context.Context, key taskproviders.Key) (taskproviders.Snapshot, error) {
	if _, err := p.space(key.SpaceID); err != nil {
		return taskproviders.Snapshot{}, err
	}
	t, err := p.Client.Task(ctx, key)
	if err != nil {
		return taskproviders.Snapshot{}, err
	}
	return p.snapshot(t, key.SpaceID)
}
