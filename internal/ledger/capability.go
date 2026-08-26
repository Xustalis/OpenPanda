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
	"unicode"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

// Card is the parsed form of capabilities.yaml for this node.
type Card struct {
	Device          string           `yaml:"device"`
	ResourceClass   string           `yaml:"resource_class"`
	NodeKind        string           `yaml:"node_kind,omitempty" json:"node_kind,omitempty"`
	NodeIdentity    string           `yaml:"node_identity,omitempty" json:"node_identity,omitempty"`
	Chip            string           `yaml:"chip"`
	Native          []NativeAbility  `yaml:"native"`
	Agents          map[string]Agent `yaml:"agents"`
	Manual          []ManualAbility  `yaml:"manual"`
	Capacity        Capacity         `yaml:"capacity"`
	ResourceProfile ResourceProfile  `yaml:"resource_profile"`
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
	// Tier mirrors NativeAbility.Tier: 1=reversible, 2=irreversible (needs
	// auth). Zero defaults to 2 — an agent CLI can run arbitrary shell through
	// the model, so the safe default is to require consent unless the card
	// explicitly declares the agent read-only (P1-15).
	Tier int `yaml:"tier"`
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
	Capacity      Capacity            `json:"capacity"`
	// ResourceProfile is what makes "this node cannot run that" decidable off the
	// network instead of only locally: a task declaring 8 GiB of VRAM must not be
	// forwarded to a node with none, and before v0.0.6 the peer half of this
	// field was simply dropped, so every peer looked equally capable.
	ResourceProfile ResourceProfile `json:"resource_profile,omitempty"`
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
	return upsertNode(db, id, c.Device, c.Chip, kind, identity, string(native), string(agents), string(manual), string(capJSON), string(resJSON), tier)
}

// upsertNode writes one directory row — native/agents/manual/capacity/resource
// profile already marshalled to JSON — and marks it online. Shared by Register
// (self, full card) and UpsertRemote (peer, ID-only summary) so the upsert SQL
// lives in one place.
func upsertNode(db *sql.DB, id, device, chip, kind, identity, nativeJSON, agentsJSON, manualJSON, capJSON, resJSON string, tier int) error {
	_, err := db.Exec(`
		INSERT INTO employee_cache (id, name, department, chip, node_kind, node_identity, native_json, agents_json, manual_json, capacity_json, resource_profile_json, status, last_seen, scheduler_tier)
		VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, 'online', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, chip=excluded.chip,
			node_kind=excluded.node_kind, node_identity=excluded.node_identity,
			native_json=excluded.native_json, agents_json=excluded.agents_json,
			manual_json=excluded.manual_json, capacity_json=excluded.capacity_json,
			resource_profile_json=excluded.resource_profile_json,
			status='online', last_seen=excluded.last_seen, scheduler_tier=excluded.scheduler_tier`,
		id, device, chip, kind, identity, nativeJSON, agentsJSON, manualJSON, capJSON, resJSON, storage.Now(), tier,
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
	resJSON, err := json.Marshal(s.ResourceProfile)
	if err != nil {
		return fmt.Errorf("marshal remote resource profile: %w", err)
	}

	kind, identity := s.NodeKind, s.NodeIdentity
	if kind == "" {
		kind = "physical"
	}
	if identity == "" {
		identity = id
	}
	return upsertNode(db, id, s.Device, s.Chip, kind, identity, string(nativeJSON), string(agentsJSON), string(manualJSON), string(capJSON), string(resJSON), s.SchedulerTier)
}

// Node is a single employee_cache row, decoded.
type Node struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Chip            string           `json:"chip,omitempty"`
	NodeKind        string           `json:"node_kind"`
	NodeIdentity    string           `json:"node_identity,omitempty"`
	Status          string           `json:"status"`
	LastSeen        int64            `json:"last_seen"`
	SchedulerTier   int              `json:"scheduler_tier"`
	Native          []NativeAbility  `json:"native,omitempty"`
	Agents          map[string]Agent `json:"agents,omitempty"`
	Manual          []ManualAbility  `json:"manual,omitempty"`
	Capacity        Capacity         `json:"capacity"`
	ResourceProfile ResourceProfile  `json:"resource_profile"`
}

// Abilities returns this node's displayable ability list — native IDs plus an
// "agent:<name>" entry per configured agent — sorted for deterministic output.
// It is the single shared form used by the CLI status panel and the entry-model
// device summary, so the two stay in lock-step as new ability kinds appear.
func (n Node) Abilities() []string {
	out := make([]string, 0, len(n.Native)+len(n.Agents))
	for _, a := range n.Native {
		out = append(out, a.ID)
	}
	for name := range n.Agents {
		out = append(out, "agent:"+name)
	}
	sort.Strings(out)
	return out
}

// Matches reports whether this node declares any of required, across the
// three ability layers (native / agent / manual). Mirrors commander.Router's
// per-kind matching so network-level and local routing agree on semantics.
//
// A required id of the form "agent:<name>" refers to a configured agent by
// name (the form advertised in the device summary); any other id matches a
// declared native/manual id or an agent capability, with token-subset matching
// that bridges separator/category-prefix differences (see AbilityMatches).
func (n Node) Matches(required []string) bool {
	// Pre-tokenize the declared ids once; otherwise each required id would
	// re-tokenize the whole declared set (O(R×A) allocations instead of O(A)).
	native, agentCaps, manual := n.tokenizedAbilities()
	for _, req := range required {
		if name, ok := strings.CutPrefix(req, "agent:"); ok {
			if _, exists := n.Agents[name]; exists {
				return true
			}
			continue
		}
		r := tokenizeAbility(req)
		if matchTokens(native, r) || matchTokens(agentCaps, r) || matchTokens(manual, r) {
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
// a node that positively declares its hardware can be positively excluded.
func (n Node) Fits(req ResourceProfile) bool {
	if !req.Declared() || !n.ResourceProfile.Declared() {
		return true
	}
	if req.GPUVRAMGB > 0 && n.ResourceProfile.GPUVRAMGB < req.GPUVRAMGB {
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

// tokenizedAbilities returns the declared native/agent/manual ability ids as
// case-folded token sets, computed once so Matches does not re-tokenize them
// per required id.
func (n Node) tokenizedAbilities() (native, agentCaps, manual [][]string) {
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
	return native, agentCaps, manual
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
	q := `SELECT id, name, chip, COALESCE(node_kind, 'physical'), COALESCE(node_identity, ''), status, last_seen, scheduler_tier, native_json, agents_json, manual_json, capacity_json, resource_profile_json
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
		var native, agents, manual, capJSON, resJSON sql.NullString
		if err := rows.Scan(&n.ID, &n.Name, &n.Chip, &n.NodeKind, &n.NodeIdentity, &n.Status, &n.LastSeen, &n.SchedulerTier,
			&native, &agents, &manual, &capJSON, &resJSON); err != nil {
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
		out = append(out, n)
	}
	return out, rows.Err()
}
