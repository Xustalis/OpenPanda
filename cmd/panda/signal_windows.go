//go:build windows

package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Console control events (wincon.h). The Go toolchain's stdlib syscall
// package defines the constants but not the handler registration API, and
// golang.org/x/sys/windows exports only GenerateConsoleCtrlEvent — so the
// registration goes through kernel32 directly.
const (
	ctrlEventC        = 0 // CTRL_C_EVENT
	ctrlEventBreak    = 1 // CTRL_BREAK_EVENT
	ctrlEventClose    = 2 // CTRL_CLOSE_EVENT
	ctrlEventLogoff   = 5 // CTRL_LOGOFF_EVENT
	ctrlEventShutdown = 6 // CTRL_SHUTDOWN_EVENT
)

// consoleCtrlHandlerRoutine is the PHANDLER_ROUTINE signature: it receives
// the control type and reports whether it handled the event.
type consoleCtrlHandlerRoutine func(ctrlType uint32) bool

var procSetConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")

// setConsoleCtrlHandler appends (add=true) or removes a handler from this
// console's handler chain. The Go runtime holds its own handler (the one that
// turns Ctrl-C into os.Interrupt); ours sits alongside it.
func setConsoleCtrlHandler(handler consoleCtrlHandlerRoutine, add bool) error {
	r1, _, e1 := syscall.SyscallN(procSetConsoleCtrlHandler.Addr(),
		windows.NewCallback(handler), uintptr(boolToInt(add)))
	if r1 == 0 {
		return e1
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// closeCleanupWindow is how long the console handler holds the process open
// after CTRL_CLOSE/LOGOFF/SHUTDOWN before returning to the OS. Windows gives
// a console process roughly 5s after the close button; blocking here for most
// of that window is the documented way to buy graceful-shutdown time (the
// handler's return lets the OS terminate the process).
const closeCleanupWindow = 4500 * time.Millisecond

// shutdownContext returns the context that ends the process and a cancel that
// triggers a controlled shutdown. Windows consoles do not deliver SIGTERM;
// instead a console control handler catches Ctrl-C, console close, logoff and
// system shutdown and cancels the same context. The unix-flavoured
// os.Interrupt subscription stays in place so both delivery paths land on one
// cancel.
func shutdownContext() (context.Context, context.CancelFunc) {
	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	ctx, cancel := context.WithCancel(baseCtx)
	var once sync.Once
	trigger := func() { once.Do(cancel) }

	// Registration failure (no console attached, e.g. a detached service)
	// degrades to signal-only handling — Ctrl-C via os.Interrupt still works
	// through the Go runtime's own handler.
	_ = setConsoleCtrlHandler(func(ctrlType uint32) bool {
		switch ctrlType {
		case ctrlEventC, ctrlEventBreak:
			trigger()
			return true
		case ctrlEventClose, ctrlEventLogoff, ctrlEventShutdown:
			// Terminal events: the OS kills the process when this handler
			// returns (or after its ~5s budget). Trigger the shutdown, then
			// hold the handler here so the deferred cleanup on the main
			// goroutine gets its window before the kill lands.
			trigger()
			time.Sleep(closeCleanupWindow)
			return true
		}
		return false
	}, true)

	return ctx, func() {
		stop()
		cancel()
	}
}
