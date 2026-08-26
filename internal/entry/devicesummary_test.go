package entry

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestDeviceSummaryShowsHardware covers the input half of resource-aware routing.
// resource_profile is a hard filter — a task asking for more VRAM than a node
// declares is refused by that node — and the entry model was being asked to fill
// it without ever being shown what any machine has. Every guess it made was
// therefore arbitrary: too high made a task unroutable, too low sent a training
// run to the weakest node in the network.
func TestDeviceSummaryShowsHardware(t *testing.T) {
	summary := summarizeDevices([]ledger.Node{
		{
			Name: "win-box", Chip: "RTX 4070",
			Native:          []ledger.NativeAbility{{ID: "sys:info"}},
			ResourceProfile: ledger.ResourceProfile{CPU: 16, RAMGB: 32, GPUVRAMGB: 12},
			Capacity:        ledger.Capacity{MaxConcurrent: 2},
		},
		{
			Name: "pi", Chip: "RK3566",
			Native:          []ledger.NativeAbility{{ID: "gpio:servo"}},
			ResourceProfile: ledger.ResourceProfile{CPU: 4, RAMGB: 4},
		},
	})

	if !strings.Contains(summary, "显存 12 GiB") {
		t.Errorf("the GPU node's VRAM is missing from the summary:\n%s", summary)
	}
	if !strings.Contains(summary, "cpu 16 核") || !strings.Contains(summary, "内存 32 GiB") {
		t.Errorf("cpu/ram missing from the summary:\n%s", summary)
	}
	// The Pi declared cpu and ram but no VRAM. It must not print "显存 0 GiB":
	// zero would read as "this machine has no GPU", a claim the card never made
	// and one the scheduler does not make either.
	if strings.Contains(summary, "显存 0") {
		t.Errorf("an undeclared VRAM figure was rendered as zero:\n%s", summary)
	}
	if !strings.Contains(summary, "未声明显存") {
		t.Errorf("the Pi's undeclared VRAM is not flagged:\n%s", summary)
	}
}

// TestDeviceSummaryUndeclaredHardware pins the pre-v0.0.6 card shape: an all-zero
// resource_profile is silence, and Fits lets such a node through rather than
// declining every GPU task. The summary has to say that, or the model will read
// the blank as "no capacity" and refuse to route work that would in fact run.
func TestDeviceSummaryUndeclaredHardware(t *testing.T) {
	summary := summarizeDevices([]ledger.Node{
		{Name: "legacy", Native: []ledger.NativeAbility{{ID: "lint"}}},
	})
	if !strings.Contains(summary, "未声明") {
		t.Errorf("an undeclared node is not marked as such:\n%s", summary)
	}
	if strings.Contains(summary, "cpu 0") || strings.Contains(summary, "内存 0") {
		t.Errorf("silence was rendered as zeroes:\n%s", summary)
	}
}

// TestCoreRulesExplainResourceProfile guards the other half: the prompt must tell
// the model that the field is a hard filter and how to size it. Without the rule,
// the skeleton's literal "gpu_vram_gb": 0 is the only signal the model gets, and
// it copies it — which is exactly why the VRAM gate never fired in real use.
func TestCoreRulesExplainResourceProfile(t *testing.T) {
	for _, want := range []string{"resource_profile", "gpu_vram_gb", "duration_hint", "硬性路由条件"} {
		if !strings.Contains(coreRules, want) {
			t.Errorf("coreRules does not mention %q", want)
		}
	}
}
