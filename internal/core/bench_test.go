// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strconv"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// BenchmarkClaimMsgID measures the per-message dedup check on the dispatch
// path, including the steady-state eviction once the table sits at its cap
// (the worst case a long-lived, busy node actually runs in).
func BenchmarkClaimMsgID(b *testing.B) {
	c := &Core{msgSeen: make(map[string]time.Time, msgSeenMax)}
	env := bus.Envelope{From: "peer-a"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.MsgID = strconv.Itoa(i)
		if !c.claimMsgID(env) {
			b.Fatal("fresh message id rejected")
		}
	}
}
