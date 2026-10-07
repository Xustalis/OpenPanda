// SPDX-License-Identifier: AGPL-3.0-or-later

package scheduler

import (
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestRouteSkipsDiskStarvedPeer is the Track-2 consumption contract: a peer
// whose own live sample says its work disk is nearly full must lose the
// task to a peer with headroom — forwarding there would only fail when the
// result or worktree has nowhere to land.
func TestRouteSkipsDiskStarvedPeer(t *testing.T) {
	now := time.Now().Unix()
	self := ledger.Node{ID: "pi", Name: "pi", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5, CurrentTasks: 2}} // busy, so a surviving peer wins
	full := ledger.Node{ID: "full", Name: "full", Status: "online", LastSeen: now, SchedulerTier: 10,
		Native: []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5,
			Live: &ledger.LiveMetrics{DiskFreeGB: 0.1, MemFreeGB: -1, GPUUtil: -1}}}
	roomy := ledger.Node{ID: "roomy", Name: "roomy", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native: []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5,
			Live: &ledger.LiveMetrics{DiskFreeGB: 40, MemFreeGB: -1, GPUUtil: -1}}}

	d := RouteAt("pi", []string{"pi"}, []ledger.Node{self, full, roomy}, alwaysLocal,
		[]string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionForward || d.Target != "roomy" {
		t.Fatalf("disk-starved peer must be skipped: %+v", d)
	}
}

// TestRouteDiskStarvedSelfDefers is the symmetric case: when the local node's
// own sample is under the floor, home must not win just because it is home.
func TestRouteDiskStarvedSelfDefers(t *testing.T) {
	now := time.Now().Unix()
	self := ledger.Node{ID: "pi", Name: "pi", Status: "online", LastSeen: now, SchedulerTier: 10,
		Native: []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5,
			Live: &ledger.LiveMetrics{DiskFreeGB: 0.05, MemFreeGB: -1, GPUUtil: -1}}}
	peer := ledger.Node{ID: "mac", Name: "mac", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5}}

	d := RouteAt("pi", []string{"pi"}, []ledger.Node{self, peer}, alwaysLocal,
		[]string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionForward || d.Target != "mac" {
		t.Fatalf("disk-starved self must defer to peer: %+v", d)
	}
}

// TestRouteUnmeasuredDiskStaysCandidate guards the absence-of-data rule: a
// peer with no live block (old version, probe-less build) and one with the
// -1 sentinel must not be excluded — a floor applied to unmeasured data
// would empty the fleet's candidate set on upgrade day.
func TestRouteUnmeasuredDiskStaysCandidate(t *testing.T) {
	now := time.Now().Unix()
	self := ledger.Node{ID: "pi", Name: "pi", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 1, CurrentTasks: 1}} // busy, so peer wins
	oldPeer := ledger.Node{ID: "old", Name: "old", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5}} // no Live block at all

	d := RouteAt("pi", []string{"pi"}, []ledger.Node{self, oldPeer}, alwaysLocal,
		[]string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionForward || d.Target != "old" {
		t.Fatalf("unmeasured peer must stay a candidate: %+v", d)
	}
}

// TestResourceEfficiencyMemoryHeadroom verifies the live discount: identical
// slot counts must still rank the truly roomier node higher once measured
// memory enters the term, while a node reporting nothing keeps the plain
// slot ratio.
func TestResourceEfficiencyMemoryHeadroom(t *testing.T) {
	base := ledger.Node{Capacity: ledger.Capacity{MaxConcurrent: 4, CurrentTasks: 0, RAMGB: 16}}
	if got := resourceEfficiency(base); got != 1 {
		t.Fatalf("idle eff = %v, want 1", got)
	}
	tight := base
	tight.Capacity.Live = &ledger.LiveMetrics{MemFreeGB: 0.8, DiskFreeGB: -1, GPUUtil: -1}
	roomy := base
	roomy.Capacity.Live = &ledger.LiveMetrics{MemFreeGB: 12, DiskFreeGB: -1, GPUUtil: -1}
	eTight, eRoomy := resourceEfficiency(tight), resourceEfficiency(roomy)
	if eRoomy <= eTight {
		t.Fatalf("measured headroom must outrank tight memory: roomy %v tight %v", eRoomy, eTight)
	}
	if eTight < 0.09 || eTight > 0.11 {
		t.Fatalf("tight node should sit at the 0.1 floor, got %v", eTight)
	}
}
