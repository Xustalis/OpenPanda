// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tsync implements Integration v1. Its wire types are intentionally
// separate from Panda's presentation model. The shared contract and fixtures,
// rather than the model-provider protocol, define this boundary.
package tsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/updater"
	"github.com/Xustalis/OpenPanda/internal/version"
)

const ClientID = "openpanda"

var semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

type Capabilities struct {
	IntegrationVersion int              `json:"integrationVersion"`
	InstanceID         string           `json:"instanceId"`
	FeedEpoch          string           `json:"feedEpoch"`
	MinimumVersion     string           `json:"minIntegrationClientVersion"`
	Features           map[string]bool  `json:"integrationFeatures"`
	Limits             map[string]int64 `json:"integrationLimits"`
}

func (c Capabilities) Require(features ...string) error {
	if c.IntegrationVersion != 1 || c.InstanceID == "" || c.FeedEpoch == "" || !semanticVersion.MatchString(c.MinimumVersion) || !semanticVersion.MatchString(version.Version) || updater.CompareVersion(version.Version, c.MinimumVersion) < 0 {
		return taskproviders.ErrUnsupported
	}
	for _, feature := range features {
		if !c.Features[feature] {
			return taskproviders.ErrUnsupported
		}
	}
	return nil
}

type Space struct {
	SpaceID     string   `json:"spaceId"`
	WorkspaceID string   `json:"workspaceId"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Archived    bool     `json:"archived"`
	Scopes      []string `json:"scopes"`
}
type Me struct {
	Account struct {
		ID     string `json:"accountId"`
		Name   string `json:"displayName"`
		Active bool   `json:"active"`
	} `json:"account"`
	Device *struct {
		ID      string `json:"deviceId"`
		Revoked bool   `json:"revoked"`
	} `json:"device"`
	Scopes    []string `json:"scopes"`
	Spaces    []Space  `json:"spaces"`
	AuthEpoch uint64   `json:"authEpoch"`
}
type SpaceList struct {
	Spaces    []Space `json:"spaces"`
	AuthEpoch uint64  `json:"authEpoch"`
}
type Member struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Active      bool   `json:"active"`
}
type MemberPage struct {
	Members   []Member `json:"members"`
	NextPage  *string  `json:"nextPageToken"`
	AuthEpoch uint64   `json:"authEpoch"`
}
type Versions struct {
	Task       string `json:"taskRevision"`
	Input      string `json:"inputRevision"`
	Assignment string `json:"assignmentRevision"`
	Generation uint64 `json:"automationGeneration"`
}
type Time struct {
	Kind     string  `json:"kind"`
	Raw      string  `json:"raw"`
	Timezone *string `json:"timezone"`
}
type Task struct {
	Key           taskproviders.Key `json:"taskKey"`
	Title         string            `json:"title"`
	Body          *string           `json:"body"`
	BodyAvailable bool              `json:"bodyAvailable"`
	Status        string            `json:"status"`
	Priority      int               `json:"priority"`
	Project       *struct {
		ID          string  `json:"projectId"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
	} `json:"project"`
	Assignees    []string        `json:"assigneeAccountIds"`
	Source       json.RawMessage `json:"sourceRef"`
	Dependencies []struct {
		TaskID        string `json:"taskId"`
		Kind          string `json:"kind"`
		Direction     string `json:"direction"`
		Status        string `json:"status"`
		InputRevision string `json:"inputRevision"`
	} `json:"dependencies"`
	Checklist   json.RawMessage `json:"checklist"`
	Subtasks    json.RawMessage `json:"subtasks"`
	StartAt     *Time           `json:"startAt"`
	DueAt       *Time           `json:"dueAt"`
	Versions    Versions        `json:"versions"`
	Permissions struct {
		Read   bool `json:"read"`
		Write  bool `json:"write"`
		Claim  bool `json:"claim"`
		Decide bool `json:"decide"`
	} `json:"permissions"`
	ApprovalMode             taskproviders.ApprovalMode `json:"approvalMode"`
	CompletionOwner          string                     `json:"completionOwner"`
	ExternalCompletionPolicy string                     `json:"externalCompletionPolicy"`
	WaitingReason            *string                    `json:"waitingReason"`
}

// JSONBytes matches serde_json's compact UTF-8 array encoding, including
// non-ASCII strings and literal <>&. Signatures and trigger keys use it.
func JSONBytes(value any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
func (t Task) TriggerKey(purpose string) (string, error) {
	if purpose == "" {
		return "", errors.New("execution purpose required")
	}
	b, err := JSONBytes([]any{t.Key.String(), t.Versions.Input, t.Versions.Assignment, t.Versions.Generation, purpose})
	return string(b), err
}

func (t Task) Snapshot(epoch uint64, spaceName string) (taskproviders.Snapshot, error) {
	if err := t.Key.Validate(); err != nil {
		return taskproviders.Snapshot{}, err
	}
	if t.Title == "" || t.Versions.Task == "" || t.Versions.Input == "" || t.Versions.Assignment == "" {
		return taskproviders.Snapshot{}, errors.New("incomplete integration task")
	}
	if _, err := taskproviders.QueuePriority(t.Priority); err != nil {
		return taskproviders.Snapshot{}, err
	}
	s := taskproviders.Snapshot{Key: t.Key, Title: t.Title, Status: t.Status, Priority: t.Priority, SpaceName: spaceName, AssigneeAccountIDs: t.Assignees,
		TaskRevision: t.Versions.Task, InputRevision: t.Versions.Input, AssignmentRevision: t.Versions.Assignment, AutomationGeneration: strconv.FormatUint(t.Versions.Generation, 10),
		Permissions: taskproviders.Permissions{AuthEpoch: strconv.FormatUint(epoch, 10), Read: t.Permissions.Read, Edit: t.Permissions.Write, Execute: t.Permissions.Claim, Accept: t.Permissions.Decide},
		SourceRef:   taskproviders.SourceRef{Provider: "tsync", ID: t.Key.String()}, CompletionRule: taskproviders.CompletionRule{RequiresAcceptance: t.ApprovalMode == taskproviders.AfterExecution, RequiresReceipt: t.ApprovalMode == taskproviders.BeforeSubmit || t.ExternalCompletionPolicy == "source_confirmed"}}
	if t.Body != nil && t.BodyAvailable {
		s.Description = *t.Body
	}
	if t.Project != nil {
		s.ProjectID = t.Project.ID
		s.ProjectName = t.Project.Name
	}
	for _, d := range t.Dependencies {
		k := t.Key
		k.TaskID = d.TaskID
		if err := k.Validate(); err != nil {
			return s, err
		}
		s.Dependencies = append(s.Dependencies, k)
	}
	convert := func(v *Time) (*taskproviders.TypedTime, error) {
		if v == nil {
			return nil, nil
		}
		kind := ""
		if v.Kind == "zoned" && (v.Timezone == nil || *v.Timezone == "") || v.Kind != "zoned" && v.Timezone != nil {
			return nil, errors.New("integration time timezone mismatch")
		}
		switch v.Kind {
		case "date_only":
			kind = "date"
		case "floating":
			kind = "floating_datetime"
		case "offset", "zoned":
			kind = "zoned_datetime"
		default:
			return nil, errors.New("unknown integration time kind")
		}
		x := &taskproviders.TypedTime{Kind: kind, Value: v.Raw}
		if v.Timezone != nil {
			x.Timezone = *v.Timezone
		}
		return x, nil
	}
	var err error
	if s.StartAt, err = convert(t.StartAt); err != nil {
		return s, err
	}
	if s.DueAt, err = convert(t.DueAt); err != nil {
		return s, err
	}
	s.ProviderData, err = JSONBytes(t)
	if err == nil {
		err = s.Validate()
	}
	return s, err
}

type Manifest struct {
	SnapshotID string `json:"snapshotId"`
	SpaceID    string `json:"spaceId"`
	Watermark  string `json:"watermark"`
	ExpiresAt  string `json:"expiresAt"`
	TotalTasks uint64 `json:"totalTasks"`
	AuthEpoch  uint64 `json:"authEpoch"`
}
type Page struct {
	SnapshotID string  `json:"snapshotId"`
	Tasks      []Task  `json:"tasks"`
	NextPage   *string `json:"nextPageToken"`
	Watermark  string  `json:"watermark"`
	AuthEpoch  uint64  `json:"authEpoch"`
}
type Change struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}
type Changes struct {
	Changes   []Change `json:"changes"`
	Cursor    string   `json:"nextCursor"`
	HasMore   bool     `json:"hasMore"`
	AuthEpoch uint64   `json:"authEpoch"`
}
