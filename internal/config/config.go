package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// TunnelConfig defines a single tunnel's properties.
type TunnelConfig struct {
	Type   string `json:"type"`   // "web" or "mesh"
	Target string `json:"target"` // e.g. "localhost:8080" or "localhost:25565"
}

// AppConfig is the root configuration structure loaded from btunnel.json.
type AppConfig struct {
	SignalURL string                  `json:"signal,omitempty"` // Override default signaling server URL
	Tunnels   map[string]TunnelConfig `json:"tunnels"`          // Set of named tunnels to share
}

// LoadConfig reads and parses the JSON configuration file from the specified path.
func LoadConfig(path string) (*AppConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	var cfg AppConfig
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode JSON config: %w", err)
	}

	// Basic validation
	if len(cfg.Tunnels) == 0 {
		return nil, fmt.Errorf("configuration contains no tunnels to run")
	}

	for name, t := range cfg.Tunnels {
		if t.Type != "web" && t.Type != "mesh" {
			return nil, fmt.Errorf("invalid type '%s' for tunnel '%s', must be 'web' or 'mesh'", t.Type, name)
		}
		if t.Target == "" {
			return nil, fmt.Errorf("target address cannot be empty for tunnel '%s'", name)
		}
	}

	return &cfg, nil
}
