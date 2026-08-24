package panel

import (
	"net/http"
	"runtime"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/hwinfo"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
)

// selfJSON is the wire form of GET /api/self — this machine's device profile
// (C3): OS/architecture/CPU/memory probed via internal/hwinfo (the same
// helpers `panda detect` uses, sunk into the internal layer), plus this
// node's capability-card summary from the ledger when one is registered.
type selfJSON struct {
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Chip         string   `json:"chip,omitempty"`
	CPUCores     int      `json:"cpu_cores"`
	RAMGB        int      `json:"ram_gb,omitempty"`
	NodeName     string   `json:"node_name,omitempty"`
	NodeID       string   `json:"node_id,omitempty"`
	NodeKind     string   `json:"node_kind,omitempty"`
	NodeIdentity string   `json:"node_identity,omitempty"`
	NodeRunning  bool     `json:"node_running"`
	Node         *nodeRow `json:"node,omitempty"`
}

// nodeRow is the full decoded directory row: the hardware/resources/agents
// breakdown the nodes page expands. Shared by GET /api/self and the extended
// GET /api/nodes.
type nodeRow struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Status          string                     `json:"status"`
	NodeKind        string                     `json:"node_kind"`
	NodeIdentity    string                     `json:"node_identity,omitempty"`
	IsLocal         bool                       `json:"is_local,omitempty"`
	Running         bool                       `json:"running"`
	Chip            string                     `json:"chip,omitempty"`
	LastSeen        string                     `json:"last_seen"`
	SchedulerTier   int                        `json:"scheduler_tier"`
	Abilities       []string                   `json:"abilities"`
	NativeIDs       []string                   `json:"native_ids,omitempty"`
	Agents          map[string]nodeAgentDetail `json:"agents,omitempty"`
	Capacity        ledger.Capacity            `json:"capacity"`
	ResourceProfile *ledger.ResourceProfile    `json:"resource_profile,omitempty"`
}

type nodeAgentDetail struct {
	Capabilities []string `json:"capabilities,omitempty"`
	BestAt       []string `json:"best_at,omitempty"`
	NotFor       []string `json:"not_for,omitempty"`
	CostTier     string   `json:"cost_tier,omitempty"`
	Tier         int      `json:"tier,omitempty"`
}

// getSelf serves GET /api/self — the local device profile and, when the
// ledger knows this node (matched by the configured node name), its
// capability-card summary.
func (h *handler) getSelf(w http.ResponseWriter, r *http.Request) {
	out := selfJSON{
		Hostname: hwinfo.Hostname(),
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Chip:     hwinfo.CPUModel(),
		CPUCores: runtime.NumCPU(),
		RAMGB:    hwinfo.RAMGB(),
	}
	if h.cfg != nil {
		out.NodeName = h.cfg.Node.Name
		out.NodeID = localNodeID(h.cfg)
		out.NodeKind = h.cfg.Node.Kind
		out.NodeIdentity = h.cfg.Node.EffectiveIdentity()
		held, err := nodeidentity.Held(h.cfg.Node.Kind, h.cfg.Node.EffectiveIdentity())
		out.NodeRunning = err == nil && held
	}
	if h.db != nil && out.NodeName != "" {
		if nodes, err := ledger.Query(h.db, "", ""); err == nil {
			for _, n := range nodes {
				if n.ID != localNodeID(h.cfg) {
					continue
				}
				row := toNodeRow(n)
				row.IsLocal = true
				out.Node = &row
				out.NodeRunning = out.NodeRunning && n.Status == "online"
				break
			}
		}
	}
	writeJSON(w, out)
}

// toNodeRow converts one decoded ledger row to its wire form.
func toNodeRow(n ledger.Node) nodeRow {
	row := nodeRow{
		ID:            n.ID,
		Name:          n.Name,
		Status:        n.Status,
		NodeKind:      n.NodeKind,
		NodeIdentity:  n.NodeIdentity,
		Running:       n.Status == "online" && n.LastSeen > 0 && time.Now().Unix()-n.LastSeen <= 45,
		Chip:          n.Chip,
		LastSeen:      "never",
		SchedulerTier: n.SchedulerTier,
		Abilities:     n.Abilities(),
		Capacity:      n.Capacity,
	}
	if n.LastSeen != 0 {
		row.LastSeen = ts(n.LastSeen)
	}
	for _, ab := range n.Native {
		row.NativeIDs = append(row.NativeIDs, ab.ID)
	}
	if len(n.Agents) > 0 {
		row.Agents = make(map[string]nodeAgentDetail, len(n.Agents))
		for name, ag := range n.Agents {
			row.Agents[name] = nodeAgentDetail{
				Capabilities: ag.Capabilities,
				BestAt:       ag.BestAt,
				NotFor:       ag.NotFor,
				CostTier:     ag.CostTier,
				Tier:         ag.Tier,
			}
		}
	}
	if row.ResourceProfile == nil && (n.ResourceProfile.CPU != 0 || n.ResourceProfile.RAMGB != 0 || n.ResourceProfile.GPUVRAMGB != 0 || n.ResourceProfile.DurationHint != "") {
		rp := n.ResourceProfile
		row.ResourceProfile = &rp
	}
	return row
}

func localNodeID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
}
