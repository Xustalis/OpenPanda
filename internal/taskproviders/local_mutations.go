// SPDX-License-Identifier: AGPL-3.0-or-later

package taskproviders

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/todos"
)

// Mutations keep the exact response with the authoritative todo in one commit.
// A replay returns that response even if the row has since been edited.
func (p Local) mutate(ctx context.Context, key string, input any, apply func(*sql.Tx) (todos.Todo, error)) (todos.Todo, error) {
	if strings.TrimSpace(key) == "" || len(key) > 256 {
		return todos.Todo{}, ErrPrecondition
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return todos.Todo{}, err
	}
	h := sha256.Sum256(raw)
	hash := hex.EncodeToString(h[:])
	tx, err := p.Store.DB().BeginTx(ctx, nil)
	if err != nil {
		return todos.Todo{}, err
	}
	defer tx.Rollback()
	var previous, response string
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,response_json FROM task_mutations WHERE idempotency_key=?`, key).Scan(&previous, &response)
	if err == nil {
		if previous != hash {
			return todos.Todo{}, ErrConflict
		}
		var t todos.Todo
		err = json.Unmarshal([]byte(response), &t)
		return t, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return todos.Todo{}, err
	}
	t, err := apply(tx)
	if err != nil {
		return todos.Todo{}, err
	}
	encoded, err := json.Marshal(t)
	if err != nil {
		return todos.Todo{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_mutations(idempotency_key,payload_hash,response_json,created_at) VALUES(?,?,?,?)`, key, hash, string(encoded), time.Now().Unix()); err != nil {
		return todos.Todo{}, err
	}
	return t, tx.Commit()
}

func (p Local) CreateLocal(ctx context.Context, in todos.CreateInput, key string) (todos.Todo, error) {
	if err := validateLocalFields(in.Status, in.Priority); err != nil {
		return todos.Todo{}, err
	}
	return p.mutate(ctx, key, struct {
		Operation string
		Input     todos.CreateInput
	}{"create", in}, func(tx *sql.Tx) (todos.Todo, error) {
		return todos.CreateTx(ctx, tx, in)
	})
}

func (p Local) PatchLocal(ctx context.Context, id int64, revision string, patch todos.Patch, key string) (todos.Todo, error) {
	if revision == "" {
		return todos.Todo{}, ErrPrecondition
	}
	if patch.TaskID != nil || patch.Seq != nil {
		return todos.Todo{}, errors.New("execution links cannot be patched through the task provider")
	}
	status, priority := "", ""
	if patch.Status != nil {
		status = *patch.Status
	}
	if patch.Priority != nil {
		priority = *patch.Priority
	}
	if err := validateLocalFields(status, priority); err != nil {
		return todos.Todo{}, err
	}
	return p.mutate(ctx, key, struct {
		Operation string
		ID        int64
		Revision  string
		Patch     todos.Patch
	}{"patch", id, revision, patch}, func(tx *sql.Tx) (todos.Todo, error) {
		cur, err := todos.ReadTx(ctx, tx, id)
		if err != nil {
			return todos.Todo{}, err
		}
		if LocalSnapshot(cur).TaskRevision != revision {
			return todos.Todo{}, ErrConflict
		}
		return todos.PatchTx(ctx, tx, id, patch)
	})
}

func (p Local) CreateTask(ctx context.Context, s Snapshot, key string) (Snapshot, error) {
	priority, err := QueuePriority(s.Priority)
	if err != nil {
		return Snapshot{}, err
	}
	in := todos.CreateInput{Title: s.Title, Description: s.Description, Priority: priority, Project: s.ProjectID}
	if s.DueAt != nil {
		due, err := s.DueAt.ScheduleTime()
		if err != nil {
			return Snapshot{}, err
		}
		in.DueAt = due.Unix()
	}
	t, err := p.CreateLocal(ctx, in, key)
	return LocalSnapshot(t), err
}

func (p Local) PatchTask(ctx context.Context, k Key, revision string, raw json.RawMessage, key string) (Snapshot, error) {
	id, err := LocalID(k)
	if err != nil {
		return Snapshot{}, err
	}
	var in struct {
		Title       *string `json:"title"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
		Priority    *int    `json:"priority"`
		Project     *string `json:"projectId"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&in); err != nil {
		return Snapshot{}, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("expected one JSON object")
	}
	patch := todos.Patch{Title: in.Title, Description: in.Description, Status: in.Status, Project: in.Project}
	if in.Priority != nil {
		priority, err := QueuePriority(*in.Priority)
		if err != nil {
			return Snapshot{}, err
		}
		patch.Priority = &priority
	}
	t, err := p.PatchLocal(ctx, id, revision, patch, key)
	return LocalSnapshot(t), err
}

func validateLocalFields(status, priority string) error {
	if status != "" && todos.NormalizeStatus(status) != status {
		return errors.New("invalid task status")
	}
	if priority != "" && todos.NormalizePriority(priority) != priority {
		return errors.New("invalid task priority")
	}
	return nil
}
