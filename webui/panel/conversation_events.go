// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// A common event envelope carries mode, proposal and goal changes, including
// changes made by another local process. Polling the state endpoint reconnects.
func (h *handler) pushConversationEvents(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, after int64) int64 {
	if h.db == nil {
		return after
	}
	rows, err := h.db.QueryContext(ctx, "SELECT id,session_id,kind,payload FROM conversation_events WHERE id>? ORDER BY id LIMIT 128", after)
	if err != nil {
		return after
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var sid, kind, payload string
		if rows.Scan(&id, &sid, &kind, &payload) != nil {
			return after
		}
		data, _ := json.Marshal(map[string]any{"id": id, "session_id": sid, "kind": kind, "data": json.RawMessage(payload)})
		if _, err = fmt.Fprintf(w, "event: conversation\ndata: %s\n\n", data); err != nil {
			return after
		}
		after = id
	}
	flusher.Flush()
	return after
}
