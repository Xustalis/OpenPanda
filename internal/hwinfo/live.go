// SPDX-License-Identifier: AGPL-3.0-or-later

package hwinfo

// live.go samples the compute metrics the heartbeat advertises per beat
// (v0.0.10 Track 2): memory headroom, work-disk free space, and GPU
// utilization. Each probe returns (value, ok) — ok=false means "not
// measured", which the caller reports as the -1 sentinel rather than a
// false zero that would look like an exhausted machine.

import (
	"runtime"
	"strconv"
	"strings"
)

// MemFreeGB returns usable memory in GiB: Linux MemAvailable, darwin
// free+inactive+purgeable pages, Windows FreePhysicalMemory.
func MemFreeGB() (float64, bool) {
	switch runtime.GOOS {
	case "linux":
		return parseMemAvailableGB(readFile("/proc/meminfo"))
	case "darwin":
		return parseVMStatFreeGB(probe("vm_stat"))
	case "windows":
		if kb := parseByteCountKB(powershell(
			"(Get-CimInstance Win32_OperatingSystem).FreePhysicalMemory")); kb > 0 {
			return float64(kb) / (1 << 20), true
		}
	}
	return 0, false
}

// GPUUtilPercent returns the busiest GPU's utilization in percent. Only
// NVIDIA via nvidia-smi is measured today — a machine without the driver
// reports unmeasured, not 0, so routing never mistakes "unreadable" for
// "idle".
func GPUUtilPercent() (int, bool) {
	out := probe("nvidia-smi",
		"--query-gpu=utilization.gpu", "--format=csv,noheader,nounits")
	if out == "" {
		return 0, false
	}
	best := -1
	for _, line := range strings.Split(out, "\n") {
		if v, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && v > best {
			best = v
		}
	}
	if best < 0 || best > 100 {
		return 0, false
	}
	return best, true
}
