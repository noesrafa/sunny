// Package agent loads, validates, and writes an agent's `agent.yaml`.
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// ID is the opaque, immutable identity of the agent. Used everywhere
	// on disk, on the wire, in journal references. Generated via NewID
	// at creation time and never changes — even if the user renames the
	// agent, the ID stays. Format: "agt_<unix_ms>_<8hex>" (or any string
	// matching ValidID for handcrafted agents and the seeded default).
	ID          string `yaml:"id"`
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

// idRe accepts opaque agt_… IDs plus the legacy lowercase-alnum-dash
// shape so the seeded default ("sunny") and any hand-authored
// agent.yaml stays valid. Underscore is allowed for the agt_ prefix.
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidID reports whether s is a well-formed agent id.
func ValidID(s string) bool { return s != "" && idRe.MatchString(s) }

// NewID returns a sortable, opaque agent id of the shape
// agt_<unix_ms>_<8hex>. Same shape as conv_… and tab_… so the family
// reads consistently on disk.
func NewID() (string, error) {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return fmt.Sprintf("agt_%013d_%s", time.Now().UnixMilli(), hex.EncodeToString(rnd[:])), nil
}

func (c Config) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("agent.yaml: id is required")
	}
	if !ValidID(c.ID) {
		return fmt.Errorf("agent.yaml: invalid id %q (allowed: lowercase alnum, dash, underscore, starting with alnum)", c.ID)
	}
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
	// Backfill: if an agent.yaml predates the id field, derive it from
	// the directory name. The store's loader is the only caller and
	// will overwrite the file on the next mutation, so this is a
	// transparent forward-migration with zero user touch.
	if c.ID == "" {
		c.ID = filepath.Base(dir)
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
