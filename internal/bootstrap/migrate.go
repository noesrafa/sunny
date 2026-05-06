package bootstrap

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MigrateLayout converts the pre-categories layout (flat
// skills/<name>/SKILL.md and flat knowledge/*.md) to the categorized
// form: skills/<category>/<name>/SKILL.md and
// knowledge/<category>/<file>.md. Flat entries are moved into a
// "general" category.
//
// Idempotent: a daemon whose tree is already categorized has nothing
// to do and the call is a cheap directory walk. Per-agent backups
// land at root/.trash/migrate-<ts>/<id>/ before the move so the
// operation is reversible.
func MigrateLayout(root string) error {
	agentsRoot := filepath.Join(root, "agents")
	entries, err := os.ReadDir(agentsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	backupRoot := filepath.Join(root, ".trash", "migrate-"+stamp)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		agentDir := filepath.Join(agentsRoot, e.Name())
		agentBackup := filepath.Join(backupRoot, e.Name())
		if err := migrateAgentSkills(agentDir, agentBackup); err != nil {
			return fmt.Errorf("migrate skills %s: %w", e.Name(), err)
		}
		if err := migrateAgentKnowledge(agentDir, agentBackup); err != nil {
			return fmt.Errorf("migrate knowledge %s: %w", e.Name(), err)
		}
	}
	return nil
}

// migrateAgentSkills detects flat skill dirs (those containing
// SKILL.md directly) and moves each into skills/general/. Category
// dirs are recognized by NOT containing a SKILL.md at their root.
func migrateAgentSkills(agentDir, backupBase string) error {
	skillsDir := filepath.Join(agentDir, "skills")
	info, err := os.Stat(skillsDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	children, err := os.ReadDir(skillsDir)
	if err != nil {
		return err
	}
	var flat []string
	for _, c := range children {
		if !c.IsDir() || strings.HasPrefix(c.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(skillsDir, c.Name(), "SKILL.md")); err == nil {
			flat = append(flat, c.Name())
		}
	}
	if len(flat) == 0 {
		return nil
	}
	if err := copyDir(skillsDir, filepath.Join(backupBase, "skills")); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	generalDir := filepath.Join(skillsDir, "general")
	if err := os.MkdirAll(generalDir, 0o755); err != nil {
		return err
	}
	for _, name := range flat {
		src := filepath.Join(skillsDir, name)
		dst := filepath.Join(generalDir, name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %s: %w", name, err)
		}
	}
	return nil
}

// migrateAgentKnowledge detects flat .md files at the top of
// knowledge/ and moves them into knowledge/general/. Existing
// subdirectories of knowledge/ are assumed to be categories already
// and are left alone.
func migrateAgentKnowledge(agentDir, backupBase string) error {
	knowledgeDir := filepath.Join(agentDir, "knowledge")
	info, err := os.Stat(knowledgeDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	children, err := os.ReadDir(knowledgeDir)
	if err != nil {
		return err
	}
	var flat []string
	for _, c := range children {
		if c.IsDir() || strings.HasPrefix(c.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(c.Name()), ".md") {
			continue
		}
		flat = append(flat, c.Name())
	}
	if len(flat) == 0 {
		return nil
	}
	if err := copyDir(knowledgeDir, filepath.Join(backupBase, "knowledge")); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	generalDir := filepath.Join(knowledgeDir, "general")
	if err := os.MkdirAll(generalDir, 0o755); err != nil {
		return err
	}
	for _, name := range flat {
		src := filepath.Join(knowledgeDir, name)
		dst := filepath.Join(generalDir, name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %s: %w", name, err)
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rErr := filepath.Rel(src, path)
		if rErr != nil {
			return rErr
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rErr := os.ReadFile(path)
		if rErr != nil {
			return rErr
		}
		return os.WriteFile(target, data, 0o644)
	})
}
