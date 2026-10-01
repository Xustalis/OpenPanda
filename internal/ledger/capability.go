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
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

// Card is the parsed form of capabilities.yaml for this node.
type Card struct {
	Device          string            `yaml:"device" json:"device"`
	ResourceClass   string            `yaml:"resource_class" json:"resource_class"`
	NodeKind        string            `yaml:"node_kind,omitempty" json:"node_kind,omitempty"`
	NodeIdentity    string            `yaml:"node_identity,omitempty" json:"node_identity,omitempty"`
	Chip            string            `yaml:"chip" json:"chip"`
	Native          []NativeAbility   `yaml:"native" json:"native"`
	Agents          map[string]Agent  `yaml:"agents" json:"agents"`
	Manual          []ManualAbility   `yaml:"manual" json:"manual"`
	Actuators       []ActuatorProfile `yaml:"actuators,omitempty" json:"actuators,omitempty"`
	Capacity        Capacity          `yaml:"capacity" json:"capacity"`
	ResourceProfile ResourceProfile   `yaml:"resource_profile" json:"resource_profile"`
}

// ActuatorProfile is the unified model equalizing software cognitive agents and hardware peripherals (whitepaper §7.1).
type ActuatorProfile struct {
	ID           string   `yaml:"id" json:"id"`
	Type         string   `yaml:"type" json:"type"`                               // "software" or "hardware"
	Category     string   `yaml:"category" json:"category"`                       // "coding", "motor_control", "audio_sensing", etc.
	Interface    string   `yaml:"interface,omitempty" json:"interface,omitempty"` // "gpio", "usb_audio", "cli", etc.
	PinMapping   []int    `yaml:"pin_mapping,omitempty" json:"pin_mapping,omitempty"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	CostTier     string   `yaml:"cost_tier,omitempty" json:"cost_tier,omitempty"`
	Tier         int      `yaml:"tier" json:"tier"` // 1=reversible (default), 2=irreversible (needs auth)
	// Command/Args are the actuator's driver invocation (§7.2): the program
	// that turns an intent or ActionSpec into a physical/software action —
	// e.g. a GPIO control script on an Orange Pi. Placeholders {intent},
	// {action} and {param:<name>} are substituted at execution. An actuator
	// without a command stays a routing advertisement only: tasks matching it
	// fall through to the agent tier, which figures the hardware out itself.
	Command string   `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
}

// ActionSpec defines a uniform action dispatch across both software harnesses and hardware actuators (whitepaper §7.2).
type ActionSpec struct {
	TargetActuator string         `json:"target_actuator"`
	Action         string         `json:"action"`
	Parameters     map[string]any `json:"parameters,omitempty"`
}

// RequiresForActionSpec returns requires with the spec's target actuator
// leading it. Commander.MatchActuator resolves the FIRST required token that
// matches any card actuator, so a vague token ("servo") ahead of the exact id
// can resolve a different actuator than the spec named — the substitution
// gate then refuses the mismatch and the task churns through declines and
// re-routes. Leading with the exact id makes the spec's stated target win;
// a duplicate exact id is folded rather than doubled.
func RequiresForActionSpec(requires []string, spec *ActionSpec) []string {
	if spec == nil || spec.TargetActuator == "" {
		return requires
	}
	out := make([]string, 0, len(requires)+1)
	out = append(out, spec.TargetActuator)
	for _, r := range requires {
		if r != spec.TargetActuator {
			out = append(out, r)
		}
	}
	return out
}

// NativeAbility is a deterministic command this node can run.
type NativeAbility struct {
	ID          string   `yaml:"id" json:"id"`
	Command     string   `yaml:"command" json:"command"`
	Args        []string `yaml:"args" json:"args"`
	Tier        int      `yaml:"tier" json:"tier"` // 1=reversible (default) 2=irreversible (needs auth)
	Description string   `yaml:"description" json:"description"`
}

// Agent is an installed agent CLI + its capabilities.
type Agent struct {
	Adapter      string `yaml:"adapter" json:"adapter"`
	InstallCheck string `yaml:"install_check" json:"install_check"`
	// Command is the argv template the generic adapter (generic.py) expands:
	// shlex-split, with "{prompt}" replaced by the task prompt as one literal
	// argv element (appended when no placeholder is present), plus optional
	// {stdin} (pipe the prompt to the child's stdin), {cwd}, {resume} and
	// {max_turns} placeholders (see the generic.py docstring). It lets a card
	// wire ANY headless CLI — `command: "zcode --prompt {prompt}"` — without
	// a bespoke adapter script. Ignored by adapters that carry their own
	// command line. Never crosses the wire (CapabilitySummary carries only
	// capability tags), so it stays a local declaration like NativeAbility.
	Command      string   `yaml:"command,omitempty" json:"command,omitempty"`
	Capabilities []string `yaml:"capabilities" json:"capabilities"`
	BestAt       []string `yaml:"best_at" json:"best_at"`
	NotFor       []string `yaml:"not_for" json:"not_for"`
	CostTier     string   `yaml:"cost_tier" json:"cost_tier"`
	// Tier mirrors NativeAbility.Tier: 1=reversible, 2=irreversible (needs
	// auth). Zero defaults to 2 — an agent CLI can run arbitrary shell through
	// the model, so the safe default is to require consent unless the card
	// explicitly declares the agent read-only (P1-15).
	Tier int `yaml:"tier" json:"tier"`
}

// ManualAbility is a human-performed task.
type ManualAbility struct {
	ID     string `yaml:"id" json:"id"`
	Notify string `yaml:"notify" json:"notify"`
}

// Capacity describes current resource availability.
type Capacity struct {
	CPUCores      int `yaml:"cpu_cores" json:"cpu_cores"`
	RAMGB         int `yaml:"ram_gb" json:"ram_gb"`
	MaxConcurrent int `yaml:"max_concurrent_tasks" json:"max_concurrent_tasks"`
	CurrentTasks  int `yaml:"current_tasks" json:"current_tasks"`
}

// ResourceProfile is a node-side, manually declared resource hint (design §13.2;
// Sprint 5.1 consumes it for weighted scoring). It mirrors entry.ResourceProfile
// in shape so the task and node sides compare field-for-field, but is declared in
// ledger to avoid an entry→ledger import cycle. Static for the life of a card.
type ResourceProfile struct {
	CPU          int    `yaml:"cpu" json:"cpu"`
	RAMGB        int    `yaml:"ram_gb" json:"ram_gb"`
	GPUVRAMGB    int    `yaml:"gpu_vram_gb" json:"gpu_vram_gb"`
	DurationHint string `yaml:"duration_hint" json:"duration_hint"` // short | long
}

// GPUVRAMUnknown marks a node whose GPU exists but whose size could not be read
// — no nvidia-smi, a driver that reports nothing, a card behind a hypervisor.
//
// It has to be distinguishable from 0. Fits treats a declared VRAM figure as a
// hard filter, so if an unreadable card wrote 0 the machine that owns the GPU
// would be excluded from exactly the tasks it exists to run, while an Orange Pi
// that honestly declares 0 sits there holding the training stage. Unknown is
// therefore permissive (the node stays a candidate and finds out at run time),
// and a real 0 keeps excluding. hwinfo.GPUVRAMGB is what produces the value.
const GPUVRAMUnknown = -1

// Declared reports whether this profile says anything at all about hardware. An
// all-zero profile is the shape of a card that never wrote a resource_profile
// block, and that is silence, not a claim of zero capacity — every card shipped
// before v0.0.6 looks like this. Treating silence as "no VRAM" would decline
// every GPU task in the network, so Fits passes an undeclared node through and
// the requirement is enforced only where it can be checked.
func (r ResourceProfile) Declared() bool {
	return r.CPU > 0 || r.RAMGB > 0 || r.GPUVRAMGB > 0 || r.DurationHint != ""
}

// CapabilitySummary is the compact capability profile a node advertises in its
// hello handshake (design doc §2.1 capability exchange). It carries only what
// routing needs — ability IDs, scheduler tier, current capacity and the declared
// hardware profile — not the executable commands, which stay on the owning node.
type CapabilitySummary struct {
	Device        string              `json:"device"`
	ResourceClass string              `json:"resource_class"`
	NodeKind      string              `json:"node_kind,omitempty"`
	NodeIdentity  string              `json:"node_identity,omitempty"`
	SchedulerTier int                 `json:"scheduler_tier"`
	Chip          string              `json:"chip,omitempty"`
	NativeIDs     []string            `json:"native_ids,omitempty"`
	AgentCaps     map[string][]string `json:"agent_caps,omitempty"`
	ManualIDs     []string            `json:"manual_ids,omitempty"`
	// ActuatorIDs advertises the unified actuator set (§7.1) so remote routing
	// can see this node's hardware "hands" — a GPIO servo on a Pi is a routing
	// target exactly like an agent capability, and invisible if unadvertised.
	ActuatorIDs []string `json:"actuator_ids,omitempty"`
	// Neighbors is the link-state advertisement: the peer node ids this node
	// currently holds a live connection to. The mesh builds its routing graph
	// G=(V,E) from these edges, which is what makes multi-hop shortest-path
	// forwarding (§9.3) computable off the directory.
	Neighbors []string `json:"neighbors,omitempty"`
	// Links carries the measured edge metrics for the same adjacency (§4.1):
	// one entry per peer this node has an RTT sample for. Weighted shortest
	// path reads these as edge costs; an advertised neighbor with no metric
	// gets the unknown-link default.
	Links    []LinkMetric `json:"links,omitempty"`
	Capacity Capacity     `json:"capacity"`
	// Contacts is the node's advertised contact plan (§8.4): scheduled
	// transmission windows toward peers that custody routing evaluates
	// alongside live adjacency. A node with no scheduled links advertises
	// nothing, and its row reads identically to a pre-contacts peer.
	Contacts []Contact `json:"contacts,omitempty"`
	// ResourceProfile is what makes "this node cannot run that" decidable off the
	// network instead of only locally: a task declaring 8 GiB of VRAM must not be
	// forwarded to a node with none, and before v0.0.6 the peer half of this
	// field was simply dropped, so every peer looked equally capable.
	ResourceProfile ResourceProfile `json:"resource_profile,omitempty"`
	// Projects lists the project names this node holds a checkout of, so a
	// project-bound task can be routed where its tree already lives instead of
	// packing it across the wire (§6.3 residence term). Membership changes as
	// projects land, so the regular heartbeat carries the same list.
	Projects []string `json:"projects,omitempty"`
}

// LinkMetric is one measured edge of the link-state graph (§4.1): the
// advertised cost of reaching Peer over this node's live connection. RTTms
// is milliseconds; zero means the link exists but has no sample yet.
type LinkMetric struct {
	Peer  string `json:"peer"`
	RTTms int64  `json:"rtt_ms,omitempty"`
}

// Register inserts (or upserts) this node's card into the local capability
// directory. In Phase 0 each node is its own directory; a remote employee
// table arrives in a later phase.
func Register(db *sql.DB, c Card, id string, tier int) error {
	nativeList := append([]NativeAbility{}, c.Native...)
	for _, act := range c.Actuators {
		// An actuator's driver command (when declared) is the executable its
		// folded native ability carries — falling back to the interface name
		// keeps the row honest about there being nothing to run rather than
		// recording a literal "gpio" as if it were a binary.
		cmd := act.Command
		if cmd == "" {
			cmd = act.Interface
		}
		nativeList = append(nativeList, NativeAbility{
			ID:          act.ID,
			Command:     cmd,
			Args:        act.Args,
			Tier:        act.Tier,
			Description: act.Category,
		})
	}
	native, err := json.Marshal(nativeList)
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
	resJSON, err := json.Marshal(c.ResourceProfile)
	if err != nil {
		return fmt.Errorf("marshal resource profile: %w", err)
	}

	kind, identity := c.NodeKind, c.NodeIdentity
	if kind == "" {
		kind = "physical"
	}
	if identity == "" {
		identity = id
	}
	return upsertNode(db, id, c.Device, c.Chip, kind, identity, string(native), string(agents), string(manual), string(capJSON), string(resJSON), "", "", "", "", tier)
}

// upsertNode writes one directory row — native/agents/manual/capacity/resource
// profile/neighbors/link metrics/contacts already marshalled to JSON — and
// marks it online. Shared by Register (self, full card) and UpsertRemote
// (peer, ID-only summary) so the upsert SQL lives in one place.
func upsertNode(db *sql.DB, id, device, chip, kind, identity, nativeJSON, agentsJSON, manualJSON, capJSON, resJSON, neighborsJSON, linksJSON, contactsJSON, projectsJSON string, tier int) error {
	_, err := db.Exec(`
		INSERT INTO employee_cache (id, name, department, chip, node_kind, node_identity, native_json, agents_json, manual_json, capacity_json, resource_profile_json, neighbors_json, links_json, contacts_json, projects_json, status, last_seen, scheduler_tier)
		VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'online', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, chip=excluded.chip,
			node_kind=excluded.node_kind, node_identity=excluded.node_identity,
			native_json=excluded.native_json, agents_json=excluded.agents_json,
			manual_json=excluded.manual_json, capacity_json=excluded.capacity_json,
			resource_profile_json=excluded.resource_profile_json,
			neighbors_json=excluded.neighbors_json, links_json=excluded.links_json,
			contacts_json=excluded.contacts_json,
			-- A silent upsert (self Register, an old peer's card) carries no
			-- residence claim: preserve what the heartbeat gossip published
			-- rather than blanking it until the next beat.
			projects_json=CASE WHEN excluded.projects_json='' THEN employee_cache.projects_json
				ELSE excluded.projects_json END,
			status='online', last_seen=excluded.last_seen, scheduler_tier=excluded.scheduler_tier`,
		id, device, chip, kind, identity, nativeJSON, agentsJSON, manualJSON, capJSON, resJSON, neighborsJSON, linksJSON, contactsJSON, projectsJSON, storage.Now(), tier,
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

// HeartbeatIfChanged is Heartbeat behind a read-compare: the row is
// rewritten only when status or the capacity payload differs, or when
// last_seen is older than minRefreshSec — the freshness floor that keeps the
// 90s stale-peer sweep and the panel's 45s liveness check honest. A steady
// peer's 15s beat otherwise appends a WAL page on every arrival for a row
// that already says exactly this.
func HeartbeatIfChanged(db *sql.DB, id, status, capJSON string, minRefreshSec int64) error {
	// Normalize before comparing: Heartbeat stores "{}" for an empty payload,
	// so comparing raw "" against the stored "{}" would rewrite every beat
	// and the gate would never engage.
	if capJSON == "" {
		b, err := json.Marshal(Capacity{})
		if err != nil {
			return err
		}
		capJSON = string(b)
	}
	// One statement, not read-compare-write: the WHERE clause gates the
	// update on "something changed or the row went stale", so a steady beat
	// costs a single no-op UPDATE round-trip instead of a SELECT plus a
	// conditional write — and the change check is atomic with the write,
	// not two racing round-trips. RowsAffected tells the truth either way
	// (0 = unchanged/stale-gated, 1 = rewritten); callers don't care, so it
	// is deliberately discarded.
	now := storage.Now()
	_, err := db.Exec(
		`UPDATE employee_cache SET status=?, capacity_json=?, last_seen=?
		 WHERE id=? AND (
		   COALESCE(status,'') IS NOT ? OR
		   COALESCE(capacity_json,'') IS NOT ? OR
		   COALESCE(last_seen,0) <= ?)`,
		status, capJSON, now, id, status, capJSON, now-minRefreshSec)
	if err != nil {
		return fmt.Errorf("heartbeat %s: %w", id, err)
	}
	return nil
}

// UpdateAdjacency refreshes a node's advertised edge set, measured link
// weights (§4.1) and contact plan (§8.4) without touching the rest of its
// row — the gossip channel heartbeats drive between full card updates.
// Empty inputs leave that column alone, so a sender that only publishes one
// side does not blank the others.
func UpdateAdjacency(db *sql.DB, id, neighborsJSON, linksJSON, contactsJSON, projectsJSON string) error {
	if neighborsJSON != "" {
		if _, err := db.Exec(`UPDATE employee_cache SET neighbors_json=? WHERE id=?`, neighborsJSON, id); err != nil {
			return fmt.Errorf("update neighbors %s: %w", id, err)
		}
	}
	if linksJSON != "" {
		if _, err := db.Exec(`UPDATE employee_cache SET links_json=? WHERE id=?`, linksJSON, id); err != nil {
			return fmt.Errorf("update links %s: %w", id, err)
		}
	}
	if contactsJSON != "" {
		if _, err := db.Exec(`UPDATE employee_cache SET contacts_json=? WHERE id=?`, contactsJSON, id); err != nil {
			return fmt.Errorf("update contacts %s: %w", id, err)
		}
	}
	if projectsJSON != "" {
		if _, err := db.Exec(`UPDATE employee_cache SET projects_json=? WHERE id=?`, projectsJSON, id); err != nil {
			return fmt.Errorf("update projects %s: %w", id, err)
		}
	}
	return nil
}

// UpdateAdjacencyIfChanged runs UpdateAdjacency only when one of the supplied
// columns would actually change. Timer-driven publishers (heartbeat
// residence, the 5s self-neighbor refresh) otherwise append a WAL page every
// tick for a row that already says exactly this. A missing or unreadable row
// falls through to the write — that is precisely when it matters.
func UpdateAdjacencyIfChanged(db *sql.DB, id, neighborsJSON, linksJSON, contactsJSON, projectsJSON string) error {
	// One statement: the CASEs keep the stored column for empty inputs (same
	// "absent means leave alone" contract UpdateAdjacency has), and the WHERE
	// clause asks SQLite whether any supplied value actually differs — a
	// steady tick is a single no-op UPDATE round-trip, atomic with the
	// compare, replacing the old SELECT-then-maybe-four-UPDATEs shape.
	_, err := db.Exec(
		`UPDATE employee_cache SET
		   neighbors_json = CASE WHEN ?1 != '' THEN ?1 ELSE neighbors_json END,
		   links_json     = CASE WHEN ?2 != '' THEN ?2 ELSE links_json END,
		   contacts_json  = CASE WHEN ?3 != '' THEN ?3 ELSE contacts_json END,
		   projects_json  = CASE WHEN ?4 != '' THEN ?4 ELSE projects_json END
		 WHERE id = ?5 AND (
		   (?1 != '' AND COALESCE(neighbors_json,'') IS NOT ?1) OR
		   (?2 != '' AND COALESCE(links_json,'') IS NOT ?2) OR
		   (?3 != '' AND COALESCE(contacts_json,'') IS NOT ?3) OR
		   (?4 != '' AND COALESCE(projects_json,'') IS NOT ?4))`,
		neighborsJSON, linksJSON, contactsJSON, projectsJSON, id)
	if err != nil {
		return fmt.Errorf("update adjacency %s: %w", id, err)
	}
	return nil
}

// NodeStamp is the minimal per-node triple a change-detection digest needs —
// the full Query projection carries every JSON payload the card holds.
type NodeStamp struct {
	ID       string
	Status   string
	LastSeen int64
}

// NodeStamps lists the digest input for every known node, ordered by id so a
// digest built from it is order-stable.
func NodeStamps(db *sql.DB) ([]NodeStamp, error) {
	rows, err := db.Query(`SELECT id, status, COALESCE(last_seen,0) FROM employee_cache ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query node stamps: %w", err)
	}
	defer rows.Close()
	var out []NodeStamp
	for rows.Next() {
		var n NodeStamp
		if err := rows.Scan(&n.ID, &n.Status, &n.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
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

// MarkOnline flips a node back to online without touching last_seen — the
// symmetric of MarkOffline for rows the stale sweep flipped by mistake (a
// self row caught between beats); the node's own next heartbeat restores
// capacity and freshness.
func MarkOnline(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE employee_cache SET status='online' WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("mark online %s: %w", id, err)
	}
	return nil
}

// ExpireStale marks nodes offline whose status still claims online but whose
// last heartbeat is older than maxAgeSec seconds. IDs in exclude are left
// alone (peers the caller still holds a live connection to). Rows that never
// heartbeated (last_seen = 0, hand-built fixtures) are left untouched — the
// sweep acts on silence, not on the absence of a clock. Returns the ids
// actually flipped.
func ExpireStale(db *sql.DB, maxAgeSec int64, exclude []string) ([]string, error) {
	cutoff := storage.Now() - maxAgeSec
	rows, err := db.Query(
		`SELECT id FROM employee_cache WHERE status='online' AND last_seen > 0 AND last_seen < ?`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("scan stale nodes: %w", err)
	}
	skip := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		skip[id] = true
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if !skip[id] {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var flipped []string
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE employee_cache SET status='offline' WHERE id=? AND status='online'`, id); err != nil {
			return flipped, fmt.Errorf("expire stale %s: %w", id, err)
		}
		flipped = append(flipped, id)
	}
	return flipped, nil
}

// Remove deletes a node's directory row. A removed remote that is still
// alive re-appears on its next hello, so removal is for stale rows: a
// renamed machine, a peer whose identity changed, a decommissioned node.
// The self node is the callers' business to refuse (this layer has no
// notion of "local"), not this function's.
func Remove(db *sql.DB, id string) (int64, error) {
	res, err := db.Exec(`DELETE FROM employee_cache WHERE id=?`, id)
	if err != nil {
		return 0, fmt.Errorf("remove node %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("remove node %s: rows: %w", id, err)
	}
	return n, nil
}

// MarkVerified stamps the node's CURRENT advertised key as human-confirmed —
// the `panda nodes verify` write. It refuses (false) when there is no key on
// record: there is nothing to have compared, and a verify that attaches to a
// later first-seen key would bless a key nobody looked at.
func MarkVerified(db *sql.DB, id string) (bool, error) {
	res, err := db.Exec(`UPDATE employee_cache SET key_verified=? WHERE id=? AND pub_key != ''`,
		time.Now().Unix(), id)
	if err != nil {
		return false, fmt.Errorf("verify node %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("verify node %s: rows: %w", id, err)
	}
	return n > 0, nil
}

// PendingNode is one row of the LAN discovery hint list: an unpaired node
// that broadcast its address recently. Everything in it is self-asserted —
// the row is a lead for `panda nodes add`, never proof of identity. Proof
// still comes from the paired hello (shared secret + Ed25519) after the
// operator admits the address.
type PendingNode struct {
	ID        string `json:"id"`
	Addr      string `json:"addr"`
	PubKey    string `json:"pub_key,omitempty"`
	Ver       string `json:"ver,omitempty"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
}

// Fingerprint renders the advertised key's comparable prefix — the same
// 16-hex form the fleet list uses, so an operator can compare this row
// against the other machine's own `panda nodes` line before admitting it.
func (p PendingNode) Fingerprint() string {
	if len(p.PubKey) < 16 {
		return p.PubKey
	}
	return p.PubKey[:16]
}

// UpsertPending records a discovery beacon. first_seen survives across
// beacons so the listing can tell a just-appeared node from a long-announced
// one; a node already in the directory is skipped by the caller (it is
// joined, not pending).
func UpsertPending(db *sql.DB, p PendingNode) error {
	_, err := db.Exec(`INSERT INTO pending_nodes (id, addr, pub_key, ver, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET addr=excluded.addr, pub_key=excluded.pub_key,
			ver=excluded.ver, last_seen=excluded.last_seen`,
		p.ID, p.Addr, p.PubKey, p.Ver, p.FirstSeen, p.LastSeen)
	if err != nil {
		return fmt.Errorf("upsert pending %s: %w", p.ID, err)
	}
	return nil
}

// ListPending returns pending nodes freshest-first, first sweeping rows whose
// last beacon is older than maxAge — the read-side expiry keeps the list
// honest even when the daemon has stopped sweeping.
func ListPending(db *sql.DB, maxAge time.Duration) ([]PendingNode, error) {
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge).Unix()
		if _, err := db.Exec(`DELETE FROM pending_nodes WHERE last_seen < ?`, cutoff); err != nil {
			return nil, fmt.Errorf("sweep pending: %w", err)
		}
	}
	rows, err := db.Query(`SELECT id, addr, pub_key, ver, first_seen, last_seen FROM pending_nodes ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PendingNode, 0, 4)
	for rows.Next() {
		var p PendingNode
		if err := rows.Scan(&p.ID, &p.Addr, &p.PubKey, &p.Ver, &p.FirstSeen, &p.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ForgetPending drops a pending row — after `nodes admit` converts it to a
// configured peer, or when the operator wants a broadcaster gone from view.
func ForgetPending(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM pending_nodes WHERE id=?`, id)
	return err
}

// UpsertRemote writes a peer's capability summary into the local directory,
// marking it online. Remote nodes are stored with ID-only abilities (no
// executable commands) since this node never runs their commands directly —
// it forwards to them. Mirrors Register's ON CONFLICT upsert.
func UpsertRemote(db *sql.DB, id string, s CapabilitySummary) error {
	native := make([]NativeAbility, 0, len(s.NativeIDs)+len(s.ActuatorIDs))
	for _, nid := range s.NativeIDs {
		native = append(native, NativeAbility{ID: nid})
	}
	// Remote actuators fold into the remote native set: from this side they
	// are abilities the peer owns, which is exactly what Matches consults.
	for _, aid := range s.ActuatorIDs {
		native = append(native, NativeAbility{ID: aid})
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
	resJSON, err := json.Marshal(s.ResourceProfile)
	if err != nil {
		return fmt.Errorf("marshal remote resource profile: %w", err)
	}
	neighborsJSON, err := json.Marshal(s.Neighbors)
	if err != nil {
		return fmt.Errorf("marshal remote neighbors: %w", err)
	}
	linksJSON, err := json.Marshal(s.Links)
	if err != nil {
		return fmt.Errorf("marshal remote link metrics: %w", err)
	}
	contactsJSON, err := json.Marshal(s.Contacts)
	if err != nil {
		return fmt.Errorf("marshal remote contacts: %w", err)
	}
	// s.Projects stays nil on a peer that predates the field: marshalling the
	// nil slice still lands as "null" in the column, which decodes to the same
	// absence — the row simply reads as "unknown residence" until a heartbeat
	// carrying the list arrives.
	projectsJSON, err := json.Marshal(s.Projects)
	if err != nil {
		return fmt.Errorf("marshal remote projects: %w", err)
	}

	kind, identity := s.NodeKind, s.NodeIdentity
	if kind == "" {
		kind = "physical"
	}
	if identity == "" {
		identity = id
	}
	return upsertNode(db, id, s.Device, s.Chip, kind, identity, string(nativeJSON), string(agentsJSON), string(manualJSON), string(capJSON), string(resJSON), string(neighborsJSON), string(linksJSON), string(contactsJSON), string(projectsJSON), s.SchedulerTier)
}

// Node is a single employee_cache row, decoded.
type Node struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Chip            string            `json:"chip,omitempty"`
	NodeKind        string            `json:"node_kind"`
	NodeIdentity    string            `json:"node_identity,omitempty"`
	Status          string            `json:"status"`
	LastSeen        int64             `json:"last_seen"`
	SchedulerTier   int               `json:"scheduler_tier"`
	Native          []NativeAbility   `json:"native,omitempty"`
	Agents          map[string]Agent  `json:"agents,omitempty"`
	Manual          []ManualAbility   `json:"manual,omitempty"`
	Actuators       []ActuatorProfile `json:"actuators,omitempty"`
	Capacity        Capacity          `json:"capacity"`
	ResourceProfile ResourceProfile   `json:"resource_profile"`
	// Neighbors is this node's link-state advertisement — the peers it holds
	// live connections to — learned from its capability summary and stored in
	// neighbors_json. It is the edge set E of the mesh routing graph.
	Neighbors []string `json:"neighbors,omitempty"`
	// LinkMetrics is the edge weight table for the same adjacency (§4.1):
	// peer id → RTT in milliseconds, decoded from links_json. A neighbor
	// with no entry costs the unknown-link default in weighted routing.
	LinkMetrics map[string]int64 `json:"link_metrics,omitempty"`
	// Contacts is the advertised contact plan, decoded from contacts_json:
	// the scheduled windows this node can transmit on. Contact-graph routing
	// (scheduler.ContactNextHop) treats them as edges that open at a known
	// time, alongside the always-on live adjacency above.
	Contacts []Contact `json:"contacts,omitempty"`
	// Projects is the advertised residence set (§6.3), decoded from
	// projects_json: the names of the projects this node holds a checkout of.
	// A project-bound task scores a resident node higher — the tree does not
	// have to cross the wire when the work lands where the code lives.
	Projects []string `json:"projects,omitempty"`
	// PubKey is the node's advertised Ed25519 key (hex), learned from signed
	// hellos — the bytes the fingerprint is rendered from and every signed
	// grant verifies against.
	PubKey string `json:"pub_key,omitempty"`
	// KeyVerified is the unix timestamp a human confirmed the fingerprint
	// (panda nodes verify). Zero means TOFU-recorded but never compared — a
	// state the fleet list shows rather than silently treats as trusted.
	KeyVerified int64 `json:"key_verified,omitempty"`
}

// Fingerprint renders the comparable form of PubKey — enough hex digits that
// two machines' listings can be eyeballed side by side, short enough to say
// out loud. Empty when the node predates signed hellos.
func (n Node) Fingerprint() string {
	if len(n.PubKey) < 16 {
		return n.PubKey
	}
	return n.PubKey[:16]
}

// Verified reports whether the current key has been human-confirmed. A node
// with no key at all is not "verified" — there is nothing to have checked.
func (n Node) Verified() bool {
	return n.PubKey != "" && n.KeyVerified > 0
}

// Abilities returns this node's displayable ability list — native IDs,
// actuator IDs, plus an "agent:<name>" entry per configured agent — sorted
// for deterministic output.
func (n Node) Abilities() []string {
	out := make([]string, 0, len(n.Native)+len(n.Agents)+len(n.Actuators))
	for _, a := range n.Native {
		out = append(out, a.ID)
	}
	for name := range n.Agents {
		out = append(out, "agent:"+name)
	}
	for _, act := range n.Actuators {
		out = append(out, act.ID)
	}
	sort.Strings(out)
	return out
}

// Matches reports whether this node declares any of required, across the
// four ability layers (native / agent / manual / actuators).
func (n Node) Matches(required []string) bool {
	// Pre-tokenize the declared ids once; otherwise each required id would
	// re-tokenize the whole declared set (O(R×A) allocations instead of O(A)).
	native, agentCaps, manual, actuators := n.tokenizedAbilities()
	for _, req := range required {
		if name, ok := strings.CutPrefix(req, "agent:"); ok {
			if _, exists := n.Agents[name]; exists {
				return true
			}
			continue
		}
		r := tokenizeAbility(req)
		if matchTokens(native, r) || matchTokens(agentCaps, r) || matchTokens(manual, r) || matchTokens(actuators, r) {
			return true
		}
	}
	return false
}

// Fits reports whether this node's declared hardware satisfies a task's declared
// requirement. It is the compute half of routing, where Matches is the ability
// half: the Orange Pi genuinely has the coding ability and genuinely cannot train
// a model, and only this comparison can tell those apart.
//
// Both sides are permissive when silent. A requirement of zero asks for nothing
// and every node fits it; a node that declares no profile at all is unknown
// rather than empty (see ResourceProfile.Declared) and is allowed through, since
// the alternative is that a network of pre-v0.0.6 cards can route nothing. Only
// a node that positively declares its hardware can be positively excluded — and
// GPUVRAMUnknown is not a positive declaration, it is "there is a card here and
// nothing would tell me how big it is".
func (n Node) Fits(req ResourceProfile) bool {
	if !req.Declared() || !n.ResourceProfile.Declared() {
		return true
	}
	if req.GPUVRAMGB > 0 && n.ResourceProfile.GPUVRAMGB >= 0 &&
		n.ResourceProfile.GPUVRAMGB < req.GPUVRAMGB {
		return false
	}
	if req.RAMGB > 0 && n.ResourceProfile.RAMGB > 0 && n.ResourceProfile.RAMGB < req.RAMGB {
		return false
	}
	if req.CPU > 0 && n.ResourceProfile.CPU > 0 && n.ResourceProfile.CPU < req.CPU {
		return false
	}
	return true
}

// tokenizedAbilities returns the node's native, agent and manual abilities as
// case-folded token sets, computed once so Matches does not re-tokenize them
// per required id.
func (n Node) tokenizedAbilities() (native, agentCaps, manual, actuators [][]string) {
	native = make([][]string, 0, len(n.Native))
	for _, ab := range n.Native {
		native = append(native, tokenizeAbility(ab.ID))
	}
	for _, ag := range n.Agents {
		for _, cap := range ag.Capabilities {
			agentCaps = append(agentCaps, tokenizeAbility(cap))
		}
	}
	manual = make([][]string, 0, len(n.Manual))
	for _, ab := range n.Manual {
		manual = append(manual, tokenizeAbility(ab.ID))
	}
	actuators = make([][]string, 0, len(n.Actuators))
	for _, act := range n.Actuators {
		actuators = append(actuators, tokenizeAbility(act.ID))
		for _, cap := range act.Capabilities {
			actuators = append(actuators, tokenizeAbility(cap))
		}
	}
	return native, agentCaps, manual, actuators
}

// matchTokens reports whether any declared token set matches the required set.
func matchTokens(ids [][]string, required []string) bool {
	for _, d := range ids {
		if tokenSubset(d, required) {
			return true
		}
	}
	return false
}

// tokenizeAbility splits an ability id into case-folded alphanumeric tokens on
// the separators the model uses inconsistently (":", "-", "_", ".", and any
// other non-alphanumeric). "code:lint" → ["code","lint"], "glint" → ["glint"].
func tokenizeAbility(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.ToLower(f))
	}
	return out
}

// AbilityMatches reports whether a declared ability id satisfies a required id.
// Exact equality wins; otherwise a token-subset match bridges ids where one is a
// category-prefixed form of the other — e.g. required "code:lint" against a card
// id "lint". Tokens are compared whole, so a required "lint" never matches an
// unrelated "glint", and "build" never matches "rebuild".
//
// The subset check is deliberately SYMMETRIC and fuzzy: the declared side is
// the card's claim and the required side is the model's phrasing, and neither
// is authoritative about granularity. That trades precision for reach — a node
// declaring the broad ability "build" is treated as able to serve a task
// requiring the narrower "build:macos" it may not actually have. The safety
// margin is structural rather than in this predicate: routing prefers exact-id
// and richer matches upstream in the score, the executor fails loudly on a
// capability it lacks, and the supervision layer judges the result. Tightening
// this to a directional (required ⊆ declared) match would strand every card
// written at the broad granularity, so the fuzz is kept and documented.
func AbilityMatches(declared, required string) bool {
	if declared == required {
		return true
	}
	return tokenSubset(tokenizeAbility(declared), tokenizeAbility(required))
}

// tokenSubset reports whether the tokens of one id are all present in the other.
// The shorter token list is treated as the subset; an empty list never matches,
// so a blank or separator-only id cannot fan out to unrelated abilities.
func tokenSubset(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	sub, sup := a, b
	if len(b) < len(a) {
		sub, sup = b, a
	}
	set := make(map[string]struct{}, len(sup))
	for _, t := range sup {
		set[t] = struct{}{}
	}
	for _, t := range sub {
		if _, ok := set[t]; !ok {
			return false
		}
	}
	return true
}

// Query returns nodes matching filters. Empty status or name matches all.
func Query(db *sql.DB, status, name string) ([]Node, error) {
	q := `SELECT id, name, chip, COALESCE(node_kind, 'physical'), COALESCE(node_identity, ''), status, last_seen, scheduler_tier, native_json, agents_json, manual_json, capacity_json, resource_profile_json, neighbors_json, links_json, contacts_json, projects_json, COALESCE(pub_key,''), COALESCE(key_verified,0)
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
		// JSON columns are nullable in practice: resource_profile_json is added
		// later by a migration (legacy rows are NULL until re-upserted), and a
		// partial insert leaves the others NULL too. Scan all of them as nullable
		// so a single such row does not fail the whole directory query.
		var native, agents, manual, capJSON, resJSON, neighborsJSON, linksJSON, contactsJSON, projectsJSON sql.NullString
		if err := rows.Scan(&n.ID, &n.Name, &n.Chip, &n.NodeKind, &n.NodeIdentity, &n.Status, &n.LastSeen, &n.SchedulerTier,
			&native, &agents, &manual, &capJSON, &resJSON, &neighborsJSON, &linksJSON, &contactsJSON, &projectsJSON,
			&n.PubKey, &n.KeyVerified); err != nil {
			return nil, err
		}
		if native.Valid && native.String != "" {
			_ = json.Unmarshal([]byte(native.String), &n.Native)
		}
		if agents.Valid && agents.String != "" {
			_ = json.Unmarshal([]byte(agents.String), &n.Agents)
		}
		if manual.Valid && manual.String != "" {
			_ = json.Unmarshal([]byte(manual.String), &n.Manual)
		}
		if capJSON.Valid && capJSON.String != "" {
			_ = json.Unmarshal([]byte(capJSON.String), &n.Capacity)
		}
		if resJSON.Valid && resJSON.String != "" {
			_ = json.Unmarshal([]byte(resJSON.String), &n.ResourceProfile)
		}
		if neighborsJSON.Valid && neighborsJSON.String != "" {
			_ = json.Unmarshal([]byte(neighborsJSON.String), &n.Neighbors)
		}
		if linksJSON.Valid && linksJSON.String != "" {
			var links []LinkMetric
			if err := json.Unmarshal([]byte(linksJSON.String), &links); err == nil {
				for _, l := range links {
					if n.LinkMetrics == nil {
						n.LinkMetrics = make(map[string]int64)
					}
					n.LinkMetrics[l.Peer] = l.RTTms
				}
			}
		}
		if contactsJSON.Valid && contactsJSON.String != "" {
			_ = json.Unmarshal([]byte(contactsJSON.String), &n.Contacts)
		}
		if projectsJSON.Valid && projectsJSON.String != "" {
			_ = json.Unmarshal([]byte(projectsJSON.String), &n.Projects)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
