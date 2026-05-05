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

// Skill is the in-memory handle for a single skill on disk. The
// markdown body is intentionally NOT cached here — progressive
// disclosure means callers load it on demand via LoadBody(s.Dir)
// (or by viewing s.Dir/SKILL.md directly).
type Skill struct {
	Dir      string
	Category string
	Front    Frontmatter
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
	front, _, err := splitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var fm Frontmatter
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter %s: %w", path, err)
	}
	s := &Skill{Dir: dir, Front: fm}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// LoadBody returns just the markdown body of dir/SKILL.md (no
// frontmatter). Used by callers that don't keep the body in memory.
func LoadBody(dir string) (string, error) {
	path := filepath.Join(dir, "SKILL.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	_, body, err := splitFrontmatter(raw)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	return body, nil
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
