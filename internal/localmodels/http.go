// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// The HTTP half of discovery. Every probe here is one GET with a hard timeout,
// because a discovery run is a UI action: a port that accepts the connection
// and then says nothing must cost the caller the timeout and nothing more.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient is shared so a run reuses one connection pool across the seven
// loopback ports and the provider probes.
var httpClient = &http.Client{
	// A redirect on a model-listing endpoint is not a thing; following one
	// would let a loopback port bounce the probe somewhere else.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// getJSON fetches one URL with a bearer token and returns the body.
func getJSON(ctx context.Context, url, bearer string, timeout time.Duration) ([]byte, bool) {
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	// A misconfigured port can answer with an endless stream; 4 MiB is far more
	// than any model list and keeps one bad endpoint from eating memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false
	}
	return body, true
}

// probeModelIDs asks an OpenAI-compatible endpoint for its roster. The path is
// `<base>/models` — the same rule the Codex collector uses, and the reason a
// base that already ends in /v1 works unchanged.
func probeModelIDs(ctx context.Context, base, key string, timeout time.Duration) []string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil
	}
	body, ok := getJSON(ctx, base+"/models", key, timeout)
	if !ok {
		return nil
	}
	var out []string
	for _, rec := range decodeModelRecords(body) {
		if id, _ := cleanModelID(rec.id); id != "" && !isEmbeddingModel(id) {
			out = append(out, id)
		}
	}
	return out
}

type modelRecord struct{ id, label string }

// decodeModelRecords reads the shapes vendors actually return: a bare array of
// ids, a bare array of objects, or one of those wrapped in {data: …} /
// {models: …} / {results: …}. Ollama's /api/ps names the field "model", which
// is why that key is in the list.
func decodeModelRecords(raw []byte) []modelRecord {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	rows := modelRows(v)
	out := make([]modelRecord, 0, len(rows))
	for _, row := range rows {
		switch t := row.(type) {
		case string:
			out = append(out, modelRecord{id: t})
		case map[string]any:
			id := firstString(t, "id", "slug", "model", "name")
			label := firstString(t, "display_name", "displayName", "label")
			if label == "" {
				label = firstString(t, "name")
			}
			if id != "" {
				out = append(out, modelRecord{id: id, label: label})
			}
		}
	}
	return out
}

func modelRows(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case map[string]any:
		for _, key := range []string{"data", "models", "results"} {
			if rows, ok := t[key].([]any); ok {
				return rows
			}
		}
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// isEmbeddingModel drops the rows a chat registry cannot use. Every local
// server lists its embedding models in the same roster as its chat models, and
// picking one produces a row that fails on the first turn with a shape error
// rather than a useful message.
func isEmbeddingModel(id string) bool {
	low := strings.ToLower(id)
	return strings.Contains(low, "embed") ||
		strings.Contains(low, "bge-") ||
		strings.Contains(low, "nomic") ||
		strings.Contains(low, "rerank")
}
