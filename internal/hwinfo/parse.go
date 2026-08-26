package hwinfo

import (
	"regexp"
	"strconv"
	"strings"
)

// The parsers below are pure: probing runs an OS-specific command, parsing its
// output is testable on every platform. Every one of them tolerates junk —
// a missing tool, a localised header, a deprecation warning on stderr merged
// into stdout — and answers with a zero value rather than a guess.

var (
	reMemTotalKB = regexp.MustCompile(`(\d+)\s*kB`)
	reDigits     = regexp.MustCompile(`\d{4,}`) // byte counts, never a 3-digit one
	reGBValue    = regexp.MustCompile(`(\d+)\s*GB`)
)

// parseCPUInfoModel pulls the model out of a /proc/cpuinfo "model name" line.
// ARM boards (the Orange Pi included) have no such line, which is why the
// caller falls back to lscpu and then to the device tree.
func parseCPUInfoModel(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "model name") {
			continue
		}
		if _, v, ok := strings.Cut(line, ":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseLscpuModel reads lscpu's "Model name:" field. Localised lscpu output
// uses a translated label, so a miss here is normal, not an error.
func parseLscpuModel(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Model name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Model name:"))
		}
	}
	return ""
}

// parseMemTotalGB converts a /proc/meminfo MemTotal line to whole GiB, rounding
// to nearest: a "16 GB" stick reports 16305416 kB (firmware reserves the rest),
// and truncating would advertise 15 GiB and lose tasks that ask for 16.
func parseMemTotalGB(out string) int {
	m := reMemTotalKB.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	kb, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0
	}
	return roundGB(kb * 1024)
}

// parseByteCountGB extracts the first long integer from noisy output (wmic
// prints a "TotalPhysicalMemory" header above the value, PowerShell prints the
// value alone) and converts it to GiB.
func parseByteCountGB(out string) int {
	m := reDigits.FindString(out)
	if m == "" {
		return 0
	}
	n, err := strconv.ParseInt(m, 10, 64)
	if err != nil {
		return 0
	}
	return roundGB(n)
}

// roundGB converts bytes to GiB, rounded to nearest, never negative.
func roundGB(bytes int64) int {
	if bytes <= 0 {
		return 0
	}
	const gib = 1 << 30
	return int((bytes + gib/2) / gib)
}

// parseNvidiaVRAMGB reads `nvidia-smi --query-gpu=memory.total` output (one
// MiB figure per GPU) and returns the LARGEST card's VRAM in GiB.
//
// The largest, not the sum: VRAM is not poolable across cards without the model
// being written for it, so a task that needs 8 GiB needs one card with 8 GiB.
// Summing would advertise a fit this node cannot honour and the training stage
// would die here instead of being routed to a machine that can run it.
func parseNvidiaVRAMGB(out string) int {
	best := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// "24564" with nounits, "24564 MiB" without it.
		fields := strings.FieldsFunc(line, func(r rune) bool { return r < '0' || r > '9' })
		if len(fields) == 0 {
			continue
		}
		mib, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if gb := roundGB(int64(mib) * (1 << 20)); gb > best {
			best = gb
		}
	}
	return best
}

// parseSystemProfilerVRAM reads macOS SPDisplaysDataType VRAM lines.
// Apple silicon reports no VRAM line at all (memory is unified), which is why
// the caller treats a miss there as unified memory rather than as zero.
func parseSystemProfilerVRAM(out string) int {
	best := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "VRAM") {
			continue
		}
		if m := reGBValue.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > best {
				best = n
			}
		}
	}
	return best
}

// parseChipsetModels reads the GPU names out of SPDisplaysDataType.
func parseChipsetModels(out string) []string {
	var gpus []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "Chipset Model:") {
			continue
		}
		if _, v, ok := strings.Cut(strings.TrimSpace(line), "Chipset Model:"); ok {
			if name := strings.TrimSpace(v); name != "" {
				gpus = append(gpus, name)
			}
		}
	}
	return gpus
}

// parseLspciGPUs reads the device names from pre-filtered lspci lines.
func parseLspciGPUs(out string) []string {
	var gpus []string
	for _, line := range strings.Split(out, "\n") {
		if _, v, ok := strings.Cut(line, ": "); ok {
			if name := strings.TrimSpace(v); name != "" {
				gpus = append(gpus, name)
			}
		}
	}
	return gpus
}

// parseNameList reads `nvidia-smi -L` lines:
// "GPU 0: NVIDIA GeForce RTX 4090 (UUID: GPU-…)". Used where lspci is absent
// (minimal container and ARM images often ship no pciutils) and on Windows.
func parseNameList(out string) []string {
	var gpus []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, v, ok := strings.Cut(line, ": "); ok && strings.HasPrefix(line, "GPU ") {
			line = strings.TrimSpace(v)
		}
		if i := strings.Index(line, " (UUID:"); i > 0 {
			line = strings.TrimSpace(line[:i])
		}
		gpus = append(gpus, line)
	}
	return gpus
}

// parseColumnNames reads a one-column listing (PowerShell/wmic device names),
// dropping the header row and blank padding lines wmic emits.
func parseColumnNames(out string, header string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "\r"))
		if line == "" || strings.EqualFold(line, header) {
			continue
		}
		names = append(names, line)
	}
	return names
}

// parseRegSZ reads a value out of `reg query` output:
// "    ProcessorNameString    REG_SZ    AMD Ryzen 9 5950X".
func parseRegSZ(out, value string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, value) {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if strings.HasPrefix(f, "REG_") && i+1 < len(fields) {
				return strings.Join(fields[i+1:], " ")
			}
		}
	}
	return ""
}
