// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"
)

// TestShutdownDeadlineBoundsArtifactDrain pins the contract the daemon leans
// on: Shutdown's ctx is a deadline for the whole stop, including the drain of
// detached artifact adoptions. Before this, artWG.Wait() ignored the ctx, so a
// wedged transfer could hold a restart for the full transfer budget — past the
// supervisor's stop timeout, whose SIGKILL mid-extract is the very outcome the
// drain exists to prevent.
func TestShutdownDeadlineBoundsArtifactDrain(t *testing.T) {
	c := newCore(t, "drain-node", "127.0.0.1:17999")

	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	c.goArtifact("wedged transfer", func() {
		close(started)
		<-release
	})
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	c.Shutdown(ctx)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Shutdown waited %v on a wedged transfer despite a 150ms ctx deadline", elapsed)
	}
}

// TestGoArtifactAfterShutdownRunsInline pins the other half of the drain
// contract: a result handler that reaches goArtifact while Shutdown is
// already waiting must not spawn an untracked goroutine — an Add landing at
// counter 0 after Wait began is never awaited — and must not be dropped
// either, or the adoption simply never happens. The work runs inline instead.
func TestGoArtifactAfterShutdownRunsInline(t *testing.T) {
	c := newCore(t, "drain-node2", "127.0.0.1:17998")
	c.Shutdown(context.Background())

	ran := false
	c.goArtifact("late adoption", func() { ran = true })
	if !ran {
		t.Fatal("goArtifact after Shutdown dropped the adoption instead of running it inline")
	}
}
