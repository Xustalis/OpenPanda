package panel

import (
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/hwinfo"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
	"github.com/Xustalis/OpenPanda/internal/updater"
	"github.com/Xustalis/OpenPanda/internal/version"
)

// selfJSON is the wire form of GET /api/self — this machine's device profile
// (C3): OS/architecture/CPU/memory probed via internal/hwinfo (the same
// helpers `panda detect` uses, sunk into the internal layer), plus this
// node's capability-card summary from the ledger when one is registered,
// plus the running Version and the self-update manager's live status (so
// the console can paint a soft banner when the updater has degraded to
// idle after a 403 / rate limit GitHub API call, instead of silently
// hiding it until the user clicks System).
type selfJSON struct {
	Hostname     string       `json:"hostname"`
	OS           string       `json:"os"`
	Arch         string       `json:"arch"`
	Chip         string       `json:"chip,omitempty"`
	CPUCores     int          `json:"cpu_cores"`
	RAMGB        int          `json:"ram_gb,omitempty"`
	NodeName     string       `json:"node_name,omitempty"`
	NodeID       string       `json:"node_id,omitempty"`
	NodeKind     string       `json:"node_kind,omitempty"`
	NodeIdentity string       `json:"node_identity,omitempty"`
	NodeRunning  bool         `json:"node_running"`
	Node         *nodeRow     `json:"node,omitempty"`
	Version      string       `json:"version"`
	Update       *updateSlice `json:"update,omitempty"`
}

// updateSlice is the panel's projection of updater.Status, with one
// derived flag, `degraded`, added: it is true when the check loop has
// backed off to idle because of a transient upstream error (rate limit,
// 403 forbidden, network offline) and will not retry until the next
// restart. The UI paints this as a soft "updates paused" banner rather
// than silently leaving the user unable to discover why no new version
// ever surfaces.
type updateSlice struct {
	Stage     string `json:"stage"`
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Available bool   `json:"available"`
	Idle      bool   `json:"idle"`
	Error     string `json:"error,omitempty"`
	// Degraded means "check has opted out of network calls this run".
	// Mirrors the StageIdle-with-error contract internal/updater uses on
	// 403/429 (rate limit / private repo access denied).
	Degraded bool `json:"degraded"`
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
		Version:  version.Version,
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
	if h.updater != nil {
		s := h.updater.Status()
		degraded := false
		// StageIdle means "not attempting anything right now". It is
		// semantically degraded when it is accompanied by a sticky error
		// (rate limit exceeded, private repo 403) — those are the cases
		// internal/updater opts into by explicitly setting the stage to
		// Idle in the error handler.
		if strings.EqualFold(string(s.Stage), string(updater.StageIdle)) && s.Error != "" {
			degraded = true
		}
		out.Update = &updateSlice{
			Stage:     string(s.Stage),
			Current:   s.Current,
			Latest:    s.Latest,
			Notes:     s.Notes,
			Available: s.Available,
			Idle:      s.Idle,
			Error:     s.Error,
			Degraded:  degraded,
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
