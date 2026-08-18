// Package config loads PANDA node configuration from YAML.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config is the top-level node configuration.
type Config struct {
	Node    NodeConfig    `yaml:"node"`
	Network NetworkConfig `yaml:"network"`
	Storage StorageConfig `yaml:"storage"`
	Log     LogConfig     `yaml:"log"`
	Model   ModelConfig   `yaml:"model"`
	Push    PushConfig    `yaml:"push"`
	MCP     MCPConfig     `yaml:"mcp"`
}

// NodeConfig identifies this node.
type NodeConfig struct {
	Name          string `yaml:"name"`
	ResourceClass string `yaml:"resource_class"` // Micro | Standard | Full
}

// NetworkConfig controls the WebSocket listener and manual peers. PanelAddr and
// PanelToken are read only by the optional webui sidecar (webui/cmd/panel), not
// by the kernel daemon.
type NetworkConfig struct {
	ListenAddr          string   `yaml:"listen_addr"`            // e.g. ":7836"
	PanelAddr           string   `yaml:"panel_addr"`             // webui sidecar HTTP listener; loopback by default (P1-24)
	PanelToken          string   `yaml:"panel_token"`            // Bearer token guarding /api/* in the webui sidecar
	SharedSecret        string   `yaml:"shared_secret"`          // HMAC secret authenticating node-to-node hellos; the WS listener refuses to start without it
	Peers               []string `yaml:"peers"`                  // e.g. "orangepi3b.tailnet-name.ts.net:7836"
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
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level string `yaml:"level"` // debug | info | warn | error
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

// Validate reports statically invalid configuration before the node starts:
// an unknown resource_class (a typo silently downgrades the scheduler tier),
// malformed peer addresses (must be host:port), or a listen/panel address that
// is not host:port. Reachability is NOT checked here — peers may come and go
// at runtime; the daemon's MaintainPeer loop already warns on dial failures.
func (c *Config) Validate() error {
	if !validResourceClasses[c.Node.ResourceClass] {
		return fmt.Errorf("config: node.resource_class %q is invalid (want Micro, Standard, or Full)", c.Node.ResourceClass)
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
	return nil
}

// Default returns a Config with safe local-development defaults.
func Default() *Config {
	return &Config{
		Node: NodeConfig{
			Name:          "macbook",
			ResourceClass: "Standard",
		},
		Network: NetworkConfig{
			ListenAddr: ":7836",
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
			DBPath:       "./data/openpanda.db",
			ContextPath:  "./data/context",
			MemoryPath:   "./memory",
			ProjectsPath: "./projects",
			SkillsPath:   "./skills",
			WorkPath:     ".",
		},
		Log: LogConfig{
			Level: "info",
		},
		Model: ModelConfig{
			BaseURL:   "https://api.deepseek.com/anthropic",
			Model:     "deepseek-chat",
			MaxTokens: 4096,
		},
		Push: PushConfig{
			Enabled:      false,
			VAPIDSubject: "mailto:panda@localhost",
			VAPIDKeyPath: "./data/vapid.pem",
		},
	}
}

// DefaultPath is where the node looks for its config file.
const DefaultPath = "/etc/openpanda/config.yaml"

// Load reads the config from path. If path is empty, the OPENPANDA_CONFIG_PATH env
// var (if set) or DefaultPath is used. A missing file is not an error; defaults
// apply. An unreadable or malformed file is an error so a bad deployment
// surfaces loudly.
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("OPENPANDA_CONFIG_PATH")
		if path == "" {
			path = DefaultPath
		}
	}
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	hardenSecretPerms(path, data)
	cfg.applyEnv()
	if cfg.Node.Name == "" {
		return nil, fmt.Errorf("config %s: node.name must not be empty", path)
	}
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

	slog.Warn("config file contains secrets; prefer env vars "+
		"(OPENPANDA_SHARED_SECRET / OPENPANDA_PANEL_TOKEN / OPENPANDA_MODEL_API_KEY)", "path", path)

	st, err := os.Stat(path)
	if err != nil {
		return
	}
	if st.Mode().Perm()&0o077 == 0 {
		return // already owner-only
	}
	if err := os.Chmod(path, 0o600); err != nil {
		slog.Warn("config contains secrets but permissions could not be tightened to 0600",
			"path", path, "err", err)
		return
	}
	slog.Warn("tightened config file permissions to 0600 (contains secrets)", "path", path)
}
