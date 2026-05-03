// Package skill loads a single skill from a folder containing SKILL.md
// (Claude Code convention: YAML frontmatter + markdown body).
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Frontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	AllowedTools []string `yaml:"allowed-tools,omitempty"`
}

type Skill struct {
	Dir   string
	Front Frontmatter
	Body  string
}

func (s Skill) Validate() error {
	if s.Front.Name == "" {
		return fmt.Errorf("frontmatter: 'name' is required")
	}
	if s.Front.Description == "" {
		return fmt.Errorf("frontmatter: 'description' is required")
	}
	return nil
}

func Load(dir string) (*Skill, error) {
	path := filepath.Join(dir, "SKILL.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	front, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var fm Frontmatter
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter %s: %w", path, err)
	}
	s := &Skill{Dir: dir, Front: fm, Body: body}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// splitFrontmatter expects the file to start with `---` on its own line,
// followed by a YAML block, terminated by another `---` on its own line.
// Returns (frontmatterBytes, bodyString).
func splitFrontmatter(raw []byte) ([]byte, string, error) {
	s := strings.TrimLeft(string(raw), " \t\r\n")
	if !strings.HasPrefix(s, "---") {
		return nil, "", fmt.Errorf("file must begin with '---' frontmatter delimiter")
	}
	s = strings.TrimPrefix(s, "---")
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return nil, "", fmt.Errorf("malformed frontmatter (no newline after opening ---)")
	}
	s = s[nl+1:]
	end := strings.Index(s, "\n---")
	if end < 0 {
		return nil, "", fmt.Errorf("frontmatter not terminated with '---'")
	}
	front := []byte(s[:end])
	after := s[end+len("\n---"):]
	if i := strings.IndexByte(after, '\n'); i >= 0 {
		after = after[i+1:]
	} else {
		after = ""
	}
	return front, after, nil
}
