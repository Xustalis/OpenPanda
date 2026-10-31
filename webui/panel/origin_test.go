// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// The task wire's origin attribution is the board's device-filter dimension:
// chain[0] names the device that FIRST created the row, and origin_local
// must read true only when that device is this console. These branches are
// what separates "my machine's own work" from "a VM driving this machine
// over the mesh" — the board's whole reason to show the chip.
func TestToTaskJSONOriginAttribution(t *testing.T) {
	cfg := &config.Config{Node: config.NodeConfig{Name: "panel-origin-test", Kind: "vm"}}
	self := localNodeID(cfg)
	if self == "" {
		t.Fatal("localNodeID returned empty for a named config")
	}

	tests := []struct {
		name        string
		task        core.Task
		wantOrigin  string
		wantIsLocal bool
	}{
		{
			name:        "locally submitted row carries itself as origin",
			task:        core.Task{TaskID: "t-local", Chain: []string{self}},
			wantOrigin:  self,
			wantIsLocal: true,
		},
		{
			name:        "delegated row points at the delegating device",
			task:        core.Task{TaskID: "t-delegated", Chain: []string{"vm-peer@vm-abc", self}, Remote: true},
			wantOrigin:  "vm-peer@vm-abc",
			wantIsLocal: false,
		},
		{
			// Rows that predate the chain column: the persisted remote flag
			// is the only provenance left.
			name:        "legacy local row with no chain reads local",
			task:        core.Task{TaskID: "t-legacy-local"},
			wantOrigin:  "",
			wantIsLocal: true,
		},
		{
			name:        "legacy remote row with no chain reads foreign",
			task:        core.Task{TaskID: "t-legacy-remote", Remote: true},
			wantOrigin:  "",
			wantIsLocal: false,
		},
		{
			// Degraded static mode (no config loaded): the chain is still
			// surfaced, but nothing may claim locality it cannot verify.
			name:        "nil cfg keeps the origin but never claims local",
			task:        core.Task{TaskID: "t-nocfg", Chain: []string{self}},
			wantOrigin:  self,
			wantIsLocal: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &handler{cfg: cfg}
			if tc.name == "nil cfg keeps the origin but never claims local" {
				h = &handler{}
			}
			out := h.toTaskJSON(tc.task)
			if out.OriginNode != tc.wantOrigin {
				t.Fatalf("origin_node = %q, want %q", out.OriginNode, tc.wantOrigin)
			}
			if out.OriginLocal != tc.wantIsLocal {
				t.Fatalf("origin_local = %v, want %v", out.OriginLocal, tc.wantIsLocal)
			}
		})
	}
}
