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
	// Effort drives the provider's reasoning budget — for Anthropic
	// it maps to OutputConfig.Effort (low/medium/high/xhigh/max).
	// Empty falls back to "max" at request build time.
	Effort string `yaml:"effort,omitempty"`
	// Provider is optional. When set ("anthropic", "claude-code",
	// "ollama", …) it overrides the daemon's default provider for
	// turns against this agent. Empty falls back to the default.
	Provider string `yaml:"provider,omitempty"`
	// NoRuntimeContext skips the auto-injected sunny meta-prompt
	// for this agent. Default false (= the meta-prompt IS injected).
	// Set true for purist agents that want a clean prompt window.
	NoRuntimeContext bool `yaml:"no_runtime_context,omitempty"`
}

func (c Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("agent.yaml: name is required")
	}
	if c.Model == "" {
		return fmt.Errorf("agent.yaml: model is required")
	}
	if c.Effort != "" {
		switch c.Effort {
		case "low", "medium", "high", "xhigh", "max":
		default:
			return fmt.Errorf("agent.yaml: invalid effort %q (allowed: low|medium|high|xhigh|max)", c.Effort)
		}
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
