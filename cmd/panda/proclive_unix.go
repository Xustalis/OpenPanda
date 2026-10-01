//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// daemonProcStats returns a one-line "rss_kb cpu% uptime" detail for pid when
// the process is alive — `panda metrics --runtime` reports the daemon's load
// through its pid file because the daemon keeps no introspection endpoint.
// signal-0 is the portable liveness probe; ps carries the numbers.
func daemonProcStats(pid int) (string, bool) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return "", false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return "", false
	}
	out, err := exec.Command("ps", "-o", "rss=,%cpu=,etime=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "alive", true
	}
	fields := strings.Fields(string(out))
	if len(fields) == 3 {
		// rss is KiB — render MiB so the line reads like the rest of metrics.
		if rss, err := strconv.ParseFloat(fields[0], 64); err == nil {
			return "rss=" + strconv.FormatFloat(rss/1024, 'f', 1, 64) + "MiB cpu=" + fields[1] + "% uptime=" + fields[2], true
		}
	}
	return strings.TrimSpace(string(out)), true
}
