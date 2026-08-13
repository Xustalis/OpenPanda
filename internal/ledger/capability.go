// Package ledger manages the local capability directory (SQLite cache of
// node capability cards) and this node's self-registration.
//
// In Phase 0 the ledger is fully local: each node registers its own card and
// heartbeats are recorded locally. A remote employee table is a later phase.
package ledger

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xenith/panda/internal/storage"
)

// Card is the parsed form of capabilities.yaml for this node.
type Card struct {
	Device        string           `yaml:"device"`
	ResourceClass string           `yaml:"resource_class"`
	Chip          string           `yaml:"chip"`
	Native        []NativeAbility  `yaml:"native"`
	Agents        map[string]Agent `yaml:"agents"`
	Manual        []ManualAbility  `yaml:"manual"`
	Capacity      Capacity         `yaml:"capacity"`
}

// NativeAbility is a deterministic command this node can run.
type NativeAbility struct {
	ID          string   `yaml:"id"`
	Command     string   `yaml:"command"`
	Args        []string `yaml:"args"`
	Tier        int      `yaml:"tier"` // 1=reversible (default) 2=irreversible (needs auth)
	Description string   `yaml:"description"`
}

// Agent is an installed agent CLI + its capabilities.
type Agent struct {
	Adapter      string   `yaml:"adapter"`
	InstallCheck string   `yaml:"install_check"`
	Capabilities []string `yaml:"capabilities"`
	BestAt       []string `yaml:"best_at"`
	NotFor       []string `yaml:"not_for"`
	CostTier     string   `yaml:"cost_tier"`
}

// ManualAbility is a human-performed task.
type ManualAbility struct {
	ID     string `yaml:"id"`
	Notify string `yaml:"notify"`
}

// Capacity describes current resource availability.
type Capacity struct {
	CPUCores      int `yaml:"cpu_cores" json:"cpu_cores"`
	RAMGB         int `yaml:"ram_gb" json:"ram_gb"`
	MaxConcurrent int `yaml:"max_concurrent_tasks" json:"max_concurrent_tasks"`
	CurrentTasks  int `yaml:"current_tasks" json:"current_tasks"`
}

// CapabilitySummary is the compact capability profile a node advertises in its
// hello handshake (design doc §2.1 capability exchange). It carries only what
// routing needs — ability IDs, scheduler tier, and current capacity — not the
// executable commands, which stay on the owning node.
type CapabilitySummary struct {
	Device        string              `json:"device"`
	ResourceClass string              `json:"resource_class"`
	SchedulerTier int                 `json:"scheduler_tier"`
	Chip          string              `json:"chip,omitempty"`
	NativeIDs     []string            `json:"native_ids,omitempty"`
	AgentCaps     map[string][]string `json:"agent_caps,omitempty"`
	ManualIDs     []string            `json:"manual_ids,omitempty"`
	Capacity      Capacity            `json:"capacity"`
}

// Register inserts (or upserts) this node's card into the local capability
// directory. In Phase 0 each node is its own directory; a remote employee
// table arrives in a later phase.
func Register(db *sql.DB, c Card, id string, tier int) error {
	native, err := json.Marshal(c.Native)
	if err != nil {
		return fmt.Errorf("marshal native: %w", err)
	}
	agents, err := json.Marshal(c.Agents)
	if err != nil {
		return fmt.Errorf("marshal agents: %w", err)
	}
	manual, err := json.Marshal(c.Manual)
	if err != nil {
		return fmt.Errorf("marshal manual: %w", err)
	}
	capJSON, err := json.Marshal(c.Capacity)
	if err != nil {
		return fmt.Errorf("marshal capacity: %w", err)
	}

	return upsertNode(db, id, c.Device, c.Chip, string(native), string(agents), string(manual), string(capJSON), tier)
}

// upsertNode writes one directory row — native/agents/manual/capacity already
// marshalled to JSON — and marks it online. Shared by Register (self, full
// card) and UpsertRemote (peer, ID-only summary) so the upsert SQL lives in one
// place.
func upsertNode(db *sql.DB, id, device, chip, nativeJSON, agentsJSON, manualJSON, capJSON string, tier int) error {
	_, err := db.Exec(`
		INSERT INTO employee_cache (id, name, department, chip, native_json, agents_json, manual_json, capacity_json, status, last_seen, scheduler_tier)
		VALUES (?, ?, '', ?, ?, ?, ?, ?, 'online', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, chip=excluded.chip,
			native_json=excluded.native_json, agents_json=excluded.agents_json,
			manual_json=excluded.manual_json, capacity_json=excluded.capacity_json,
			status='online', last_seen=excluded.last_seen, scheduler_tier=excluded.scheduler_tier`,
		id, device, chip, nativeJSON, agentsJSON, manualJSON, capJSON, storage.Now(), tier,
	)
	if err != nil {
		return fmt.Errorf("upsert %s: %w", id, err)
	}
	return nil
}

// Heartbeat updates status/capacity/last_seen for one node.
func Heartbeat(db *sql.DB, id, status string, capJSON string) error {
	if capJSON == "" {
		b, err := json.Marshal(Capacity{})
		if err != nil {
			return err
		}
		capJSON = string(b)
	}
	_, err := db.Exec(`UPDATE employee_cache SET status=?, capacity_json=?, last_seen=? WHERE id=?`,
		status, capJSON, storage.Now(), id)
	if err != nil {
		return fmt.Errorf("heartbeat %s: %w", id, err)
	}
	return nil
}

// MarkOffline flips a node to offline and stamps last_seen.
func MarkOffline(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE employee_cache SET status='offline', last_seen=? WHERE id=?`,
		storage.Now(), id)
	if err != nil {
		return fmt.Errorf("mark offline %s: %w", id, err)
	}
	return nil
}

// UpsertRemote writes a peer's capability summary into the local directory,
// marking it online. Remote nodes are stored with ID-only abilities (no
// executable commands) since this node never runs their commands directly —
// it forwards to them. Mirrors Register's ON CONFLICT upsert.
func UpsertRemote(db *sql.DB, id string, s CapabilitySummary) error {
	native := make([]NativeAbility, 0, len(s.NativeIDs))
	for _, nid := range s.NativeIDs {
		native = append(native, NativeAbility{ID: nid})
	}
	agents := make(map[string]Agent, len(s.AgentCaps))
	for name, caps := range s.AgentCaps {
		agents[name] = Agent{Capabilities: caps}
	}
	manual := make([]ManualAbility, 0, len(s.ManualIDs))
	for _, mid := range s.ManualIDs {
		manual = append(manual, ManualAbility{ID: mid})
	}

	nativeJSON, err := json.Marshal(native)
	if err != nil {
		return fmt.Errorf("marshal remote native: %w", err)
	}
	agentsJSON, err := json.Marshal(agents)
	if err != nil {
		return fmt.Errorf("marshal remote agents: %w", err)
	}
	manualJSON, err := json.Marshal(manual)
	if err != nil {
		return fmt.Errorf("marshal remote manual: %w", err)
	}
	capJSON, err := json.Marshal(s.Capacity)
	if err != nil {
		return fmt.Errorf("marshal remote capacity: %w", err)
	}

	return upsertNode(db, id, s.Device, s.Chip, string(nativeJSON), string(agentsJSON), string(manualJSON), string(capJSON), s.SchedulerTier)
}

// Node is a single employee_cache row, decoded.
type Node struct {
	ID            string
	Name          string
	Chip          string
	Status        string
	LastSeen      int64
	SchedulerTier int
	Native        []NativeAbility
	Agents        map[string]Agent
	Manual        []ManualAbility
	Capacity      Capacity
}

// Matches reports whether this node declares any of required, across the
// three ability layers (native / agent / manual). Mirrors commander.Router's
// per-kind matching so network-level and local routing agree on semantics.
//
// A required id of the form "agent:<name>" refers to a configured agent by
// name (the form advertised in the device summary); any other id matches a
// declared native/manual id or an agent capability, with a normalized fallback
// (see AbilityMatches).
func (n Node) Matches(required []string) bool {
	for _, req := range required {
		if name, ok := strings.CutPrefix(req, "agent:"); ok {
			if _, exists := n.Agents[name]; exists {
				return true
			}
			continue
		}
		for _, ab := range n.Native {
			if AbilityMatches(ab.ID, req) {
				return true
			}
		}
		for _, ag := range n.Agents {
			for _, cap := range ag.Capabilities {
				if AbilityMatches(cap, req) {
					return true
				}
			}
		}
		for _, ab := range n.Manual {
			if AbilityMatches(ab.ID, req) {
				return true
			}
		}
	}
	return false
}

// normalizeAbility folds an ability id to its lowercase alphanumeric form,
// dropping the ":" / "-" / "_" / "." separators the model uses inconsistently
// (e.g. "code:lint" vs "lint").
func normalizeAbility(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b = append(b, c)
		case c >= 'A' && c <= 'Z':
			b = append(b, c-'A'+'a')
		case c >= '0' && c <= '9':
			b = append(b, c)
		}
	}
	return string(b)
}

// AbilityMatches reports whether a declared ability id satisfies a required id.
// Exact equality wins; otherwise a normalized comparison (case- and
// separator-insensitive, with containment) bridges ids the model emitted with a
// different separator or a surrounding category prefix — e.g. required
// "code:lint" against a card id "lint". The fallback is guarded so a degenerate
// fragment (shorter side under 3 chars) never fans out to unrelated abilities.
func AbilityMatches(declared, required string) bool {
	if declared == required {
		return true
	}
	d, r := normalizeAbility(declared), normalizeAbility(required)
	if d == "" || r == "" {
		return false
	}
	if d == r {
		return true
	}
	short := d
	if len(r) < len(short) {
		short = r
	}
	if len(short) < 3 {
		return false
	}
	return strings.Contains(d, r) || strings.Contains(r, d)
}

// Query returns nodes matching filters. Empty status or name matches all.
func Query(db *sql.DB, status, name string) ([]Node, error) {
	q := `SELECT id, name, chip, status, last_seen, scheduler_tier, native_json, agents_json, manual_json, capacity_json
	      FROM employee_cache WHERE 1=1`
	var args []any
	if status != "" {
		q += " AND status = ?"
		args = append(args, status)
	}
	if name != "" {
		q += " AND name = ?"
		args = append(args, name)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query employees: %w", err)
	}
	defer rows.Close()

	var out []Node
	for rows.Next() {
		var n Node
		var native, agents, manual, capJSON string
		if err := rows.Scan(&n.ID, &n.Name, &n.Chip, &n.Status, &n.LastSeen, &n.SchedulerTier,
			&native, &agents, &manual, &capJSON); err != nil {
			return nil, err
		}
		if native != "" {
			_ = json.Unmarshal([]byte(native), &n.Native)
		}
		if agents != "" {
			_ = json.Unmarshal([]byte(agents), &n.Agents)
		}
		if manual != "" {
			_ = json.Unmarshal([]byte(manual), &n.Manual)
		}
		if capJSON != "" {
			_ = json.Unmarshal([]byte(capJSON), &n.Capacity)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
