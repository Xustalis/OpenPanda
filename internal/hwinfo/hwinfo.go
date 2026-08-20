// Package hwinfo probes the host machine's hardware identity (hostname,
// CPU model, RAM). It is the shared, internal-layer form of the detection
// helpers `panda detect` uses, so panel endpoints like GET /api/self can
// report the device profile without re-implementing probing in webui.
// Everything is best-effort: a failed probe yields a zero/empty value,
// never an error.
package hwinfo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Hostname returns the machine's hostname, or "unknown-host" when it cannot
// be resolved.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown-host"
	}
	return h
}

// probe runs a command with a short timeout and returns trimmed stdout.
func probe(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CPUModel returns a human-readable CPU name for darwin/linux, or a
// "os/arch (unknown model)" placeholder elsewhere.
func CPUModel() string {
	switch runtime.GOOS {
	case "darwin":
		return probe("sysctl", "-n", "machdep.cpu.brand_string")
	case "linux":
		if out := probe("sh", "-c", "grep -m1 'model name' /proc/cpuinfo"); out != "" {
			if _, v, ok := strings.Cut(out, ":"); ok {
				return strings.TrimSpace(v)
			}
		}
		if out := probe("lscpu"); out != "" {
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "Model name:") {
					return strings.TrimSpace(strings.TrimPrefix(line, "Model name:"))
				}
			}
		}
	}
	return fmt.Sprintf("%s/%s (unknown model)", runtime.GOOS, runtime.GOARCH)
}

// RAMGB returns the installed memory in GiB, or 0 when it cannot be probed.
func RAMGB() int {
	switch runtime.GOOS {
	case "darwin":
		if out := probe("sysctl", "-n", "hw.memsize"); out != "" {
			if n, err := strconv.ParseInt(out, 10, 64); err == nil {
				return int(n / (1 << 30))
			}
		}
	case "linux":
		if out := probe("sh", "-c", "grep -m1 MemTotal /proc/meminfo"); out != "" {
			re := regexp.MustCompile(`(\d+)\s*kB`)
			if m := re.FindStringSubmatch(out); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					return n / (1024 * 1024)
				}
			}
		}
	}
	return 0
}
