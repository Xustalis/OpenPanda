// Package carddetect probes host hardware, discovers installed agent CLIs,
// and assembles or initializes capability cards (capabilities.yaml).
package carddetect

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/hwinfo"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"gopkg.in/yaml.v3"
)

// DetectCard probes the host and assembles a Card draft.
func DetectCard() ledger.Card {
	cores := runtime.NumCPU()
	ramGB := hwinfo.RAMGB()
	vram := hwinfo.GPUVRAMGB()

	// Resource class from RAM: <8GB = Micro, <32GB = Standard, else Full.
	class := "Standard"
	switch {
	case ramGB > 0 && ramGB < 8:
		class = "Micro"
	case ramGB >= 32:
		class = "Full"
	}
	maxConcurrent := 2
	if cores >= 8 {
		maxConcurrent = 4
	}

	card := ledger.Card{
		Device:        hwinfo.Hostname(),
		ResourceClass: class,
		Chip:          hwinfo.CPUModel(),
		Native:        DetectNative(),
		Capacity: ledger.Capacity{
			CPUCores:      cores,
			RAMGB:         ramGB,
			MaxConcurrent: maxConcurrent,
		},
		ResourceProfile: ledger.ResourceProfile{
			CPU:          cores,
			RAMGB:        ramGB,
			GPUVRAMGB:    vram,
			DurationHint: DurationHint(cores, ramGB, vram),
		},
	}

	// Agents: probe every known agent CLI from the registry (internal/agents)
	// and prewire the ones present.
	card.Agents = CardAgents()
	return card
}

// NativeProbe defines one candidate native ability to probe on the host.
type NativeProbe struct {
	ID          string
	Command     string
	Args        []string
	Tier        int
	Description string
}

var defaultNativeProbes = []NativeProbe{
	{
		ID:          "sys:info",
		Command:     "uname",
		Args:        []string{"-a"},
		Tier:        1,
		Description: "system info",
	},
	{
		ID:          "disk:usage",
		Command:     "df",
		Args:        []string{"-h", "."},
		Tier:        1,
		Description: "disk usage of the work directory",
	},
	{
		ID:          "git",
		Command:     "git",
		Args:        []string{"status", "--short"},
		Tier:        1,
		Description: "git repository inspection",
	},
	{
		ID:          "media:ffmpeg",
		Command:     "ffmpeg",
		Args:        []string{"-version"},
		Tier:        1,
		Description: "media processing and transcoding",
	},
	{
		ID:          "docker:ps",
		Command:     "docker",
		Args:        []string{"ps"},
		Tier:        1,
		Description: "docker container inspection",
	},
	{
		ID:          "net:curl",
		Command:     "curl",
		Args:        []string{"--version"},
		Tier:        1,
		Description: "network requests and HTTP fetch",
	},
	{
		ID:          "runtime:python3",
		Command:     "python3",
		Args:        []string{"--version"},
		Tier:        1,
		Description: "python3 runtime",
	},
	{
		ID:          "runtime:node",
		Command:     "node",
		Args:        []string{"--version"},
		Tier:        1,
		Description: "nodejs runtime",
	},
	{
		ID:          "media:screencapture",
		Command:     "screencapture",
		Args:        []string{"-x", "/tmp/screencap.png"},
		Tier:        1,
		Description: "screen capture utility",
	},
}

// DetectNative probes common command-line utilities present on the host
// and returns corresponding NativeAbility entries.
func DetectNative() []ledger.NativeAbility {
	var out []ledger.NativeAbility
	for _, p := range defaultNativeProbes {
		if _, err := exec.LookPath(p.Command); err == nil {
			out = append(out, ledger.NativeAbility{
				ID:          p.ID,
				Command:     p.Command,
				Args:        p.Args,
				Tier:        p.Tier,
				Description: p.Description,
			})
		}
	}
	return out
}

// DurationHint classifies what this machine is for.
func DurationHint(cores, ramGB, vram int) string {
	if vram > 0 || vram == ledger.GPUVRAMUnknown || cores >= 8 || ramGB >= 32 {
		return "long"
	}
	return "short"
}

// CardAgents scans the agent registry and returns a card.Agents map with one
// entry per installed CLI.
func CardAgents() map[string]ledger.Agent {
	out := map[string]ledger.Agent{}
	for _, k := range agents.Registry() {
		bin := InstalledBinary(k)
		if bin == "" {
			continue
		}
		caps := k.DefaultCapabilities
		if len(caps) == 0 {
			caps = []string{"coding", "shell", "file_edit"}
		}
		bestAt := k.DefaultBestAt
		if len(bestAt) == 0 {
			bestAt = []string{"multi_file_edits", "code_search", "running_tests"}
		}
		costTier := k.DefaultCostTier
		if costTier == "" {
			costTier = "medium"
		}
		tier := k.DefaultTier
		if tier == 0 {
			tier = 2
		}
		out[k.Name] = ledger.Agent{
			Adapter:      k.Adapter,
			InstallCheck: InstallCheckFor(bin),
			Capabilities: caps,
			BestAt:       bestAt,
			NotFor:       []string{"hardware_io", "realtime_control"},
			CostTier:     costTier,
			Tier:         tier,
		}
	}
	return out
}

// InstalledBinary returns the first of an agent's known binary names that is on
// PATH (or conventional user binary directories), or "".
func InstalledBinary(k agents.Known) string {
	for _, bin := range k.Binaries {
		if bin == "" {
			continue
		}
		if _, err := exec.LookPath(bin); err == nil {
			return bin
		}
		home, _ := os.UserHomeDir()
		candidates := []string{
			filepath.Join(home, ".local", "bin", bin),
			filepath.Join("/opt/homebrew/bin", bin),
			filepath.Join("/usr/local/bin", bin),
			filepath.Join(home, ".grok", "bin", bin),
			filepath.Join(home, ".cargo", "bin", bin),
		}
		for _, cand := range candidates {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() && (st.Mode()&0o111 != 0) {
				return bin
			}
		}
	}
	return ""
}

// InstallCheckFor renders the shell one-liner a card carries so a human (or a
// remote node reading the card) can verify the CLI is really installed.
func InstallCheckFor(bin string) string {
	if runtime.GOOS == "windows" {
		return "where " + bin
	}
	return "which " + bin
}

// EnsureCard checks if the card file at path exists. If it exists, it loads
// and returns the card (created=false). If it does not exist, it runs DetectCard(),
// writes the generated YAML to path with a header, and returns the card (created=true).
func EnsureCard(path string) (ledger.Card, bool, error) {
	if path == "" {
		return ledger.Card{}, false, fmt.Errorf("ensure card: empty path")
	}
	if _, err := os.Stat(path); err == nil {
		c, err := ledger.LoadCard(path)
		return c, false, err
	}

	card := DetectCard()
	data, err := yaml.Marshal(card)
	if err != nil {
		return ledger.Card{}, false, fmt.Errorf("render capability card: %w", err)
	}

	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ledger.Card{}, false, fmt.Errorf("create card dir: %w", err)
		}
	}

	header := fmt.Sprintf("# capabilities.yaml — generated by OpenPanda on %s\n# hardware fields are auto-detected; modify or add native/agents as needed.\n\n",
		time.Now().Format("2006-01-02 15:04"))

	content := append([]byte(header), data...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return ledger.Card{}, false, fmt.Errorf("write capability card %s: %w", path, err)
	}

	return card, true, nil
}
