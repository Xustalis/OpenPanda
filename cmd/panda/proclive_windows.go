//go:build windows

package main

import "golang.org/x/sys/windows"

// daemonProcStats reports liveness only: Windows has no ps, and per-process
// RSS would need GetProcessMemoryInfo — the pid check alone still answers
// the question `panda metrics --runtime` is usually asked ("is the daemon up").
func daemonProcStats(pid int) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", false
	}
	_ = windows.CloseHandle(h)
	return "alive", true
}
