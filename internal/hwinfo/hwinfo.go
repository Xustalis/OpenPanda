// Package hwinfo probes the host machine's hardware identity (hostname, CPU
// model, RAM, GPU, VRAM). It is the single detection layer: `panda detect`,
// `panda card rescan` and the panel's GET /api/self all read from here, so a
// node's advertised profile cannot disagree with itself depending on which
// surface produced it.
//
// Everything is best-effort: a failed probe yields a zero/empty value, never an
// error. The one exception is VRAM, where "I could not read it" is reported as
// ledger.GPUVRAMUnknown rather than 0 — see GPUVRAMGB.
package hwinfo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/ledger"
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

// Probe runs a short-lived detection command and returns its trimmed stdout, or
// "" if it failed. Exported so callers with one-off platform probes of their own
// (VM-vendor strings, machine identity) get the same timeout and the same hidden
// console window instead of reaching for os/exec.
func Probe(name string, args ...string) string { return probe(name, args...) }

// probe runs a command with a short timeout and returns trimmed stdout.
// executil.CommandContext (not os/exec) so a probe never flashes a console
// window on Windows: the kernel runs headless there, and wmic/powershell/reg
// would each pop a terminal for a fraction of a second otherwise.
func probe(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := executil.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// powershell runs a PowerShell expression with profile loading disabled (a
// user profile can print a banner and slow startup by seconds).
func powershell(expr string) string {
	return probe("powershell", "-NoProfile", "-NonInteractive", "-Command", expr)
}

// CPUModel returns a human-readable CPU name, or an "os/arch (unknown model)"
// placeholder when no probe on this platform answers.
func CPUModel() string {
	switch runtime.GOOS {
	case "darwin":
		if out := probe("sysctl", "-n", "machdep.cpu.brand_string"); out != "" {
			return out
		}
	case "linux":
		if m := parseCPUInfoModel(readFile("/proc/cpuinfo")); m != "" {
			return m
		}
		if m := parseLscpuModel(probe("lscpu")); m != "" {
			return m
		}
		// ARM SBCs (Orange Pi, Raspberry Pi) have no "model name" in
		// /proc/cpuinfo; the board name lives in the device tree instead, and
		// it is more useful than "aarch64" anyway.
		if m := strings.Trim(readFile("/proc/device-tree/model"), "\x00 \n"); m != "" {
			return m
		}
	case "windows":
		// The registry first: it is present on every install, needs no WMI
		// service, and answers in single-digit milliseconds. wmic is absent on
		// current Windows 11 builds, so PowerShell CIM is the fallback.
		if m := parseRegSZ(probe("reg", "query",
			`HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "/v", "ProcessorNameString"),
			"ProcessorNameString"); m != "" {
			return m
		}
		if m := powershell("(Get-CimInstance Win32_Processor).Name"); m != "" {
			return strings.TrimSpace(strings.Split(m, "\n")[0])
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
				return roundGB(n)
			}
		}
	case "linux":
		if gb := parseMemTotalGB(readFile("/proc/meminfo")); gb > 0 {
			return gb
		}
	case "windows":
		if gb := parseByteCountGB(powershell("(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory")); gb > 0 {
			return gb
		}
		if gb := parseByteCountGB(probe("wmic", "ComputerSystem", "get", "TotalPhysicalMemory")); gb > 0 {
			return gb
		}
	}
	return 0
}

// GPUs lists the GPU names this machine reports (best effort, may be empty).
func GPUs() []string {
	switch runtime.GOOS {
	case "darwin":
		return parseChipsetModels(probe("system_profiler", "SPDisplaysDataType"))
	case "linux":
		// lspci names both discrete and integrated adapters, but pciutils is
		// missing from minimal and ARM images — where an NVIDIA card, if any,
		// still answers nvidia-smi.
		if gpus := parseLspciGPUs(probe("sh", "-c",
			"lspci 2>/dev/null | grep -i 'vga\\|3d controller\\|display controller' || true")); len(gpus) > 0 {
			return gpus
		}
		if gpus := parseNameList(probe("nvidia-smi", "-L")); len(gpus) > 0 {
			return gpus
		}
		// AMD/Intel without pciutils: the DRM nodes still exist.
		if cards, _ := filepath.Glob("/sys/class/drm/card[0-9]/device/vendor"); len(cards) > 0 {
			gpus := make([]string, 0, len(cards))
			for _, p := range cards {
				gpus = append(gpus, "drm "+filepath.Base(filepath.Dir(filepath.Dir(p)))+
					" (vendor "+readFile(p)+")")
			}
			return gpus
		}
	case "windows":
		if gpus := parseNameList(probe("nvidia-smi", "-L")); len(gpus) > 0 {
			return gpus
		}
		if gpus := parseColumnNames(powershell(
			"Get-CimInstance Win32_VideoController | Select-Object -ExpandProperty Name"), "Name"); len(gpus) > 0 {
			return gpus
		}
		return parseColumnNames(probe("wmic", "path", "win32_VideoController", "get", "name"), "Name")
	}
	return nil
}

// GPUVRAMGB returns the largest single GPU's VRAM in GiB.
//
// Three outcomes, and the difference between the last two decides whether the
// flagship training stage can be routed here:
//
//	0                        no GPU on this machine — a hard, honest zero
//	n > 0                    n GiB of usable video memory
//	ledger.GPUVRAMUnknown    a GPU is present but its size could not be read
//
// The sentinel exists because ledger.Node.Fits treats a declared VRAM figure as
// a hard filter. If an unreadable card reported 0, a Windows box with a 24 GiB
// card would be excluded from exactly the stage it exists to run, and the task
// would sit on an Orange Pi that honestly declares 0. Unknown must therefore
// stay permissive, while a real 0 must keep excluding.
func GPUVRAMGB() int {
	gpus := GPUs()
	if len(gpus) == 0 {
		return 0
	}
	if gb := parseNvidiaVRAMGB(probe("nvidia-smi",
		"--query-gpu=memory.total", "--format=csv,noheader,nounits")); gb > 0 {
		return gb
	}
	switch runtime.GOOS {
	case "darwin":
		if gb := parseSystemProfilerVRAM(probe("system_profiler", "SPDisplaysDataType")); gb > 0 {
			return gb
		}
		// Apple silicon reports no VRAM line: the GPU shares system memory, so
		// the honest figure is the unified pool, not "unknown".
		if runtime.GOARCH == "arm64" {
			return RAMGB()
		}
	case "linux":
		if gb := amdSysfsVRAMGB(); gb > 0 {
			return gb
		}
	case "windows":
		// Win32_VideoController.AdapterRAM is a 32-bit field: anything ≥ 4 GiB
		// reads as ~4095 MiB, so a value at the ceiling is a truncation
		// artifact and not an answer.
		if gb := parseByteCountGB(powershell(
			"(Get-CimInstance Win32_VideoController | Measure-Object -Property AdapterRAM -Maximum).Maximum")); gb > 0 && gb < 4 {
			return gb
		}
	}
	return ledger.GPUVRAMUnknown
}

// amdSysfsVRAMGB reads VRAM from the amdgpu/radeon DRM nodes, the only source
// that works without rocm-smi installed.
func amdSysfsVRAMGB() int {
	paths, _ := filepath.Glob("/sys/class/drm/card[0-9]/device/mem_info_vram_total")
	best := 0
	for _, p := range paths {
		n, err := strconv.ParseInt(strings.TrimSpace(readFile(p)), 10, 64)
		if err != nil {
			continue
		}
		if gb := roundGB(n); gb > best {
			best = gb
		}
	}
	return best
}

// Displays reports the attached display count (0 when unknown — a headless
// node and an unprobeable one are indistinguishable here, and neither wants a
// desk-pet UI started for it).
func Displays() int {
	switch runtime.GOOS {
	case "darwin":
		return strings.Count(probe("system_profiler", "SPDisplaysDataType"), "Resolution:")
	case "linux":
		if paths, _ := filepath.Glob("/sys/class/drm/card*-*/status"); len(paths) > 0 {
			n := 0
			for _, p := range paths {
				if strings.TrimSpace(readFile(p)) == "connected" {
					n++
				}
			}
			return n
		}
	case "windows":
		return len(parseColumnNames(powershell(
			"Get-CimInstance Win32_DesktopMonitor | Select-Object -ExpandProperty DeviceID"), "DeviceID"))
	}
	return 0
}

// AudioInput reports whether a capture device exists — the desk-pet voice
// entry needs one, so a node without it should not advertise voice abilities.
// Windows is approximate: CIM does not cheaply separate capture endpoints from
// playback ones, so the answer there is "an audio subsystem is present".
func AudioInput() bool {
	switch runtime.GOOS {
	case "darwin":
		out := probe("system_profiler", "SPAudioDataType")
		return strings.Contains(out, "Input Source") || strings.Contains(out, "Microphone")
	case "linux":
		// ALSA lists capture devices here without any userspace tool.
		if strings.Contains(readFile("/proc/asound/pcm"), "capture") {
			return true
		}
		return strings.Contains(probe("sh", "-c", "arecord -l 2>/dev/null || true"), "card ")
	case "windows":
		return len(parseColumnNames(powershell(
			"Get-CimInstance Win32_SoundDevice | Select-Object -ExpandProperty Name"), "Name")) > 0
	}
	return false
}

// readFile returns a file's contents, or "" — used for /proc and /sys, where
// reading directly beats shelling out to grep.
func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
