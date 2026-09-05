//go:build windows

package main

import "testing"

// TestShutdownContext is a regression test for the console control handler
// signature: the handler routine used to return bool, which windows.NewCallback
// rejects at runtime, so every command that called shutdownContext panicked on
// startup. shutdownContext must return a working context whose cancel closes it.
func TestShutdownContext(t *testing.T) {
	ctx, cancel := shutdownContext()
	cancel()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("shutdown context was not cancelled")
	}
}
