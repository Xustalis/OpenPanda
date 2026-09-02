// Package config loads PANDA node configuration from YAML.
package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/executil"
	"gopkg.in/yaml.v3"
)

// Config is the top-level node configuration.
type Config struct {
	Node      NodeConfig      `yaml:"node"`
	Network   NetworkConfig   `yaml:"network"`
	Storage   StorageConfig   `yaml:"storage"`
	Log       LogConfig       `yaml:"log"`
	Model     ModelConfig     `yaml:"model"`
	Push      PushConfig      `yaml:"push"`
	MCP       MCPConfig       `yaml:"mcp"`
	Injection InjectionConfig `yaml:"injection"`
	Routing   RoutingConfig   `yaml:"routing"`
	Memory    MemoryConfig    `yaml:"memory"`
	Approval  ApprovalConfig  `yaml:"approval"`
	Timeouts  TimeoutsConfig  `yaml:"timeouts"`
}

// Injection model strategies (injection.model).
const (
	// InjectionModelAuto injects the panda model endpoint into an agent
	// subprocess only when the agent carries no model credentials of its own
	// (env vars, login state, or its config files) and panda has a model
	// configured. This is the default: agent-native models win.
	InjectionModelAuto = "auto"
	// InjectionModelAlways always overrides the agent with the panda model
	// endpoint (legacy behavior).
	InjectionModelAlways = "always"
	// InjectionModelNever never injects; agents always use their own models.
	InjectionModelNever = "never"
)

// InjectionConfig controls what panda injects into agent subprocesses.
type InjectionConfig struct {
	Model string `yaml:"model"` // auto | always | never (default auto)
}

// NormalizedModel returns the validated injection.model strategy, defaulting
// to auto when unset.
func (i InjectionConfig) NormalizedModel() string {
	switch i.Model {
	case InjectionModelAlways, InjectionModelNever:
		return i.Model
	default:
		return InjectionModelAuto
	}
}

// RoutingConfig tunes local agent routing.
type RoutingConfig struct {
	// PreferredAgents lists agent names that receive a score bonus during
	// routing, so the user can pin favorites without editing capability cards.
	PreferredAgents []string `yaml:"preferred_agents"`
	// ToolsPolicy grades the tool face agent adapters run with: minimal
	// (default) keeps each adapter's safe whitelist; extended lifts the
	// restriction so the agent's own skills, sub-agent tooling and MCP
	// servers configured for the work directory are reachable. Widening the
	// tool face is an explicit operator choice, never a default.
	ToolsPolicy string `yaml:"tools_policy"`
}

// Agent tool policies (routing.tools_policy).
const (
	// ToolsPolicyMinimal keeps the adapter's safe tool whitelist (default).
	ToolsPolicyMinimal = "minimal"
	// ToolsPolicyExtended lifts the whitelist so agent-native skills, the
	// sub-agent Task tool and project MCP servers are usable.
	ToolsPolicyExtended = "extended"
)

// NormalizedToolsPolicy returns the validated agent tools policy, defaulting
// to minimal when unset.
func (r RoutingConfig) NormalizedToolsPolicy() string {
	if r.ToolsPolicy == ToolsPolicyExtended {
		return ToolsPolicyExtended
	}
	return ToolsPolicyMinimal
}

// Default memory size limits (characters). They override the compile-time
// constants in internal/memory once that package becomes config-driven.
const (
	DefaultMemoryLimitUser    = 5000
	DefaultMemoryLimitMemory  = 10000
	DefaultMemoryLimitProject = 30000
)

// MemoryConfig holds memory-system tunables.
type MemoryConfig struct {
	Limits MemoryLimitsConfig `yaml:"limits"`
}

// MemoryLimitsConfig caps the size of each memory file class. Zero or
// negative values fall back to the defaults at load time, so old config
// files without a memory section keep working.
type MemoryLimitsConfig struct {
	User    int `yaml:"user"`    // USER.md cap (default 5000)
	Memory  int `yaml:"memory"`  // MEMORY.md cap (default 10000)
	Project int `yaml:"project"` // per-project MEMORY.md cap (default 30000)
}

// Approval modes (approval.mode).
const (
	// ApprovalModeAlways requires explicit user approval for every task.
	ApprovalModeAlways = "always"
	// ApprovalModeOnRequest requires approval only when the entry model marks
	// a task as needing it (default).
	ApprovalModeOnRequest = "on-request"
	// ApprovalModeNever never asks; tasks run as classified.
	ApprovalModeNever = "never"
)

// ApprovalConfig controls the task approval gate.
type ApprovalConfig struct {
	Mode string `yaml:"mode"` // always | on-request | never (default on-request)
}

// NormalizedMode returns the validated approval mode, defaulting to
// on-request when unset.
func (a ApprovalConfig) NormalizedMode() string {
	switch a.Mode {
	case ApprovalModeAlways, ApprovalModeNever:
		return a.Mode
	default:
		return ApprovalModeOnRequest
	}
}

// NodeConfig identifies this node.
type NodeConfig struct {
	Name          string `yaml:"name"`
	ResourceClass string `yaml:"resource_class"`     // Micro | Standard | Full
	Kind          string `yaml:"kind"`               // physical | vm (default physical)
	Identity      string `yaml:"identity,omitempty"` // stable VM identity; physical nodes use host fingerprint
}

// NetworkConfig controls the WebSocket listener and manual peers. PanelAddr and
// PanelToken are read only by the optional webui sidecar (webui/cmd/panel), not
// by the kernel daemon.
type NetworkConfig struct {
	ListenAddr          string   `yaml:"listen_addr"`            // e.g. ":7836"
	PanelAddr           string   `yaml:"panel_addr"`             // webui sidecar HTTP listener; loopback by default (P1-24)
	PanelToken          string   `yaml:"panel_token"`            // Bearer token guarding /api/* in the webui sidecar
	SharedSecret        string   `yaml:"shared_secret"`          // HMAC secret authenticating node-to-node hellos; the WS listener refuses to start without it
	Peers               []string `yaml:"peers"`                  // e.g. "worker-1.your-tailnet.ts.net:7836"
	MaxConnections      int      `yaml:"max_connections"`        // global concurrent WS connection limit (0 = unlimited)
	MaxConnectionsPerIP int      `yaml:"max_connections_per_ip"` // per-remote-IP concurrent WS connection limit (0 = unlimited)
}

// StorageConfig controls local persistence.
type StorageConfig struct {
	DBPath       string `yaml:"db_path"`
	ContextPath  string `yaml:"context_path"`
	MemoryPath   string `yaml:"memory_path"`   // Hermes personal memory root (memory/)
	ProjectsPath string `yaml:"projects_path"` // per-project memory root (projects/)
	SkillsPath   string `yaml:"skills_path"`   // procedural-memory root (skills/)
	WorkPath     string `yaml:"work_path"`     // agents execute here; scope drift is measured against it
	ArtifactPath string `yaml:"artifact_path"` // packed task artifacts (artifacts/), named by hash
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level string `yaml:"level"` // debug | info | warn | error
}

// Built-in timeout defaults, used when timeouts.* is unset. The lease must stay
// comfortably above the agent budget: a task whose lease expires while its
// adapter is still legitimately running gets force-failed and re-routed, so the
// same work runs twice on two nodes at once.
const (
	DefaultAgentTimeoutS     = 600
	DefaultTaskLeaseS        = 1200
	DefaultSuperviseRoundsCf = 5
)

// TimeoutsConfig bounds long-running task execution. All durations are seconds;
// zero means "use the built-in default". Operators need these knobs because a
// deep-learning stage can legitimately run far longer than a code edit, and the
// defaults are tuned for the latter.
type TimeoutsConfig struct {
	// TaskLeaseS is how long one task attempt may hold its lease before the
	// monitor treats the executor as dead. The lease is renewed on a heartbeat
	// while execution is live, so this bounds silence, not total runtime.
	TaskLeaseS int `yaml:"task_lease_s"`
	// AgentS is the wall-clock budget for one agent-adapter execution
	// (advertised to the adapter and enforced with a hard deadline).
	AgentS int `yaml:"agent_s"`
	// AgentByKind overrides AgentS for specific plan kinds (e.g. "training",
	// "qa"). A kind not listed falls back to AgentS. This lets long-running
	// training stages get a larger budget without inflating the default for
	// quick code-edit tasks.
	AgentByKind map[string]int `yaml:"agent_by_kind"`
	// SuperviseRounds caps the execute → judge → re-delegate loop per task.
	SuperviseRounds int `yaml:"supervise_rounds"`
}

// TaskLease returns the configured task lease, or the default when unset.
func (t TimeoutsConfig) TaskLease() time.Duration {
	if t.TaskLeaseS > 0 {
		return time.Duration(t.TaskLeaseS) * time.Second
	}
	return DefaultTaskLeaseS * time.Second
}

// AgentTimeout returns the configured agent-execution budget, or the default
// when unset.
func (t TimeoutsConfig) AgentTimeout() time.Duration {
	if t.AgentS > 0 {
		return time.Duration(t.AgentS) * time.Second
	}
	return DefaultAgentTimeoutS * time.Second
}

// AgentTimeoutForKind returns the per-kind agent timeout when configured,
// falling back to AgentTimeout. A kind not present in AgentByKind uses the
// global AgentS, so only kinds that genuinely differ need an entry.
func (t TimeoutsConfig) AgentTimeoutForKind(kind string) time.Duration {
	if s, ok := t.AgentByKind[kind]; ok && s > 0 {
		return time.Duration(s) * time.Second
	}
	return t.AgentTimeout()
}

// Rounds returns the configured supervision round budget, or the default.
func (t TimeoutsConfig) Rounds() int {
	if t.SuperviseRounds > 0 {
		return t.SuperviseRounds
	}
	return DefaultSuperviseRoundsCf
}

// API type wire values: which request/response dialect the provider speaks.
const (
	// APITypeAnthropic is the Anthropic Messages API (/v1/messages, x-api-key
	// header). DeepSeek's /anthropic endpoint and Anthropic itself use it.
	APITypeAnthropic = "anthropic"
	// APITypeOpenAI is the OpenAI Chat Completions API (/v1/chat/completions,
	// Authorization: Bearer). OpenAI, DeepSeek's OpenAI endpoint, and most
	// third-party gateways (Ollama, vLLM, OneAPI…) use it.
	APITypeOpenAI = "openai"
)

// ModelConfig selects the LLM provider for the entry model and the agent
// adapters. api_type picks the wire format — "anthropic" (Messages API,
// default) or "openai" (Chat Completions) — so any provider works with a
// custom base_url + model; the user is never locked to a fixed vendor.
type ModelConfig struct {
	APIType   string `yaml:"api_type"`   // "anthropic" | "openai" (default anthropic)
	BaseURL   string `yaml:"base_url"`   // e.g. https://api.deepseek.com/anthropic
	APIKey    string `yaml:"api_key"`    // secret; prefer env OPENPANDA_MODEL_API_KEY
	Model     string `yaml:"model"`      // e.g. deepseek-chat | gpt-4o-mini — fully user-defined
	MaxTokens int    `yaml:"max_tokens"` // completion cap; 0 = provider/entry default
}

// NormalizedAPIType returns the validated api type, defaulting to Anthropic.
func (m ModelConfig) NormalizedAPIType() string {
	if m.APIType == APITypeOpenAI {
		return APITypeOpenAI
	}
	return APITypeAnthropic
}

// MCPConfig selects the stdio MCP server whose tools the ask engine may call
// (design §7.3). Command is a space-separated argv (quotes honored), e.g.
// `npx -y @modelcontextprotocol/server-filesystem /tmp`. Empty disables MCP;
// a CLI --mcp flag overrides it.
type MCPConfig struct {
	Command string `yaml:"command"`
}

// PushConfig enables Web Push notifications (design P3-26) for the optional
// webui sidecar (webui/cmd/panel); the kernel daemon does not read it. VAPID
// (RFC 8292) needs a stable P-256 keypair and a mailto:/https: subject
// identifying the sender; the key is persisted at vapid_key_path so browser
// subscriptions stay valid across restarts.
type PushConfig struct {
	Enabled      bool   `yaml:"enabled"`
	VAPIDSubject string `yaml:"vapid_subject"` // e.g. mailto:ops@example.com
	VAPIDKeyPath string `yaml:"vapid_key_path"`
}

// validResourceClasses are the resource classes schedulerTier understands;
// anything else silently degrades to a worker tier, so catch typos at startup.
var validResourceClasses = map[string]bool{"Micro": true, "Standard": true, "Full": true, "": true}

const (
	NodeKindPhysical = "physical"
	NodeKindVM       = "vm"
)

// ValidNodeKind reports whether kind is a supported node identity class.
func ValidNodeKind(kind string) bool {
	return kind == "" || kind == NodeKindPhysical || kind == NodeKindVM
}

// MachineIdentity returns a stable, non-secret identity for the current host.
// OPENPANDA_NODE_IDENTITY is accepted for compatibility when generating a
// default config; physical runtime locking still uses PhysicalIdentity.
func MachineIdentity() string {
	if v := os.Getenv("OPENPANDA_NODE_IDENTITY"); v != "" {
		return v
	}
	return PhysicalIdentity()
}

// PhysicalIdentity ignores user-provided overrides and identifies the host
// itself. It is used for the physical-node singleton lock.
//
// The order is "most stable first": a machine-id file survives a rename, the
// platform UUID survives a reinstall, and the hostname fallback survives
// nothing but is better than a constant. The last resort matters — two nodes
// that both fall back to the same fingerprint would fight over one lock, so the
// fallback is at least per-hostname.
func PhysicalIdentity() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id", "/var/db/SystemConfiguration/.com.apple.uuid"} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return "machine-" + shortHash(s)
			}
		}
	}
	if s := platformMachineID(); s != "" {
		return "machine-" + shortHash(s)
	}
	host, _ := os.Hostname()
	if host == "" {
		host = runtime.GOOS + "-" + runtime.GOARCH
	}
	return "host-" + shortHash(host)
}

// platformMachineID reads the OS's own hardware UUID where no machine-id file
// exists. macOS has none of the paths above on a stock install (that
// .com.apple.uuid file is not always present), and Windows has none of them at
// all — without this both platforms would fall back to the hostname, so
// renaming the machine would look like a different node and the physical
// singleton lock would stop recognising its own host.
func platformMachineID() string {
	switch runtime.GOOS {
	case "darwin":
		out := probeIdentity("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, "IOPlatformUUID") {
				continue
			}
			if _, v, ok := strings.Cut(line, "="); ok {
				return strings.Trim(strings.TrimSpace(v), `"`)
			}
		}
	case "windows":
		// MachineGuid is written once at install and is the conventional
		// Windows host fingerprint.
		out := probeIdentity("reg", "query",
			`HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid")
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			for i, f := range fields {
				if strings.HasPrefix(f, "REG_") && i+1 < len(fields) {
					return fields[i+1]
				}
			}
		}
	}
	return ""
}

// probeIdentity runs a short identity probe. executil keeps the console window
// hidden on Windows — the daemon is headless there and must not flash a
// terminal at startup.
func probeIdentity(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := executil.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// EffectiveIdentity is the identity used for runtime ownership. Physical
// nodes always use the host fingerprint; VM nodes may supply a separate stable
// identity so a host and its guest can coexist.
func (n NodeConfig) EffectiveIdentity() string {
	if n.Kind == NodeKindPhysical {
		return PhysicalIdentity()
	}
	if strings.TrimSpace(n.Identity) != "" {
		return strings.TrimSpace(n.Identity)
	}
	return PhysicalIdentity()
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// ValidResourceClass reports whether s is a resource class the scheduler
// understands (empty counts as "unset → default"). `panda init` uses it to
// re-prompt on typos before they can break a later config load.
func ValidResourceClass(s string) bool {
	return validResourceClasses[s]
}

// Validate reports statically invalid configuration before the node starts:
// an unknown resource_class (a typo silently downgrades the scheduler tier),
// malformed peer addresses (must be host:port), or a listen/panel address that
// is not host:port. Reachability is NOT checked here — peers may come and go
// at runtime; the daemon's MaintainPeer loop already warns on dial failures.
func (c *Config) Validate() error {
	if !validResourceClasses[c.Node.ResourceClass] {
		return fmt.Errorf("config: node.resource_class %q is invalid (want Micro, Standard, or Full)", c.Node.ResourceClass)
	}
	if !ValidNodeKind(c.Node.Kind) {
		return fmt.Errorf("config: node.kind %q is invalid (want physical or vm)", c.Node.Kind)
	}
	if c.Node.Kind == NodeKindVM && strings.TrimSpace(c.Node.Identity) == "" {
		return fmt.Errorf("config: node.identity is required for vm nodes")
	}
	for _, addr := range []struct{ name, value string }{
		{"network.listen_addr", c.Network.ListenAddr},
		{"network.panel_addr", c.Network.PanelAddr},
	} {
		if addr.value == "" {
			continue // optional / disabled
		}
		if _, _, err := net.SplitHostPort(addr.value); err != nil {
			return fmt.Errorf("config: %s %q is not host:port: %w", addr.name, addr.value, err)
		}
	}
	for i, peer := range c.Network.Peers {
		if _, _, err := net.SplitHostPort(peer); err != nil {
			return fmt.Errorf("config: network.peers[%d] %q is not host:port: %w", i, peer, err)
		}
	}
	switch c.Injection.Model {
	case "", InjectionModelAuto, InjectionModelAlways, InjectionModelNever:
	default:
		return fmt.Errorf("config: injection.model %q is invalid (want auto, always, or never)", c.Injection.Model)
	}
	switch c.Approval.Mode {
	case "", ApprovalModeAlways, ApprovalModeOnRequest, ApprovalModeNever:
	default:
		return fmt.Errorf("config: approval.mode %q is invalid (want always, on-request, or never)", c.Approval.Mode)
	}
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"memory.limits.user", c.Memory.Limits.User},
		{"memory.limits.memory", c.Memory.Limits.Memory},
		{"memory.limits.project", c.Memory.Limits.Project},
	} {
		if limit.value < 0 {
			return fmt.Errorf("config: %s %d must not be negative", limit.name, limit.value)
		}
	}
	return nil
}

// normalize fills in defaults for fields that load as zero on old config
// files (or explicit empties), so callers can read every field without
// nil-checks. Load runs it after unmarshal; Default() already carries these
// values, so this is a no-op for a fresh config.
func (c *Config) normalize() {
	if c.Node.Kind == "" {
		c.Node.Kind = NodeKindPhysical
	}
	if c.Node.Kind != NodeKindVM && strings.TrimSpace(c.Node.Identity) == "" {
		c.Node.Identity = MachineIdentity()
	}
	if c.Injection.Model == "" {
		c.Injection.Model = InjectionModelAuto
	}
	if c.Approval.Mode == "" {
		c.Approval.Mode = ApprovalModeOnRequest
	}
	if c.Memory.Limits.User <= 0 {
		c.Memory.Limits.User = DefaultMemoryLimitUser
	}
	if c.Memory.Limits.Memory <= 0 {
		c.Memory.Limits.Memory = DefaultMemoryLimitMemory
	}
	if c.Memory.Limits.Project <= 0 {
		c.Memory.Limits.Project = DefaultMemoryLimitProject
	}
	// Artifacts are task *outputs* that travel between nodes: a build tree, a
	// trained model. They get a root of their own because they belong neither
	// in ctxstore (whose LRU keeps as few as 5 entries on a Micro node and
	// would silently evict them) nor in SQLite (they are measured in GB). The
	// root is derived from the database's directory rather than fixed at
	// Default() time, so it follows storage wherever the deployer put it.
	if strings.TrimSpace(c.Storage.ArtifactPath) == "" {
		c.Storage.ArtifactPath = filepath.Join(filepath.Dir(c.Storage.DBPath), "artifacts")
	}
}

// UserDataDir returns the per-user state directory used when no config
// overrides storage paths. It follows the install / uninstall layout from
// docs/install.md and the READMEs:
//   - Unix: ${XDG_DATA_HOME:-$HOME/.local/share}/openpanda
//   - macOS: ~/Library/Application Support/openpanda (falls back to the
//     Unix convention when os.UserHomeDir fails)
//   - Windows: %LOCALAPPDATA%\openpanda-data
//
// On Windows the directory is deliberately NOT "openpanda": the installer
// prefix is %LOCALAPPDATA%\OpenPanda and NTFS is case-insensitive, so a
// same-named data dir would collapse into the install prefix and an
// uninstall would take the database with it.
//
// A best-effort fallback (./data relative to cwd) is used when the home
// directory cannot be determined — that keeps test and container scenarios
// working without user intervention.
func UserDataDir() (string, error) {
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "openpanda-data"), nil
		}
	} else if runtime.GOOS == "darwin" {
		if dir, err := os.UserConfigDir(); err == nil {
			// os.UserConfigDir on macOS = ~/Library/Application Support.
			// Keep data alongside config for a single easy-to-find folder.
			return filepath.Join(dir, "openpanda"), nil
		}
	}
	// Generic Unix / fallback: XDG_DATA_HOME or ~/.local/share/openpanda.
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "openpanda"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", "openpanda"), nil
}

// userDataDirBestEffort mirrors UserDataDir but returns ./data as a safe
// fallback when the user's home cannot be resolved — used by Default() so
// callers that do not check the error still get a usable path.
func userDataDirBestEffort() string {
	if d, err := UserDataDir(); err == nil {
		return d
	}
	return "data"
}

// DefaultNodeName is the node name a fresh config gets: this machine's
// hostname, normalised. It used to be the literal "macbook", which meant every
// node in a network that never ran `panda init` announced itself under the
// author's laptop name — and node names are how peers and the queue refer to a
// machine, so two such nodes are indistinguishable in every listing.
//
// Normalisation matters because the name travels in wire payloads and log
// lines: a hostname may be "Xenith-MacBook-Pro.local" or carry non-ASCII.
func DefaultNodeName() string {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	// A .local / .lan suffix is mDNS decoration, not part of the machine name.
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "panda-" + runtime.GOOS + "-" + runtime.GOARCH
	}
	return name
}

// Default returns a Config with per-user absolute paths by default, so
// running `panda` from any working directory hits the same SQLite store.
func Default() *Config {
	data := userDataDirBestEffort()
	return &Config{
		Node: NodeConfig{
			Name:          DefaultNodeName(),
			ResourceClass: "Standard",
			Kind:          NodeKindPhysical,
		},
		Network: NetworkConfig{
			// Loopback by default (review P1-2): the bus speaks unauthenticated-
			// after-hello WebSocket, so a wildcard bind would expose delegation
			// traffic and the hello signature to the LAN. A node that should be
			// reachable by peers must set listen_addr explicitly — to a routable
			// interface or, preferably, a Tailscale/WireGuard overlay address.
			ListenAddr: "127.0.0.1:7836",
			// Loopback by default (P1-24): the panel speaks plain HTTP, so a
			// wildcard bind would expose the Bearer token and task contents to
			// the LAN. Set panel_addr explicitly to expose it (e.g. behind a
			// TLS reverse proxy).
			PanelAddr: "127.0.0.1:7840",
			// Conservative defaults for a personal device network.
			MaxConnections:      64,
			MaxConnectionsPerIP: 8,
		},
		Storage: StorageConfig{
			DBPath:       filepath.Join(data, "openpanda.db"),
			ContextPath:  filepath.Join(data, "context"),
			MemoryPath:   filepath.Join(data, "memory"),
			ProjectsPath: filepath.Join(data, "projects"),
			SkillsPath:   filepath.Join(data, "skills"),
			WorkPath:     data,
			// ArtifactPath is deliberately left empty here and derived in
			// normalize() from whatever the database path ends up being. A
			// config file that moves storage next to itself but predates
			// artifact_path would otherwise keep the per-user default and
			// scatter a node's state across two roots.
		},
		Log: LogConfig{
			Level: "info",
		},
		Model: ModelConfig{
			BaseURL: "https://api.deepseek.com/anthropic",
			// deepseek-chat/deepseek-reasoner were deprecated aliases retired
			// by DeepSeek on 2026-07-24; deepseek-v4-flash is the successor
			// default (deepseek-v4-pro is deliberately never a default).
			Model:     "deepseek-v4-flash",
			MaxTokens: 4096,
		},
		Push: PushConfig{
			Enabled:      false,
			VAPIDSubject: "mailto:panda@localhost",
			VAPIDKeyPath: filepath.Join(data, "vapid.pem"),
		},
		Injection: InjectionConfig{
			Model: InjectionModelAuto,
		},
		Routing: RoutingConfig{
			PreferredAgents: []string{},
		},
		Memory: MemoryConfig{
			Limits: MemoryLimitsConfig{
				User:    DefaultMemoryLimitUser,
				Memory:  DefaultMemoryLimitMemory,
				Project: DefaultMemoryLimitProject,
			},
		},
		Approval: ApprovalConfig{
			Mode: ApprovalModeOnRequest,
		},
		Timeouts: TimeoutsConfig{
			TaskLeaseS:      DefaultTaskLeaseS,
			AgentS:          DefaultAgentTimeoutS,
			SuperviseRounds: DefaultSuperviseRoundsCf,
		},
	}
}

// SystemConfigDir is the machine-wide configuration directory: /etc/openpanda
// on unix (byte-for-byte the historical path), %ProgramData%\OpenPanda on
// Windows, where /etc does not exist and ProgramData is the sanctioned home
// for machine-scoped application state.
func SystemConfigDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("ProgramData"), "OpenPanda")
	}
	return "/etc/openpanda"
}

// SystemConfigPath is the machine-wide config file location derived from
// SystemConfigDir.
func SystemConfigPath() string {
	return filepath.Join(SystemConfigDir(), "config.yaml")
}

// DefaultPath is where the node looks for its config file when nothing more
// specific is found. A var (not a const) so it can follow SystemConfigDir's
// platform split; on unix it stays exactly /etc/openpanda/config.yaml.
var DefaultPath = SystemConfigPath()

// UserConfigPath returns a user-writable config location —
// ~/.config/openpanda/config.yaml on Linux,
// ~/Library/Application Support/openpanda/config.yaml on macOS,
// %LOCALAPPDATA%\openpanda\config.yaml on Windows. It is where `panda init`
// writes by default, so a first run never needs root to place its config.
func UserConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "openpanda", "config.yaml"), nil
}

// ResolvePath returns the config path a node actually reads, in order:
// explicit flag > OPENPANDA_CONFIG_PATH > a user-level config (if present) >
// the system default. Load and the panel mirrors all go through here, so a
// `panda init`-written user config is picked up automatically by `panda web`
// and the daemon without extra flags.
func ResolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("OPENPANDA_CONFIG_PATH"); env != "" {
		return env
	}
	if user, err := UserConfigPath(); err == nil {
		if _, err := os.Stat(user); err == nil {
			return user
		}
	}
	return DefaultPath
}

// resolveRelativePaths rebases every storage path that is still relative
// onto baseDir. Old config files written by `panda init` (when Default used
// ./data relatives) keep working regardless of the process's cwd: they are
// interpreted relative to the config file's directory, not the user's shell
// location. Absolute paths are untouched.
func (c *Config) resolveRelativePaths(baseDir string) {
	if !filepath.IsAbs(c.Storage.DBPath) {
		c.Storage.DBPath = filepath.Join(baseDir, c.Storage.DBPath)
	}
	if !filepath.IsAbs(c.Storage.ContextPath) {
		c.Storage.ContextPath = filepath.Join(baseDir, c.Storage.ContextPath)
	}
	if !filepath.IsAbs(c.Storage.MemoryPath) {
		c.Storage.MemoryPath = filepath.Join(baseDir, c.Storage.MemoryPath)
	}
	if !filepath.IsAbs(c.Storage.ProjectsPath) {
		c.Storage.ProjectsPath = filepath.Join(baseDir, c.Storage.ProjectsPath)
	}
	if !filepath.IsAbs(c.Storage.SkillsPath) {
		c.Storage.SkillsPath = filepath.Join(baseDir, c.Storage.SkillsPath)
	}
	if !filepath.IsAbs(c.Storage.WorkPath) {
		c.Storage.WorkPath = filepath.Join(baseDir, c.Storage.WorkPath)
	}
	// artifact_path is the one storage path with no default of its own: an
	// empty value must not become baseDir itself, which would drop multi-GB
	// archives next to the YAML. normalize() derives it instead.
	if c.Storage.ArtifactPath != "" && !filepath.IsAbs(c.Storage.ArtifactPath) {
		c.Storage.ArtifactPath = filepath.Join(baseDir, c.Storage.ArtifactPath)
	}
	if !filepath.IsAbs(c.Push.VAPIDKeyPath) {
		c.Push.VAPIDKeyPath = filepath.Join(baseDir, c.Push.VAPIDKeyPath)
	}
}

// Load reads the config from path. If path is empty, the OPENPANDA_CONFIG_PATH env
// var (if set) or DefaultPath is used. A missing file is not an error; defaults
// apply. An unreadable or malformed file is an error so a bad deployment
// surfaces loudly.
func Load(path string) (*Config, error) {
	if path == "" {
		path = ResolvePath("")
	}
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No config file on disk: Default already has absolute per-user
			// paths, so there is nothing to rebase.
			cfg.applyEnv()
			cfg.normalize()
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	hardenSecretPerms(path, data)
	// Rebase relative storage paths onto the config file's directory so
	// `./data/openpanda.db` in config.yaml always points next to the YAML,
	// never next to whatever directory the user ran `panda` from.
	if abs, err := filepath.Abs(path); err == nil {
		cfg.resolveRelativePaths(filepath.Dir(abs))
	}
	cfg.applyEnv()
	if cfg.Node.Name == "" {
		return nil, fmt.Errorf("config %s: node.name must not be empty", path)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// applyEnv lets individual env vars override config fields, useful for tests
// and containerized deploys.
func (c *Config) applyEnv() {
	if v := os.Getenv("OPENPANDA_NODE_NAME"); v != "" {
		c.Node.Name = v
	}
	if v := os.Getenv("OPENPANDA_NODE_KIND"); v != "" {
		c.Node.Kind = v
	}
	if v := os.Getenv("OPENPANDA_NODE_IDENTITY"); v != "" {
		c.Node.Identity = v
	}
	if v := os.Getenv("OPENPANDA_LISTEN_ADDR"); v != "" {
		c.Network.ListenAddr = v
	}
	if v := os.Getenv("OPENPANDA_PANEL_ADDR"); v != "" {
		c.Network.PanelAddr = v
	}
	if v := os.Getenv("OPENPANDA_PANEL_TOKEN"); v != "" {
		c.Network.PanelToken = v
	}
	if v := os.Getenv("OPENPANDA_SHARED_SECRET"); v != "" {
		c.Network.SharedSecret = v
	}
	if v := os.Getenv("OPENPANDA_DB_PATH"); v != "" {
		c.Storage.DBPath = v
	}
	if v := os.Getenv("OPENPANDA_MEMORY_PATH"); v != "" {
		c.Storage.MemoryPath = v
	}
	if v := os.Getenv("OPENPANDA_PROJECTS_PATH"); v != "" {
		c.Storage.ProjectsPath = v
	}
	if v := os.Getenv("OPENPANDA_SKILLS_PATH"); v != "" {
		c.Storage.SkillsPath = v
	}
	if v := os.Getenv("OPENPANDA_WORK_PATH"); v != "" {
		c.Storage.WorkPath = v
	}
	if v := os.Getenv("OPENPANDA_ARTIFACT_PATH"); v != "" {
		c.Storage.ArtifactPath = v
	}
	if v := os.Getenv("OPENPANDA_MODEL_API_TYPE"); v != "" {
		c.Model.APIType = v
	}
	if v := os.Getenv("OPENPANDA_MODEL_BASE_URL"); v != "" {
		c.Model.BaseURL = v
	}
	if v := os.Getenv("OPENPANDA_MODEL_API_KEY"); v != "" {
		c.Model.APIKey = v
	}
	if v := os.Getenv("OPENPANDA_MODEL"); v != "" {
		c.Model.Model = v
	}
	if v := os.Getenv("OPENPANDA_MODEL_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Model.MaxTokens = n
		}
	}
	if v := os.Getenv("OPENPANDA_PUSH_VAPID_SUBJECT"); v != "" {
		c.Push.VAPIDSubject = v
	}
	if v := os.Getenv("OPENPANDA_PUSH_VAPID_KEY_PATH"); v != "" {
		c.Push.VAPIDKeyPath = v
	}
	if v := os.Getenv("OPENPANDA_MCP_COMMAND"); v != "" {
		c.MCP.Command = v
	}
}

// UpdateModelSection persists mc's model fields into the YAML file at path,
// creating the file (from defaults) when missing. It round-trips the document
// as a yaml.Node so every other section — and its comments — survives the
// edit byte-for-byte; only the model mapping's values are replaced. Fields mc
// leaves empty-string are dropped from the file so defaults apply again.
func UpdateModelSection(path string, mc ModelConfig) error {
	var root yaml.Node
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
		if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("config %s: not a YAML mapping", path)
		}
	case os.IsNotExist(err):
		doc := Default()
		doc.Model = mc
		out, err := yaml.Marshal(doc)
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0o600)
	default:
		return fmt.Errorf("read config %s: %w", path, err)
	}
	top := root.Content[0]

	// Locate or create the model mapping.
	model := mappingValue(top, "model")
	if model == nil {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "model"}
		model = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		top.Content = append(top.Content, keyNode, model)
	}
	if model.Kind != yaml.MappingNode {
		return fmt.Errorf("config %s: model is not a mapping", path)
	}

	fields := []struct {
		name  string
		value string
	}{
		{"api_type", mc.NormalizedAPIType()},
		{"base_url", mc.BaseURL},
		{"api_key", mc.APIKey},
		{"model", mc.Model},
	}
	for _, f := range fields {
		setMapField(model, f.name, f.value)
	}
	if mc.MaxTokens > 0 {
		setMapField(model, "max_tokens", strconv.Itoa(mc.MaxTokens))
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	hardenSecretPerms(path, out)
	return nil
}

// UpdateMCPSection persists the mcp.command field into the YAML file at
// path, creating the file (from defaults) when missing. It round-trips the
// document like UpdateModelSection so other sections and their comments
// survive byte-for-byte. An empty command removes the field, disabling MCP.
func UpdateMCPSection(path string, command string) error {
	var root yaml.Node
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
		if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("config %s: not a YAML mapping", path)
		}
	case os.IsNotExist(err):
		doc := Default()
		doc.MCP.Command = command
		out, err := yaml.Marshal(doc)
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0o600)
	default:
		return fmt.Errorf("read config %s: %w", path, err)
	}
	top := root.Content[0]

	mcp := mappingValue(top, "mcp")
	if mcp == nil {
		if command == "" {
			return nil // disabling with nothing stored: nothing to write
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mcp"}
		mcp = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		top.Content = append(top.Content, keyNode, mcp)
	}
	if mcp.Kind != yaml.MappingNode {
		return fmt.Errorf("config %s: mcp is not a mapping", path)
	}
	setMapField(mcp, "command", command)

	out, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	hardenSecretPerms(path, out)
	return nil
}

// UpdateNetworkSection persists the network fields a device join changes —
// listen_addr, shared_secret, peers — into the YAML file at path, creating
// the file (from defaults) when missing. It round-trips the document like
// UpdateModelSection so every other section — and its comments — survives the
// edit byte-for-byte.
//
// Zero values mean "leave the stored value alone": a caller that only adds a
// peer passes ListenAddr/SharedSecret empty, and a caller that only sets the
// secret passes a nil Peers. Removing a peer is expressed by passing the full
// remaining list; an empty-but-non-nil list clears the key so defaults apply.
func UpdateNetworkSection(path string, nc NetworkConfig) error {
	var root yaml.Node
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
		if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("config %s: not a YAML mapping", path)
		}
	case os.IsNotExist(err):
		doc := Default()
		doc.Network = nc
		out, err := yaml.Marshal(doc)
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0o600)
	default:
		return fmt.Errorf("read config %s: %w", path, err)
	}
	top := root.Content[0]

	network, err := ensureNetworkMapping(top, path)
	if err != nil {
		return err
	}
	if nc.ListenAddr != "" {
		setMapField(network, "listen_addr", nc.ListenAddr)
	}
	if nc.SharedSecret != "" {
		setMapField(network, "shared_secret", nc.SharedSecret)
	}
	if nc.Peers != nil {
		setMapFieldSeq(network, "peers", nc.Peers)
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	hardenSecretPerms(path, out)
	return nil
}

// ensureNetworkMapping returns the network mapping node, creating the section
// when the config has none yet.
func ensureNetworkMapping(top *yaml.Node, path string) (*yaml.Node, error) {
	if v := mappingValue(top, "network"); v != nil {
		if v.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config %s: network is not a mapping", path)
		}
		return v, nil
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "network"}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	top.Content = append(top.Content, key, v)
	return v, nil
}

// setMapFieldSeq upserts key: [values…] in mapping node m as a block-style
// sequence, preserving order and neighbouring comments.
func setMapFieldSeq(m *yaml.Node, key string, values []string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			if len(values) == 0 {
				m.Content = append(m.Content[:i], m.Content[i+2:]...)
				return
			}
			m.Content[i+1] = stringSeqNode(values)
			return
		}
	}
	if len(values) == 0 {
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		stringSeqNode(values),
	)
}

// stringSeqNode builds a block-style sequence node of plain string scalars.
func stringSeqNode(values []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	return seq
}

// mappingValue returns the value node paired with key in a mapping node, or
// nil when absent.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setMapField upserts key: value (empty value removes the key) in mapping
// node m, preserving order and neighbouring comments.
func setMapField(m *yaml.Node, key, value string) {
	// Drop existing entries for the key first (a duplicate key would be a
	// parse error on reload).
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			if value == "" {
				m.Content = append(m.Content[:i], m.Content[i+2:]...)
				return
			}
			m.Content[i+1].Value = value
			m.Content[i+1].Tag = "!!str"
			return
		}
	}
	if value == "" {
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

// hardenSecretPerms enforces 0600 on a config file that contains secrets
// (P1-19). api_key / shared_secret / panel_token in a world- or
// group-readable file are recoverable by any local user, so the file's
// permission bits are tightened at load time. Prefer the OPENPANDA_* env vars
// (which leave nothing on disk) — a startup warning says so when a secret is
// found in the file. A chmod failure is logged, not fatal: the config is
// still usable, and refusing to boot would lock out existing deployments.
func hardenSecretPerms(path string, data []byte) {
	// Probe only the secret-bearing fields; a YAML anchor or comment cannot
	// forge a non-empty value here.
	var probe struct {
		Network struct {
			SharedSecret string `yaml:"shared_secret"`
			PanelToken   string `yaml:"panel_token"`
		} `yaml:"network"`
		Model struct {
			APIKey string `yaml:"api_key"`
		} `yaml:"model"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return // the real unmarshal already reported any syntax error
	}
	if probe.Network.SharedSecret == "" && probe.Network.PanelToken == "" && probe.Model.APIKey == "" {
		return
	}

	st, err := os.Stat(path)
	if err != nil {
		return
	}
	if st.Mode().Perm()&0o077 == 0 {
		return // already owner-only
	}

	slog.Warn("config file contains secrets; prefer env vars "+
		"(OPENPANDA_SHARED_SECRET / OPENPANDA_PANEL_TOKEN / OPENPANDA_MODEL_API_KEY)", "path", path)

	if err := os.Chmod(path, 0o600); err != nil {
		slog.Warn("config contains secrets but permissions could not be tightened to 0600",
			"path", path, "err", err)
		return
	}
	slog.Warn("tightened config file permissions to 0600 (contains secrets)", "path", path)
}
