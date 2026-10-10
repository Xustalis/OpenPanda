// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// Queue-consumer liveness probe. `panda task add`, `panda plan run`, the
// REPL's `/task add` and the classified-plan path all persist rows only;
// the queue is drained by a daemon's queue scheduler or by an embedded web
// console (QueueTasks). With neither running, an enqueued task sits forever
// — and until this probe existed the enqueue output gave no hint of that.
// The check is best-effort: a false negative costs one advisory line, never
// behavior.

import (
	"net"
	"strconv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
)

// QueueConsumerAlive reports whether some local process can drain this node's
// task queue: the daemon holds the node identity lock (the same probe
// `panda status` uses to mark the local row running), and `panda web` (or the
// standalone sidecar) accepts TCP connections on the panel address.
func QueueConsumerAlive(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if held, err := nodeidentity.Held(cfg.Node.Kind, cfg.Node.EffectiveIdentity()); err == nil && held {
		return true
	}
	return PanelReachable(cfg.Network.PanelAddr)
}

// PanelReachable dials the panel bind address. A wildcard host probes
// loopback (the listener is local by definition); the fallback range mirrors
// listenPanel, which slips forward a few ports when the configured one is
// taken — a console on 7841 is still a live consumer.
func PanelReachable(addr string) bool {
	host, base := "127.0.0.1", 7840
	if addr != "" {
		h, p, err := net.SplitHostPort(addr)
		if err != nil {
			return false
		}
		host = h
		n, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		base = n
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	const span = 6
	hits := make(chan bool, span)
	for i := 0; i < span; i++ {
		go func(port int) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 200*time.Millisecond)
			if conn != nil {
				_ = conn.Close()
			}
			hits <- err == nil
		}(base + i)
	}
	for i := 0; i < span; i++ {
		if <-hits {
			return true
		}
	}
	return false
}
