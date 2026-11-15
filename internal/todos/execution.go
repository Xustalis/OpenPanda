// SPDX-License-Identifier: AGPL-3.0-or-later

package todos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
)

// ExecutionRevision ignores display/status changes and the most-recent-task
// compatibility field, while retaining every input that can change the work.
func ExecutionRevision(t Todo) string {
	// Create returns nil tags while a persisted empty JSON array scans as an
	// empty slice. Order and duplicate display tags do not change the work.
	tags := append([]string{}, t.Tags...)
	slices.Sort(tags)
	tags = slices.Compact(tags)
	b, _ := json.Marshal(struct {
		Title, Description, Project, Priority string
		DueAt                                 int64
		Tags                                  []string
	}{t.Title, t.Description, t.Project, t.Priority, t.DueAt, tags})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ReadTx lets the execution publisher recheck the authoritative intention in
// the same transaction that binds its execution, including concurrent edits.
func ReadTx(ctx context.Context, tx *sql.Tx, id int64) (Todo, error) {
	t, err := scanTodo(tx.QueryRowContext(ctx, `SELECT `+todoCols+` FROM todos WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound
	}
	return t, err
}
