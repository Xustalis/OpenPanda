// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// taskArtifact resolves the task's output artifact to (hash, archive) or
// writes the honest error: 404 no artifact recorded / the pool does not hold
// it (it may still live on the executor node that produced it — a task
// report only travels back as the hash; the bytes stay where they were made
// until a peer pulls them).
func (h *handler) taskArtifact(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.artifacts == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("artifact pool not configured"))
		return "", false
	}
	t, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such task"))
		return "", false
	}
	hash := t.OutputArtifact
	if hash == "" {
		writeErr(w, http.StatusNotFound, errors.New("task recorded no output artifact"))
		return "", false
	}
	if _, ok := h.artifacts.Has(hash); !ok {
		writeErr(w, http.StatusNotFound,
			fmt.Errorf("artifact %s not in this node's pool — it lives on the node that ran the task", shortHash(hash)))
		return "", false
	}
	return hash, true
}

// getTaskArtifact serves the whole packed tree as a .tar.gz download —
// `panda task artifact` is the CLI twin. ServeContent gives range/HEAD for
// free; the name carries the short hash so two downloads never collide.
func (h *handler) getTaskArtifact(w http.ResponseWriter, r *http.Request) {
	hash, ok := h.taskArtifact(w, r)
	if !ok {
		return
	}
	f, err := h.artifacts.Open(hash)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=\"task-%s-%s.tar.gz\"", r.PathValue("id"), shortHash(hash)))
	http.ServeContent(w, r, "x.tar.gz", time.Time{}, f)
}

// listTaskArtifact returns the file manifest — what the download contains —
// without unpacking: the console shows entries and sizes, and the file
// preview endpoint fetches one on demand.
func (h *handler) listTaskArtifact(w http.ResponseWriter, r *http.Request) {
	hash, ok := h.taskArtifact(w, r)
	if !ok {
		return
	}
	entries, err := h.artifacts.Entries(hash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{
		"id":      r.PathValue("id"),
		"hash":    hash,
		"entries": entries,
	})
}

// getTaskArtifactFile serves one file out of the artifact — the preview a
// user opens for a screenshot or a log. Content is untrusted output from
// whichever node ran the task, so the type whitelist is deliberate:
// images/audio/video/PDF get their detected type and render inline; text is
// served AS text/plain (a crafted task result cannot run script on the
// panel origin — nosniff holds it to the declared type); everything else
// goes as an attachment. ?download=1 always forces the attachment path.
func (h *handler) getTaskArtifactFile(w http.ResponseWriter, r *http.Request) {
	hash, ok := h.taskArtifact(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, errors.New("missing ?path="))
		return
	}
	rc, size, err := h.artifacts.OpenEntry(hash, rel)
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	defer rc.Close()

	base := path.Base(rel)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=\"%s\"", safeFilename(base)))
		w.Header().Set("Content-Length", fmt.Sprint(size))
		_, _ = io.Copy(w, rc)
		return
	}

	// Sniff the first block once; the whitelist decides the served type —
	// DetectContentType alone would let a peer's task output smuggle
	// text/html onto this origin.
	buf := make([]byte, 512)
	n, _ := io.ReadFull(rc, buf)
	sniffed := http.DetectContentType(buf[:n])
	served := sniffed
	switch {
	case strings.HasPrefix(sniffed, "image/"),
		strings.HasPrefix(sniffed, "audio/"),
		strings.HasPrefix(sniffed, "video/"),
		sniffed == "application/pdf":
		// renderable media: safe inline, this is the screenshot preview
	case strings.HasPrefix(sniffed, "text/") || sniffed == "application/json":
		served = "text/plain; charset=utf-8"
	default:
		served = "application/octet-stream"
	}
	w.Header().Set("Content-Type", served)
	w.Header().Set("Content-Length", fmt.Sprint(size))
	if _, err := w.Write(buf[:n]); err != nil {
		return
	}
	_, _ = io.Copy(w, rc)
}

// artifactSource names the node a local pool should ask for a task artifact.
// The origin's own row keeps owner_node pointing at itself after a delegation;
// the latest delegate target is the executor (or the first hop toward it), and
// a delegated result carries the executor explicitly. Asking the origin itself
// was the old dead end: the bytes were never there.
func artifactSource(ctx context.Context, store *core.TaskStore, t core.Task) string {
	if target, err := store.DispatchTarget(ctx, t.TaskID); err == nil &&
		target != "" && target != t.OwnerNode {
		return target
	}
	if exec := resultExecutor(t.ResultJSON); exec != "" && exec != t.OwnerNode {
		return exec
	}
	return t.OwnerNode
}

// resultExecutor reads the executor a delegated result payload recorded.
func resultExecutor(raw string) string {
	if raw == "" {
		return ""
	}
	var payload struct {
		Executor string `json:"executor"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Executor)
}

// shortHash renders a content hash the way the UI does — first 12 hex chars
// is far past collision distance for naming one download.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// safeFilename strips a Content-Disposition filename to characters that can
// never break the header (quotes, controls, separators already removed by
// path.Base — this is defense for %-style browser quirks too).
func safeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_', r == ' ':
			return r
		}
		return '_'
	}, name)
}
