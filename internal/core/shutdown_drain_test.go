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
