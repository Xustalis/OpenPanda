// SPDX-License-Identifier: AGPL-3.0-or-later
package queue

import "testing"

func TestCriticalPathPreservesOtherPlansPriorityAndManualOrder(t *testing.T) {
	tasks := []ReadyTask{
		{ID: "short", PlanID: "a", Priority: 1, CreatedAt: 1, CriticalPathRank: 1},
		{ID: "other", PlanID: "b", Priority: 1, CreatedAt: 2, CriticalPathRank: 90},
		{ID: "long", PlanID: "a", Priority: 1, CreatedAt: 3, CriticalPathRank: 7},
		{ID: "manual", PlanID: "a", Priority: 1, CreatedAt: 4, Seq: 1},
		{ID: "high", PlanID: "a", Priority: 0, CreatedAt: 5},
		{ID: "low", PlanID: "a", Priority: 2, CreatedAt: 6, CriticalPathRank: 99},
	}
	sortReady(tasks)
	want := []string{"manual", "high", "long", "other", "short", "low"}
	for i, v := range want {
		if tasks[i].ID != v {
			t.Fatalf("index %d got %s want %s", i, tasks[i].ID, v)
		}
	}
}
func BenchmarkSortReadyGraph64(b *testing.B) {
	original := make([]ReadyTask, 64)
	for i := range original {
		original[i] = ReadyTask{PlanID: "p", Priority: 1, CreatedAt: int64(i), CriticalPathRank: i}
	}
	tasks := make([]ReadyTask, 64)
	for b.Loop() {
		copy(tasks, original)
		sortReady(tasks)
	}
}

// Deterministic scheduling experiment, not a model latency claim. Two slots,
// two short leaves and one long chain: FIFO delays the chain by one unit.
func TestCriticalPathImprovesConstrainedGraphMakespan(t *testing.T) {
	measure := func(ranked bool) int {
		tasks := []ReadyTask{{ID: "a", PlanID: "p", Priority: 1, CreatedAt: 1, CriticalPathRank: 1}, {ID: "b", PlanID: "p", Priority: 1, CreatedAt: 2, CriticalPathRank: 1}, {ID: "c", PlanID: "p", Priority: 1, CreatedAt: 3, CriticalPathRank: 8}}
		if ranked {
			sortReady(tasks)
		}
		end := []int{0, 0}
		chainFinish := 0
		durations := map[string]int{"a": 1, "b": 1, "c": 4}
		for _, task := range tasks {
			slot := 0
			if end[1] < end[0] {
				slot = 1
			}
			end[slot] += durations[task.ID]
			if task.ID == "c" {
				chainFinish = end[slot] + 4
			}
		}
		return max(chainFinish, max(end[0], end[1]))
	}
	before, after := measure(false), measure(true)
	t.Logf("METRIC synthetic_two_slots fifo_work_units=%d critical_path_work_units=%d", before, after)
	if before != 9 || after != 8 {
		t.Fatalf("before=%d after=%d", before, after)
	}
}
