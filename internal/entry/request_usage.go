// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"sync"
)

type requestUsageKey struct{}
type requestUsage struct {
	mu sync.Mutex
	u  Usage
}

// TrackRequestUsage collects only calls made through this context, including
// streaming classification and fallback clients. The snapshot is concurrency safe.
func TrackRequestUsage(ctx context.Context) (context.Context, func() Usage) {
	r := &requestUsage{}
	return context.WithValue(ctx, requestUsageKey{}, r), func() Usage {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.u
	}
}

// recordUsage keeps the client-wide counters and, when requested, the usage
// of this invocation. Concurrent requests never share the request collector.
func (c *Client) recordUsage(ctx context.Context, in, out int64, cache ...int64) {
	c.addUsage(in, out, cache...)
	if r, _ := ctx.Value(requestUsageKey{}).(*requestUsage); r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.u.InputTokens += max(in, 0)
		r.u.OutputTokens += max(out, 0)
		if len(cache) > 0 {
			r.u.CachedInputTokens += max(cache[0], 0)
		}
		if len(cache) > 1 {
			r.u.CacheCreationTokens += max(cache[1], 0)
		}
	}
}

// CompleteUsage returns only provider-reported consumption of this invocation,
// including any retry that supplied usage. The cumulative Usage is unchanged.
func (c *Client) CompleteUsage(ctx context.Context, system, user string) (string, Usage, error) {
	r := &requestUsage{}
	text, err := c.Complete(context.WithValue(ctx, requestUsageKey{}, r), system, user)
	r.mu.Lock()
	defer r.mu.Unlock()
	return text, r.u, err
}
