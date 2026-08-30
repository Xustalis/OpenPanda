// Package guard is the panic firewall for long-lived goroutines. A resident
// goroutine that panics would otherwise take the whole process down with an
// unformatted stack dump; guard recovers it, logs the name and the full stack
// through slog, and lets the caller decide what "after the panic" means.
//
// Policy in this codebase:
//   - Resident daemon goroutines (heartbeat, scheduler, peer loops, …) pass an
//     onPanic that triggers a controlled shutdown. A half-alive kernel is
//     worse than a clean restart, so a panic is never silently absorbed.
//   - Per-connection read loops pass a nil onPanic: log and let that one
//     connection die. One malicious or buggy peer must not drag the node down.
//   - Startup code paths that run before the process is "up" (everything up to
//     the fatal() calls) are deliberately NOT wrapped: startup errors stay
//     crashes, loud and visible.
package guard

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Go starts fn in a new goroutine protected by panic recovery. name labels the
// goroutine in the recovered-panic log line; onPanic (may be nil) runs after
// the panic has been logged, on the same goroutine, so it can safely touch
// whatever state fn owned.
func Go(logger *slog.Logger, name string, onPanic func(), fn func()) {
	go Call(logger, name, onPanic, fn)
}

// Call is the synchronous form of Go: it runs fn in the current goroutine with
// the same recovery. It exists for call sites whose caller must block while fn
// runs (the bus server invokes one read loop per connection inside the HTTP
// handler and accounts connection limits from that blocking call), where an
// extra goroutine would change the accounting.
func Call(logger *slog.Logger, name string, onPanic func(), fn func()) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("goroutine panic recovered",
			"name", name,
			"panic", fmt.Sprint(r),
			"stack", string(debug.Stack()),
		)
		if onPanic != nil {
			onPanic()
		}
	}()
	fn()
}
