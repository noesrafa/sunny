// Package agent loads, validates, and writes an agent's `agent.yaml`.
package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Model       string `yaml:"model"`
	// Provider is optional. When set ("anthropic", "claude-code",
	// "ollama", …) it overrides the daemon's default provider for
	// turns against this agent. Empty falls back to the default.
	Provider string `yaml:"provider,omitempty"`
}

func (c Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("agent.yaml: name is required")
	}
	if c.Model == "" {
		return fmt.Errorf("agent.yaml: model is required")
	}
	return nil
}

func LoadConfig(dir string) (*Config, error) {
	path := filepath.Join(dir, "agent.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// SaveConfig writes c to dir/agent.yaml after validating. Atomic-ish via
// .tmp + rename so a crash mid-write doesn't leave a half-written file.
func SaveConfig(dir string, c *Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal agent.yaml: %w", err)
	}
	target := filepath.Join(dir, "agent.yaml")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write agent.yaml: %w", err)
	}
	return os.Rename(tmp, target)
}
