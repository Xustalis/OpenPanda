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
	CPUCores      int `yaml:"cpu_cores"`
	RAMGB         int `yaml:"ram_gb"`
	MaxConcurrent int `yaml:"max_concurrent_tasks"`
	CurrentTasks  int `yaml:"current_tasks"`
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

	_, err = db.Exec(`
		INSERT INTO employee_cache (id, name, department, chip, native_json, agents_json, manual_json, capacity_json, status, last_seen, scheduler_tier)
		VALUES (?, ?, '', ?, ?, ?, ?, ?, 'online', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, chip=excluded.chip,
			native_json=excluded.native_json, agents_json=excluded.agents_json,
			manual_json=excluded.manual_json, capacity_json=excluded.capacity_json,
			status='online', last_seen=excluded.last_seen, scheduler_tier=excluded.scheduler_tier`,
		id, c.Device, c.Chip, string(native), string(agents), string(manual),
		string(capJSON), storage.Now(), tier,
	)
	if err != nil {
		return fmt.Errorf("register %s: %w", id, err)
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

// Node is a single employee_cache row, decoded.
type Node struct {
	ID       string
	Name     string
	Chip     string
	Status   string
	LastSeen int64
	Native   []NativeAbility
	Agents   map[string]Agent
	Manual   []ManualAbility
	Capacity Capacity
}

// Query returns nodes matching filters. Empty status or name matches all.
func Query(db *sql.DB, status, name string) ([]Node, error) {
	q := `SELECT id, name, chip, status, last_seen, native_json, agents_json, manual_json, capacity_json
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
		if err := rows.Scan(&n.ID, &n.Name, &n.Chip, &n.Status, &n.LastSeen,
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
