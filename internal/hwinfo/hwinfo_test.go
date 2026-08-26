package hwinfo

import (
	"runtime"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// nvidia-smi is the one VRAM source that works on both Linux and Windows, so
// its parsing is what decides whether the compute node advertises its card.
func TestParseNvidiaVRAMGB(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"nounits, single card", "24564", 24},
		// Two cards: the largest one, never the sum — a model needing 24 GiB
		// cannot be split across a 24 and an 8 without being written for it.
		{"two cards", "24564\n8188\n", 24},
		{"with units", "24564 MiB\n", 24},
		// 8188 MiB is a nominal 8 GiB card minus firmware reserve; truncating
		// would advertise 7 and lose every task asking for 8.
		{"rounds up to the nominal size", "8188", 8},
		{"blank", "", 0},
		{"tool missing (empty stdout)", "\n\n", 0},
		{"unparseable", "N/A\n[insufficient permissions]\n", 0},
	}
	for _, tc := range cases {
		if got := parseNvidiaVRAMGB(tc.out); got != tc.want {
			t.Errorf("%s: parseNvidiaVRAMGB(%q) = %d, want %d", tc.name, tc.out, got, tc.want)
		}
	}
}

func TestParseMemTotalGB(t *testing.T) {
	// A 16 GiB machine reports less than 16 GiB to the kernel; rounding to
	// nearest is what makes it advertise 16 instead of 15.
	if got := parseMemTotalGB("MemTotal:       16305416 kB\nMemFree: 100 kB\n"); got != 16 {
		t.Errorf("16 GiB host parsed as %d GiB", got)
	}
	// Orange Pi 3B, 2 GiB.
	if got := parseMemTotalGB("MemTotal:        1994856 kB"); got != 2 {
		t.Errorf("2 GiB board parsed as %d GiB", got)
	}
	if got := parseMemTotalGB(""); got != 0 {
		t.Errorf("empty meminfo parsed as %d GiB", got)
	}
}

func TestParseByteCountGB(t *testing.T) {
	// wmic prints a header row above the value; PowerShell prints it bare.
	if got := parseByteCountGB("TotalPhysicalMemory\r\n34266218496\r\n\r\n"); got != 32 {
		t.Errorf("wmic 32 GiB parsed as %d GiB", got)
	}
	if got := parseByteCountGB("34266218496"); got != 32 {
		t.Errorf("powershell 32 GiB parsed as %d GiB", got)
	}
	if got := parseByteCountGB("TotalPhysicalMemory\r\n\r\n"); got != 0 {
		t.Errorf("header-only output parsed as %d GiB", got)
	}
}

func TestParseCPUModels(t *testing.T) {
	if got := parseCPUInfoModel("processor\t: 0\nmodel name\t: AMD Ryzen 9 5950X 16-Core Processor\n"); got != "AMD Ryzen 9 5950X 16-Core Processor" {
		t.Errorf("cpuinfo model = %q", got)
	}
	// ARM boards have no model name line at all — the miss is expected and the
	// caller falls through to lscpu and the device tree.
	if got := parseCPUInfoModel("processor\t: 0\nBogoMIPS\t: 48.00\nCPU part\t: 0xd05\n"); got != "" {
		t.Errorf("ARM cpuinfo produced a model %q; want the caller to fall through", got)
	}
	if got := parseLscpuModel("Architecture:  aarch64\nModel name:    Cortex-A55\n"); got != "Cortex-A55" {
		t.Errorf("lscpu model = %q", got)
	}
	if got := parseLscpuModel("架构：            aarch64\n"); got != "" {
		t.Errorf("localised lscpu produced %q; want a miss, not a wrong answer", got)
	}
}

func TestParseRegSZ(t *testing.T) {
	out := "HKEY_LOCAL_MACHINE\\HARDWARE\\DESCRIPTION\\System\\CentralProcessor\\0\r\n" +
		"    ProcessorNameString    REG_SZ    12th Gen Intel(R) Core(TM) i7-12700K\r\n"
	if got := parseRegSZ(out, "ProcessorNameString"); got != "12th Gen Intel(R) Core(TM) i7-12700K" {
		t.Errorf("reg value = %q", got)
	}
	if got := parseRegSZ(out, "MachineGuid"); got != "" {
		t.Errorf("absent value returned %q", got)
	}
}

func TestParseGPUNameLists(t *testing.T) {
	got := parseNameList("GPU 0: NVIDIA GeForce RTX 4090 (UUID: GPU-1234)\nGPU 1: NVIDIA A100 (UUID: GPU-5678)\n")
	if len(got) != 2 || got[0] != "NVIDIA GeForce RTX 4090" || got[1] != "NVIDIA A100" {
		t.Errorf("nvidia-smi -L parsed as %q", got)
	}
	if got := parseColumnNames("Name\r\nNVIDIA GeForce RTX 4090\r\n\r\n", "Name"); len(got) != 1 || got[0] != "NVIDIA GeForce RTX 4090" {
		t.Errorf("wmic column parsed as %q", got)
	}
	if got := parseChipsetModels("      Chipset Model: Apple M1 Pro\n      Type: GPU\n"); len(got) != 1 || got[0] != "Apple M1 Pro" {
		t.Errorf("system_profiler chipset parsed as %q", got)
	}
	if got := parseLspciGPUs("00:02.0 VGA compatible controller: Intel Corporation UHD Graphics 770\n"); len(got) != 1 || got[0] != "Intel Corporation UHD Graphics 770" {
		t.Errorf("lspci parsed as %q", got)
	}
	if got := parseSystemProfilerVRAM("          VRAM (Total): 8 GB\n"); got != 8 {
		t.Errorf("VRAM line parsed as %d", got)
	}
}

// The probes themselves must never return a value that the card loader would
// then reject, on whatever machine the test happens to run.
func TestLiveProbesProduceAValidProfile(t *testing.T) {
	p := ledger.ResourceProfile{
		CPU:          runtime.NumCPU(),
		RAMGB:        RAMGB(),
		GPUVRAMGB:    GPUVRAMGB(),
		DurationHint: "short",
	}
	if p.RAMGB < 0 {
		t.Errorf("RAMGB() = %d", p.RAMGB)
	}
	if p.GPUVRAMGB < 0 && p.GPUVRAMGB != ledger.GPUVRAMUnknown {
		t.Errorf("GPUVRAMGB() = %d, which is neither a size nor the unknown sentinel", p.GPUVRAMGB)
	}
	if CPUModel() == "" {
		t.Error("CPUModel() is empty; the placeholder should have been returned")
	}
	if Hostname() == "" {
		t.Error("Hostname() is empty")
	}
	// A node with no GPU must report a hard 0, not the sentinel: that is what
	// keeps the Orange Pi out of GPU work.
	if len(GPUs()) == 0 && p.GPUVRAMGB != 0 {
		t.Errorf("no GPUs detected but VRAM = %d, want a hard 0", p.GPUVRAMGB)
	}
}
