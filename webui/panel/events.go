package panel

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
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
			taskFP, err := h.cachedTaskFingerprint(r)
			if err != nil {
				return // store failure: drop the stream; the client reconnects
			}
			nodeFP := h.cachedNodeFingerprint()
			remFP := h.cachedReminderFingerprint()
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
	return h.cachedTaskFingerprint(r)
}

// cachedTaskFingerprint runs the task-set scan behind the shared cache so the
// full-table read happens at most once per poll interval across every
// connected client (see sharedFingerprints).
func (h *handler) cachedTaskFingerprint(r *http.Request) (string, error) {
	return sharedFingerprints.get("tasks", func() (string, error) {
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
	})
}

// nodeFingerprint digests the node directory into a hex string; a node coming
// or going (LastSeen flips its status) changes it. Empty when the panel has
// no DB handle.
func (h *handler) nodeFingerprint() string {
	return h.cachedNodeFingerprint()
}

// cachedNodeFingerprint runs the node-directory scan behind the shared cache.
func (h *handler) cachedNodeFingerprint() string {
	if h.db == nil {
		return ""
	}
	fp, _ := sharedFingerprints.get("nodes", func() (string, error) {
		nodes, err := ledger.Query(h.db, "", "")
		if err != nil {
			return "", err
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
		return hex.EncodeToString(sum.Sum(nil))[:16], nil
	})
	return fp
}

// cachedReminderFingerprint routes the reminder digest (defined in
// reminders.go) through the same shared cache as the task and node scans.
func (h *handler) cachedReminderFingerprint() string {
	if h.reminders == nil {
		return ""
	}
	fp, _ := sharedFingerprints.get("reminders", func() (string, error) {
		return h.reminderFingerprint(), nil
	})
	return fp
}

// sharedFingerprints memoizes the SSE change-detection digests for every
// connection. Each SSE stream polls once per eventsPollInterval; without this
// cache, N connections meant N full scans per second, so scan load grew with
// the audience. With a TTL equal to the poll interval, the store is scanned
// once per window no matter how many clients watch — and because the cache
// lifetime equals the poll cadence, change detection latency is unchanged.
var sharedFingerprints = newFingerprintCache(eventsPollInterval)

// fingerprintCache is a TTL cache with single-flight dedup: concurrent callers
// asking for the same expired key coalesce into one load instead of stampeding
// the store. Failed loads are not cached, so a transient error does not pin a
// stale value for the whole TTL window.
type fingerprintCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time // injectable for tests
	ents map[string]*fpEntry
}

type fpEntry struct {
	val   string
	err   error
	stamp time.Time
	// done closes once val and err are populated; waiters blocked on a load in
	// flight read them only after it closes.
	done chan struct{}
}

func newFingerprintCache(ttl time.Duration) *fingerprintCache {
	return &fingerprintCache{ttl: ttl, now: time.Now, ents: map[string]*fpEntry{}}
}

// get returns the cached value for key, or runs load exactly once for the
// current window — concurrent callers block on the same load.
func (c *fingerprintCache) get(key string, load func() (string, error)) (string, error) {
	c.mu.Lock()
	now := c.now()
	if e := c.ents[key]; e != nil && now.Sub(e.stamp) < c.ttl {
		c.mu.Unlock()
		<-e.done
		// The same outcome the loader got, error included: a waiter that
		// returned a bare empty value made the events loop treat a failed scan
		// as a change signal, and let every stream but the loader's survive a
		// persistent store failure on a false fingerprint.
		return e.val, e.err
	}
	e := &fpEntry{stamp: now, done: make(chan struct{})}
	c.ents[key] = e
	c.mu.Unlock()

	val, err := load()
	if err != nil {
		// Drop the in-flight entry so the next caller retries immediately;
		// waiters observing this entry see the same error, so their streams
		// drop and the clients reconnect instead of carrying a fake signal.
		c.mu.Lock()
		if c.ents[key] == e {
			delete(c.ents, key)
		}
		c.mu.Unlock()
		e.err = err
		close(e.done)
		return "", err
	}
	e.val = val
	close(e.done)
	return val, nil
}
