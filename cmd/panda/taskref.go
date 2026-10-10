// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Display refs for task ids: the git-short-hash rule. Every listing abbreviates
// a task id, and every task verb accepts a prefix — but UUIDv7 packs a
// millisecond timestamp into the leading bytes, so the naive first-group cut
// ("01a11f36") collides for every task submitted in the same second. A board
// that shows the same "id" on five rows, none of which resolves, is a broken
// board.
//
// taskRefs computes, per listed task, the shortest prefix that is unique across
// the whole store — not just the visible page, because the point of showing it
// is that `panda task <ref>` resolves it.

import (
	"context"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// taskRefFloor is the shortest ref a listing may show: the first UUID group,
// matching what the CLI always displayed before prefixes became adaptive.
const taskRefFloor = 8

// taskRefCeil bounds how long a displayed ref may grow. Past this the column
// eats the title for an unlikely collision; resolution still works because a
// longer-but-ambiguous ref reports its candidates.
const taskRefCeil = 16

// taskRefs maps each listed id to its shortest unique prefix among allIDs (the
// store's full task-id set). Refs are capped at taskRefCeil; ids needing more
// length than that simply stay ambiguous, which ResolveTaskID already reports
// with its candidate list.
func taskRefs(listed, all []string) map[string]string {
	refs := make(map[string]string, len(listed))
	for _, id := range listed {
		refs[id] = shortestUniquePrefix(id, all)
	}
	return refs
}

// shortestUniquePrefix returns the shortest prefix of id that no other id in
// all shares, floored at taskRefFloor and capped at taskRefCeil. Dashes are
// kept in the ref: the resolution layer matches raw prefixes, so
// "01a11f36-9c" is as typeable as it is unique.
func shortestUniquePrefix(id string, all []string) string {
	n := len(id)
	limit := min(n, taskRefCeil)
	for l := taskRefFloor; l <= limit; l++ {
		if l == n {
			return id
		}
		prefix := id[:l]
		unique := true
		for _, other := range all {
			if other != id && strings.HasPrefix(other, prefix) {
				unique = false
				break
			}
		}
		if unique {
			return prefix
		}
	}
	if n <= taskRefCeil {
		return id
	}
	return id[:taskRefCeil]
}

// taskRefsFor fetches the store's id set and abbreviates the listed tasks
// against it. A stamp read failure degrades every ref to the old first-group
// cut rather than failing the listing — the board is the feature, the
// disambiguation is its polish.
func taskRefsFor(ctx context.Context, store *core.TaskStore, tasks []core.Task) map[string]string {
	listed := make([]string, 0, len(tasks))
	for _, t := range tasks {
		listed = append(listed, t.TaskID)
	}
	var all []string
	if stamps, err := store.TaskStamps(ctx); err == nil {
		all = make([]string, 0, len(stamps))
		for _, s := range stamps {
			all = append(all, s.ID)
		}
	} else {
		all = listed
	}
	return taskRefs(listed, all)
}

// refOr falls back to the classic first-group abbreviation when the task is
// absent from the refs map (older callers, races between listing and display).
func refOr(refs map[string]string, id string) string {
	if refs != nil {
		if r := refs[id]; r != "" {
			return r
		}
	}
	return shortID(id)
}
