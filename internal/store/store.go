// Package store walks the runtime directory (typically ~/.sunny) and
// builds an in-memory index of agents, their knowledge files, and their skills.
package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/noesrafa/sunny/internal/agent"
	"github.com/noesrafa/sunny/internal/skill"
)

type Agent struct {
	Slug      string
	Dir       string
	Config    *agent.Config
	Knowledge []KnowledgeFile
	Skills    []*skill.Skill
}

type KnowledgeFile struct {
	Name string // relative path under knowledge/
	Path string // absolute path on disk
}

type Store struct {
	Root   string
	agents map[string]*Agent
}

func (s *Store) Agents() []*Agent {
	out := make([]*Agent, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

func (s *Store) Agent(slug string) (*Agent, bool) {
	a, ok := s.agents[slug]
	return a, ok
}

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
	return &Agent{Slug: slug, Dir: dir, Config: cfg, Knowledge: knowledge, Skills: skills}, nil
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
