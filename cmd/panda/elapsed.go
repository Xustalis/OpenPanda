package main

import (
	"fmt"
	"time"
)

// elapsed formats a duration as a compact clock for the status line. A
// sub-second duration prints "<1s" rather than "0s": routing decisions finish
// in milliseconds, and "0s" reads as a broken timer instead of a fast stage.
// Shared by the TUI and lite builds, so it lives outside the tui_* split.
func elapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 1 {
		return "<1s"
	}
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}
