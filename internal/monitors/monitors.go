// Package monitors is the daemon-side scheduler for agent-authored
// rule chains. A monitor is a YAML file at ~/.sunny/monitors/<name>.yaml
// containing an interval, a source (where to fetch items from), and
// a list of rules (when/then). Agents create and edit these files
// directly with their write/edit tools — there is no HTTP CRUD; the
// only mutation endpoint is the enable/disable toggle.
//
// Hot reload: each worker re-reads its YAML on every tick (mtime
// cache); a rule change shows up on the next firing. The watchdog
// scans the directory every few seconds for new/removed files.
//
// Extensibility: Source and Action are interfaces — adding a new
// source type or action is one file with the implementation plus a
// `RegisterSource` / `RegisterAction` call in serve.go.
package monitors

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Monitor is the parsed form of one monitors/<name>.yaml.
type Monitor struct {
	Name     string       `yaml:"name"`
	Enabled  bool         `yaml:"enabled"`
	Interval string       `yaml:"interval"` // "60s", "5m", "1h"
	Source   SourceConfig `yaml:"-"`
	Rules    []Rule       `yaml:"rules"`
}

// SourceConfig is the type discriminator + the rest of the source's
// fields. We unmarshal the `source` node manually so source-specific
// keys ride alongside `type` without nesting.
type SourceConfig struct {
	Type   string
	Config map[string]any
}

// Rule is a single when/then pair. When is a condition tree (with
// composers like all/any) and Then is an ordered list of action
// invocations whose results feed into subsequent ones via variable
// substitution.
type Rule struct {
	Name string             `yaml:"name"`
	When map[string]any     `yaml:"when"`
	Then []map[string]any   `yaml:"then"`
}

// IntervalDuration parses Interval into a time.Duration. Defaults
// to 60s on missing/invalid value so a typo doesn't disable the
// monitor; minimum is 1s to keep ticker overhead sane.
func (m *Monitor) IntervalDuration() time.Duration {
	if m.Interval == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(m.Interval)
	if err != nil || d < time.Second {
		return 60 * time.Second
	}
	return d
}

// nameRe restricts file names to filesystem-safe slugs that won't
// collide with the .state/.history subdirs.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Load reads path and parses one monitor. The source node is
// flattened: the `type` field becomes Source.Type and the remaining
// keys go into Source.Config.
func Load(path string) (*Monitor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw struct {
		Name     string                 `yaml:"name"`
		Enabled  bool                   `yaml:"enabled"`
		Interval string                 `yaml:"interval"`
		Source   map[string]any         `yaml:"source"`
		Rules    []Rule                 `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if raw.Name == "" {
		raw.Name = strings.TrimSuffix(filepath.Base(path), ".yaml")
	}
	m := &Monitor{
		Name:     raw.Name,
		Enabled:  raw.Enabled,
		Interval: raw.Interval,
		Rules:    raw.Rules,
	}
	if raw.Source != nil {
		if t, ok := raw.Source["type"].(string); ok {
			m.Source.Type = t
		}
		cfg := map[string]any{}
		for k, v := range raw.Source {
			if k == "type" {
				continue
			}
			cfg[k] = v
		}
		m.Source.Config = cfg
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// SaveEnabled rewrites the YAML in place with a flipped `enabled`
// field. Used by the HTTP toggle endpoint. Other fields are
// preserved verbatim by re-marshalling the parsed map.
func SaveEnabled(path string, enabled bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var node map[string]any
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return err
	}
	if node == nil {
		node = map[string]any{}
	}
	node["enabled"] = enabled
	out, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Monitor) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("name: required")
	}
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("name %q: must match %s", m.Name, nameRe.String())
	}
	if m.Source.Type == "" {
		return fmt.Errorf("source.type: required")
	}
	return nil
}
