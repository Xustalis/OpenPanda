// SPDX-License-Identifier: AGPL-3.0-or-later
package plan

import (
	"github.com/Xustalis/OpenPanda/internal/entry"
	"testing"
)

func TestCriticalPathRanks(t *testing.T) {
	p := Plan{Goal: "test", Stages: []Stage{
		{ID: "short", Intent: "short"}, {ID: "long", Intent: "long", Resources: entry.ResourceProfile{DurationHint: "long"}},
		{ID: "join", Intent: "join", Needs: []string{"long"}},
	}}
	r, err := CriticalPathRanks(p)
	if err != nil || r["short"] != 1 || r["long"] != 5 || r["join"] != 1 {
		t.Fatalf("ranks=%v err=%v", r, err)
	}
	p.Stages[1].Needs = []string{"join"}
	if _, err = CriticalPathRanks(p); err == nil {
		t.Fatal("cycle accepted")
	}
}
func BenchmarkCriticalPathRanks64(b *testing.B) {
	p := Plan{Goal: "64 stages"}
	for i := 0; i < 64; i++ {
		id := string(rune('A'+i/26)) + string(rune('a'+i%26))
		s := Stage{ID: id, Intent: "work"}
		if i > 0 {
			s.Needs = []string{p.Stages[i-1].ID}
		}
		p.Stages = append(p.Stages, s)
	}
	for b.Loop() {
		_, _ = CriticalPathRanks(p)
	}
}
