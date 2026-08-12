// Package config loads PANDA node configuration from YAML.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level node configuration.
type Config struct {
	Node    NodeConfig    `yaml:"node"`
	Network NetworkConfig `yaml:"network"`
	Storage StorageConfig `yaml:"storage"`
	Log     LogConfig     `yaml:"log"`
}

// NodeConfig identifies this node.
type NodeConfig struct {
	Name          string `yaml:"name"`
	ResourceClass string `yaml:"resource_class"` // Micro | Standard | Full
}

// NetworkConfig controls the WebSocket listener and manual peers.
type NetworkConfig struct {
	ListenAddr string   `yaml:"listen_addr"` // e.g. ":7836"
	Peers      []string `yaml:"peers"`       // e.g. "orangepi3b.tailnet-name.ts.net:7836"
}

// StorageConfig controls local persistence.
type StorageConfig struct {
	DBPath      string `yaml:"db_path"`
	ContextPath string `yaml:"context_path"`
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level string `yaml:"level"` // debug | info | warn | error
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
		},
		Storage: StorageConfig{
			DBPath:      "./data/panda.db",
			ContextPath: "./data/context",
		},
		Log: LogConfig{
			Level: "info",
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
	if v := os.Getenv("PANDA_DB_PATH"); v != "" {
		c.Storage.DBPath = v
	}
}
