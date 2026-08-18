package panel

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/xenith/openpanda/internal/ledger"
)

// eventsPollInterval is how often the SSE feed re-checks the task/node
// fingerprint. One second keeps the web queue feeling live without loading
// the store.
const eventsPollInterval = time.Second

// events serves GET /api/events — a Server-Sent Events stream. Whenever the
// fingerprint of the task set (id:state:updated) or the node set changes, a
// "change" event is pushed so the client can re-fetch /api/tasks and
// /api/nodes. A comment heartbeat every ~15s keeps proxies from idling the
// connection out. This is deliberately a change signal, not a data payload:
// the client keeps using the regular JSON endpoints, so SSE adds no second
// serialization of task rows to maintain.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errNoFlusher)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Announce the initial state so a fresh client fetches once immediately.
	w.Write([]byte("event: change\ndata: init\n\n"))
	flusher.Flush()

	var lastTask, lastNode, lastRem string
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
			if !first && taskFP == lastTask && nodeFP == lastNode && remFP == lastRem {
				continue
			}
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
	}
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
