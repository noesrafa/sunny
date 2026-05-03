// Package store walks the runtime directory (typically ~/.sunny) and
// builds an in-memory index of agents, their knowledge files, and their skills.
//
// CRUD: Create, Update, Delete mutate both the in-memory map and the
// filesystem so callers (HTTP handlers) only need to call one method.
// Operations are serialized through an RWMutex.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/noesrafa/sunny/internal/agent"
	"github.com/noesrafa/sunny/internal/skill"
)

// ErrNotFound is returned by mutators when no agent matches the given slug.
var ErrNotFound = errors.New("agent not found")

// ErrConflict is returned by Create when the slug is already taken.
var ErrConflict = errors.New("agent already exists")

type Agent struct {
	Slug      string
	Dir       string
	Config    *agent.Config
	Knowledge []KnowledgeFile
	Skills    []*skill.Skill
	// Prompt is the contents of prompt.md (the agent's persona). Loaded
	// once at boot; CRUD keeps it in sync.
	Prompt string
}

type KnowledgeFile struct {
	Name string // relative path under knowledge/
	Path string // absolute path on disk
}

type Store struct {
	Root   string
	mu     sync.RWMutex
	agents map[string]*Agent
}

func (s *Store) Agents() []*Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Agent, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

func (s *Store) Agent(slug string) (*Agent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[slug]
	return a, ok
}

// Create scaffolds a new agent on disk and registers it in the index.
// Slug must be unique and match [a-z0-9][a-z0-9-]*. Description and
// prompt are optional. Returns the freshly-loaded *Agent.
func (s *Store) Create(slug string, cfg agent.Config, prompt string) (*Agent, error) {
	if !validSlug(slug) {
		return nil, fmt.Errorf("invalid slug %q (allowed: lowercase alnum + dash, starting with alnum)", slug)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.agents[slug]; exists {
		return nil, fmt.Errorf("%w: %s", ErrConflict, slug)
	}

	dir := filepath.Join(s.Root, "agents", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir agent dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "knowledge"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir knowledge: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir skills: %w", err)
	}
	if err := agent.SaveConfig(dir, &cfg); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(prompt), 0o644); err != nil {
		return nil, fmt.Errorf("write prompt.md: %w", err)
	}

	a, err := loadAgent(dir, slug)
	if err != nil {
		return nil, fmt.Errorf("reload after create: %w", err)
	}
	s.agents[slug] = a
	return a, nil
}

// Update applies field-level changes to an agent. Only non-nil pointers
// in patch take effect — the rest are left untouched. Returns the
// post-update *Agent.
type AgentPatch struct {
	Name        *string
	Description *string
	Model       *string
	Prompt      *string
}

func (s *Store) Update(slug string, patch AgentPatch) (*Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.agents[slug]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	cfg := *cur.Config
	if patch.Name != nil {
		cfg.Name = *patch.Name
	}
	if patch.Description != nil {
		cfg.Description = *patch.Description
	}
	if patch.Model != nil {
		cfg.Model = *patch.Model
	}
	if patch.Name != nil || patch.Description != nil || patch.Model != nil {
		if err := agent.SaveConfig(cur.Dir, &cfg); err != nil {
			return nil, err
		}
	}
	if patch.Prompt != nil {
		if err := os.WriteFile(filepath.Join(cur.Dir, "prompt.md"), []byte(*patch.Prompt), 0o644); err != nil {
			return nil, fmt.Errorf("write prompt.md: %w", err)
		}
	}
	a, err := loadAgent(cur.Dir, slug)
	if err != nil {
		return nil, fmt.Errorf("reload after update: %w", err)
	}
	s.agents[slug] = a
	return a, nil
}

// Delete archives an agent: moves its directory (including conversations
// and skills) to ~/.sunny/.archive/. Idempotent — missing slug is not an
// error. Restoration is manual: move the timestamped folder back under
// ~/.sunny/agents/ and reload the daemon.
func (s *Store) Delete(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.agents[slug]
	if !ok {
		return nil
	}
	archiveDir := filepath.Join(s.Root, ".archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	target := filepath.Join(archiveDir, fmt.Sprintf("agent__%s__%s", slug, stamp))
	if err := os.Rename(cur.Dir, target); err != nil {
		return fmt.Errorf("move to archive: %w", err)
	}
	delete(s.agents, slug)
	return nil
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func validSlug(s string) bool { return s != "" && slugRe.MatchString(s) }

func Load(root string) (*Store, error) {
	agentsRoot := filepath.Join(root, "agents")
	info, err := os.Stat(agentsRoot)
	if err != nil {
		return nil, fmt.Errorf("agents dir at %s: %w", agentsRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", agentsRoot)
	}
	entries, err := os.ReadDir(agentsRoot)
	if err != nil {
		return nil, err
	}
	s := &Store{Root: root, agents: map[string]*Agent{}}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		a, err := loadAgent(filepath.Join(agentsRoot, e.Name()), e.Name())
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", e.Name(), err)
		}
		s.agents[a.Slug] = a
	}
	return s, nil
}

func loadAgent(dir, slug string) (*Agent, error) {
	cfg, err := agent.LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	knowledge, err := loadKnowledge(filepath.Join(dir, "knowledge"))
	if err != nil {
		return nil, err
	}
	skills, err := loadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		return nil, err
	}
	prompt, err := loadPrompt(dir)
	if err != nil {
		return nil, err
	}
	return &Agent{Slug: slug, Dir: dir, Config: cfg, Knowledge: knowledge, Skills: skills, Prompt: prompt}, nil
}

// loadPrompt reads prompt.md from the agent dir. Returns "" (not error)
// when the file is missing — agents without a persona still load.
func loadPrompt(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "prompt.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func loadKnowledge(dir string) ([]KnowledgeFile, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var files []KnowledgeFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		files = append(files, KnowledgeFile{Name: filepath.ToSlash(rel), Path: path})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func loadSkills(dir string) ([]*skill.Skill, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var skills []*skill.Skill
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sk, err := skill.Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		skills = append(skills, sk)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Front.Name < skills[j].Front.Name })
	return skills, nil
}
