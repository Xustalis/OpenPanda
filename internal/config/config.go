// Package config loads PANDA node configuration from YAML.
package config

import (
	"fmt"
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

// NetworkConfig controls the WebSocket listener and manual peers.
type NetworkConfig struct {
	ListenAddr string   `yaml:"listen_addr"` // e.g. ":7836"
	PanelAddr  string   `yaml:"panel_addr"`  // HTTP panel/PWA listener, e.g. ":7840"
	PanelToken string   `yaml:"panel_token"` // Bearer token guarding /api/*; the panel refuses to start without it
	Peers      []string `yaml:"peers"`       // e.g. "orangepi3b.tailnet-name.ts.net:7836"
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
	APIKey    string `yaml:"api_key"`    // secret; prefer env PANDA_MODEL_API_KEY
	Model     string `yaml:"model"`      // e.g. deepseek-chat | deepseek-reasoner
	MaxTokens int    `yaml:"max_tokens"` // completion cap; 0 = provider/entry default
}

// PushConfig enables Web Push notifications (design P3-26). VAPID (RFC 8292)
// needs a stable P-256 keypair and a mailto:/https: subject identifying the
// sender; the key is persisted at vapid_key_path so browser subscriptions stay
// valid across restarts.
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
			PanelAddr:  ":7840",
		},
		Storage: StorageConfig{
			DBPath:       "./data/panda.db",
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
const DefaultPath = "/etc/panda/config.yaml"

// Load reads the config from path. If path is empty, DefaultPath is used.
// A missing file is not an error; defaults apply. An unreadable or malformed
// file is an error so a bad deployment surfaces loudly.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath
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
	cfg.applyEnv()
	if cfg.Node.Name == "" {
		return nil, fmt.Errorf("config %s: node.name must not be empty", path)
	}
	return cfg, nil
}

// applyEnv lets PANDA_CONFIG_PATH override the file path and individual env
// vars override fields, useful for tests and containerized deploys.
func (c *Config) applyEnv() {
	if v := os.Getenv("PANDA_NODE_NAME"); v != "" {
		c.Node.Name = v
	}
	if v := os.Getenv("PANDA_LISTEN_ADDR"); v != "" {
		c.Network.ListenAddr = v
	}
	if v := os.Getenv("PANDA_PANEL_ADDR"); v != "" {
		c.Network.PanelAddr = v
	}
	if v := os.Getenv("PANDA_PANEL_TOKEN"); v != "" {
		c.Network.PanelToken = v
	}
	if v := os.Getenv("PANDA_DB_PATH"); v != "" {
		c.Storage.DBPath = v
	}
	if v := os.Getenv("PANDA_MEMORY_PATH"); v != "" {
		c.Storage.MemoryPath = v
	}
	if v := os.Getenv("PANDA_PROJECTS_PATH"); v != "" {
		c.Storage.ProjectsPath = v
	}
	if v := os.Getenv("PANDA_SKILLS_PATH"); v != "" {
		c.Storage.SkillsPath = v
	}
	if v := os.Getenv("PANDA_WORK_PATH"); v != "" {
		c.Storage.WorkPath = v
	}
	if v := os.Getenv("PANDA_MODEL_BASE_URL"); v != "" {
		c.Model.BaseURL = v
	}
	if v := os.Getenv("PANDA_MODEL_API_KEY"); v != "" {
		c.Model.APIKey = v
	}
	if v := os.Getenv("PANDA_MODEL"); v != "" {
		c.Model.Model = v
	}
	if v := os.Getenv("PANDA_MODEL_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Model.MaxTokens = n
		}
	}
	if v := os.Getenv("PANDA_PUSH_VAPID_SUBJECT"); v != "" {
		c.Push.VAPIDSubject = v
	}
	if v := os.Getenv("PANDA_PUSH_VAPID_KEY_PATH"); v != "" {
		c.Push.VAPIDKeyPath = v
	}
}
