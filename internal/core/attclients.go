// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// Attached clients — the return-route half of pair-over-transit (docs/protocol.md
// P4b). A joiner that cannot reach the introducer directly dials a contact
// point and asks it to forward its pair_hello; the introducer then has to
// answer to a destination that has no meshed identity. The contact point
// remembers "this unauthenticated conn claims to be k:<pub>" for the life of
// the pairing window, and transit frames addressed to that identity are
// written to that conn.
//
// The binding is deliberately weak, local and short-lived: it is NOT
// gossip, grants NO trust tier, and redirects nothing but transit frames
// whose payload is a pair handshake. An impostor that grabs another
// joiner's binding can only make the two ends' SAS codes disagree — the
// same outcome as any MITM on the pairing channel, which is exactly what
// the code comparison exists to catch.
type attClient struct {
	conn    *bus.Conn
	expires time.Time
}

// attState is the contact point's binding table.
type attState struct {
	mu sync.Mutex
	// byStable maps "k:<hex>" → conn. A joiner that stops writing (abandoned
	// session, crashed CLI) ages out after attClientTTL.
	byStable map[string]attClient
}

const (
	// attClientTTL bounds the binding: a pairing session's own window is
	// four minutes, so a route that outlives it by much is dead weight.
	attClientTTL = 4 * time.Minute
	// attMaxClients caps the table so a hostile LAN cannot pin unlimited
	// inbound conns in a contact point's memory.
	attMaxClients = 16
)

// registerAttClient binds a stable identity to the unauthenticated conn it
// arrived on, refreshing any previous binding.
func (c *Core) registerAttClient(stable string, conn *bus.Conn) {
	if stable == "" || conn == nil {
		return
	}
	c.att.mu.Lock()
	defer c.att.mu.Unlock()
	if c.att.byStable == nil {
		c.att.byStable = make(map[string]attClient)
	}
	now := time.Now()
	for id, ac := range c.att.byStable {
		if now.After(ac.expires) {
			delete(c.att.byStable, id)
		}
	}
	if _, exists := c.att.byStable[stable]; !exists && len(c.att.byStable) >= attMaxClients {
		return
	}
	c.att.byStable[stable] = attClient{conn: conn, expires: now.Add(attClientTTL)}
}

// attClientFor resolves a stable identity to the conn its joiner arrived on,
// or nil when the binding is absent or expired.
func (c *Core) attClientFor(stable string) *bus.Conn {
	if stable == "" {
		return nil
	}
	c.att.mu.Lock()
	defer c.att.mu.Unlock()
	ac, ok := c.att.byStable[stable]
	if !ok {
		return nil
	}
	if time.Now().After(ac.expires) {
		delete(c.att.byStable, stable)
		return nil
	}
	return ac.conn
}

// dropAttClient removes a binding (conn closed, or the pairing finished).
func (c *Core) dropAttClient(stable string, conn *bus.Conn) {
	c.att.mu.Lock()
	defer c.att.mu.Unlock()
	if ac, ok := c.att.byStable[stable]; ok && (conn == nil || ac.conn == conn) {
		delete(c.att.byStable, stable)
	}
}

// dropAttClientForConn removes every binding held on a dead conn.
func (c *Core) dropAttClientForConn(conn *bus.Conn) {
	c.att.mu.Lock()
	defer c.att.mu.Unlock()
	for id, ac := range c.att.byStable {
		if ac.conn == conn {
			delete(c.att.byStable, id)
		}
	}
}
