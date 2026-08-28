package panel

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// eventsPollInterval is how often the SSE feed re-checks the task/node
// fingerprint. One second keeps the web queue feeling live without loading
// the store.
const eventsPollInterval = time.Second

// traceEventTypes is the 8-track decision-orbit visibility set (core/trace.go).
// Any event row with one of these types is pushed out as event: trace when
// the caller opts in with ?trace=1.
var traceEventTypes = map[string]bool{
	"classify_result":    true,
	"route_decision":     true,
	"delegation_hop":     true,
	"exec_agent_start":   true,
	"supervision_round":  true,
	"tier2_triggered":    true,
	"plan_stage_changed": true,
	"artifact_transfer":  true,
}

// events serves GET /api/events — a Server-Sent Events stream. Whenever the
// fingerprint of the task set (id:state:updated) or the node set changes, a
// "change" event is pushed so the client can re-fetch /api/tasks and
// /api/nodes. A comment heartbeat every ~15s keeps proxies from idling the
// connection out. This is deliberately a change signal, not a data payload:
// the client keeps using the regular JSON endpoints, so SSE adds no second
// serialization of task rows to maintain.
//
// When `?trace=1` is present the stream also emits `event: trace` events as
// new decision-orbit rows land in task_events. Each is a JSON body:
//
//	{"id": 123, "task_id": "t_42", "ts": 1787000000,
//	 "type": "route_decision", "data": {...}}
//
// The numeric `id` is monotonic per connection so clients can tolerate
// reconnects and replay any gaps by fetching the task's full event list once.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errNoFlusher)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	wantTraces := r.URL.Query().Get("trace") == "1"

	// Announce the initial state so a fresh client fetches once immediately.
	w.Write([]byte("event: change\ndata: init\n\n"))
	flusher.Flush()

	var lastTask, lastNode, lastRem string
	// trace watermark: the highest task_events.id we have already delivered.
	// 0 means "deliver everything from the start of the connection's lifetime"
	// — a newly arrived user typically sees tasks already in flight, and the
	// orbit component hydrates from GET /api/tasks/{id}.events on load; so
	// post-connect live events are the delta.
	var lastTraceID int64
	first := true
	ticker := time.NewTicker(eventsPollInterval)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			taskFP, err := h.taskFingerprint(r)
			if err != nil {
				return // store failure: drop the stream; the client reconnects
			}
			nodeFP := h.nodeFingerprint()
			remFP := h.reminderFingerprint()
			fpChanged := first || taskFP != lastTask || nodeFP != lastNode || remFP != lastRem
			if fpChanged {
				first = false
				lastTask, lastNode, lastRem = taskFP, nodeFP, remFP
				kinds := []string{"tasks"}
				data := []string{taskFP}
				if nodeFP != "" {
					kinds = append(kinds, "nodes")
					data = append(data, nodeFP)
				}
				if remFP != "" {
					kinds = append(kinds, "reminders")
					data = append(data, remFP)
				}
				if _, err := w.Write([]byte("event: change\ndata: " + strings.Join(kinds, ",") + " " + strings.Join(data, "/") + "\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}

			if wantTraces && h.db != nil {
				lastTraceID = h.pushNewTraces(r.Context(), w, flusher, lastTraceID)
			}
		}
	}
}

// pushNewTraces delivers every trace-class row from task_events with id >
// watermark as one SSE `event: trace` frame per row, and returns the new
// watermark. On any DB or write error it returns the previous watermark so
// the next tick retries — a visibility glitch never kills the stream.
func (h *handler) pushNewTraces(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, watermark int64) int64 {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, task_id, ts, type, COALESCE(data_json, '{}')
		   FROM task_events
		  WHERE id > ?
		  ORDER BY id ASC
		  LIMIT 200`, watermark)
	if err != nil {
		return watermark
	}
	defer rows.Close()
	pushed := watermark
	var wroteAny bool
	for rows.Next() {
		var (
			id       int64
			taskID   string
			ts       int64
			typ      string
			dataJSON string
		)
		if err := rows.Scan(&id, &taskID, &ts, &typ, &dataJSON); err != nil {
			break
		}
		if !traceEventTypes[typ] {
			// Advance watermark even for non-trace rows so we don't re-scan the
			// same prefix every tick.
			pushed = id
			continue
		}
		body := map[string]any{
			"id":      id,
			"task_id": taskID,
			"ts":      ts,
			"type":    typ,
		}
		// data is already JSON; embed it as a decoded object so the browser
		// reads {data:{...}}, not {data:"{...}"}.
		var raw any
		if err := json.Unmarshal([]byte(dataJSON), &raw); err == nil {
			body["data"] = raw
		} else {
			body["data"] = dataJSON
		}
		buf, err := json.Marshal(body)
		if err != nil {
			break
		}
		if _, err := w.Write([]byte("event: trace\ndata: " + string(buf) + "\n\n")); err != nil {
			break
		}
		wroteAny = true
		pushed = id
	}
	if rows.Err() != nil {
		return watermark
	}
	if wroteAny {
		flusher.Flush()
	}
	return pushed
}

var errNoFlusher = &staticError{msg: "streaming unsupported"}

type staticError struct{ msg string }

func (e *staticError) Error() string { return e.msg }

// taskFingerprint digests the visible task set (id:state:updated per task)
// into a hex string; a changed task or a new/deleted task changes it.
func (h *handler) taskFingerprint(r *http.Request) (string, error) {
	tasks, err := h.store.ListByState(r.Context(), "")
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	var buf [8]byte
	for _, t := range tasks {
		sum.Write([]byte(t.TaskID))
		sum.Write([]byte{':'})
		sum.Write([]byte(t.State))
		sum.Write([]byte{':'})
		binary.BigEndian.PutUint64(buf[:], uint64(t.UpdatedAt))
		sum.Write(buf[:])
		sum.Write([]byte{';'})
	}
	return hex.EncodeToString(sum.Sum(nil))[:16], nil
}

// nodeFingerprint digests the node directory into a hex string; a node coming
// or going (LastSeen flips its status) changes it. Empty when the panel has
// no DB handle.
func (h *handler) nodeFingerprint() string {
	if h.db == nil {
		return ""
	}
	nodes, err := ledger.Query(h.db, "", "")
	if err != nil {
		return ""
	}
	sum := sha256.New()
	var buf [8]byte
	for _, n := range nodes {
		sum.Write([]byte(n.ID))
		sum.Write([]byte{':'})
		sum.Write([]byte(n.Status))
		sum.Write([]byte{':'})
		binary.BigEndian.PutUint64(buf[:], uint64(n.LastSeen))
		sum.Write(buf[:])
		sum.Write([]byte{';'})
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}
