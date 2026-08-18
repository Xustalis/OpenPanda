// Package config loads PANDA node configuration from YAML.
package config

import (
	"fmt"
	"log/slog"
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

// ModelConfig selects the LLM provider for the entry model and the agent
// adapters. DeepSeek exposes an Anthropic-compatible Messages API, so base_url
// defaults there; any /v1/messages-compatible endpoint works.
type ModelConfig struct {
	BaseURL   string `yaml:"base_url"`   // e.g. https://api.deepseek.com/anthropic
	APIKey    string `yaml:"api_key"`    // secret; prefer env OPENPANDA_MODEL_API_KEY
	Model     string `yaml:"model"`      // e.g. deepseek-chat | deepseek-reasoner
	MaxTokens int    `yaml:"max_tokens"` // completion cap; 0 = provider/entry default
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
