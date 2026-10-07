// SPDX-License-Identifier: AGPL-3.0-or-later

package scheduler

import (
	"fmt"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// BenchmarkRouteAtP measures one routing decision across a small mesh (self
// plus eight peers): candidate scan, ability match, hardware fit, DCPS
// scoring and the freshness weight. It is the per-delegation hot path the
// ledger's BenchmarkMatches feeds into.
func BenchmarkRouteAtP(b *testing.B) {
	const now = 1_700_000_000
	nodes := make([]ledger.Node, 0, 9)
	nodes = append(nodes, ledger.Node{
		ID: "macbook", Name: "macbook", Status: "online", SchedulerTier: 5,
		LastSeen: now - 5,
		Capacity: ledger.Capacity{MaxConcurrent: 4, CurrentTasks: 1},
		Native:   []ledger.NativeAbility{{ID: "sys:info"}},
		Agents: map[string]ledger.Agent{
			"claude_code": {Capabilities: []string{"code:modify", "code:review", "code:debug", "file:analyze"}},
		},
	})
	for i := 0; i < 8; i++ {
		nodes = append(nodes, ledger.Node{
			ID: fmt.Sprintf("peer-%d", i), Name: fmt.Sprintf("peer-%d", i),
			Status: "online", SchedulerTier: 5,
			LastSeen: now - int64(i*30),
			Capacity: ledger.Capacity{MaxConcurrent: 4, CurrentTasks: i % 4},
			Native:   []ledger.NativeAbility{{ID: "build:linux"}, {ID: "lint"}},
			Agents: map[string]ledger.Agent{
				"claude_code": {Capabilities: []string{"code:modify", "code:review", "code:debug", "test:generate"}},
				"opencode":    {Capabilities: []string{"code:modify", "web:search"}},
			},
			Manual: []ledger.ManualAbility{{ID: "design:figma"}},
		})
	}
	required := []string{"sys:info", "code:modify"}
	localMatch := func(req []string) bool { return true }

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := RouteAtP("macbook", nil, nodes, localMatch, required, ledger.ResourceProfile{}, "", "", now)
		if d.Action != ActionLocal && d.Action != ActionForward {
			b.Fatalf("unexpected decision %+v", d)
		}
	}
}
