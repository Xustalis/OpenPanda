// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "sync/atomic"

// Mesh counters (docs/protocol.md). They answer the questions an
// operator actually asks about this plane — "is the relay carrying my
// traffic", "is the directory converging", "is punching worth trying here" —
// without a store write on the hot path. Every field is a monotonic total
// since process start; rates are the caller's subtraction.
type meshCounters struct {
	transitTxBytes atomic.Int64
	transitRxBytes atomic.Int64
	transitRelayed atomic.Int64
	transitRefused atomic.Int64
	dirRowsIn      atomic.Int64
	dirRowsOut     atomic.Int64
	dirPulls       atomic.Int64
	punchAttempts  atomic.Int64
	punchSucceeded atomic.Int64
	dialCheckOK    atomic.Int64
	dialCheckFail  atomic.Int64
}

// MeshMetrics is the exported snapshot (`panda metrics`, the console's mesh
// page). Names match the design's docs/protocol.md list where it has one.
type MeshMetrics struct {
	TransitBytesTx int64 `json:"transit_bytes_tx"`
	TransitBytesRx int64 `json:"transit_bytes_rx"`
	TransitRelayed int64 `json:"transit_relayed"`
	TransitRefused int64 `json:"transit_refused"`
	DirRowsIn      int64 `json:"dir_rows_in"`
	DirRowsOut     int64 `json:"dir_rows_out"`
	DirPulls       int64 `json:"dir_pulls"`
	PunchAttempts  int64 `json:"punch_attempts"`
	PunchSuccess   int64 `json:"punch_success"`
	DialCheckOK    int64 `json:"dial_check_ok"`
	DialCheckFail  int64 `json:"dial_check_fail"`
}

// MeshMetrics returns a consistent-enough read of the counters. Each field is
// read atomically on its own; a snapshot taken mid-flight may see one counter
// a few microseconds ahead of another, which is exactly what monotonic
// counters are for.
func (c *Core) MeshMetrics() MeshMetrics {
	return MeshMetrics{
		TransitBytesTx: c.mesh.transitTxBytes.Load(),
		TransitBytesRx: c.mesh.transitRxBytes.Load(),
		TransitRelayed: c.mesh.transitRelayed.Load(),
		TransitRefused: c.mesh.transitRefused.Load(),
		DirRowsIn:      c.mesh.dirRowsIn.Load(),
		DirRowsOut:     c.mesh.dirRowsOut.Load(),
		DirPulls:       c.mesh.dirPulls.Load(),
		PunchAttempts:  c.mesh.punchAttempts.Load(),
		PunchSuccess:   c.mesh.punchSucceeded.Load(),
		DialCheckOK:    c.mesh.dialCheckOK.Load(),
		DialCheckFail:  c.mesh.dialCheckFail.Load(),
	}
}
