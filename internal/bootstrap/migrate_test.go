package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateLayoutFlatToCategorized covers the happy path: an agent
// with two flat skills + one flat knowledge file ends up with a
// `general` category for both, the originals are gone from the top
// level, and a backup exists under .trash/.
func TestMigrateLayoutFlatToCategorized(t *testing.T) {
	root := t.TempDir()
	slug := "alice"
	agentDir := filepath.Join(root, "agents", slug)
	mustWrite(t, filepath.Join(agentDir, "skills", "greet", "SKILL.md"), skillBody("greet"))
	mustWrite(t, filepath.Join(agentDir, "skills", "summarize", "SKILL.md"), skillBody("summarize"))
	mustWrite(t, filepath.Join(agentDir, "knowledge", "about.md"), "hello\n")

	if err := MigrateLayout(root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mustExist(t, filepath.Join(agentDir, "skills", "general", "greet", "SKILL.md"))
	mustExist(t, filepath.Join(agentDir, "skills", "general", "summarize", "SKILL.md"))
	mustNotExist(t, filepath.Join(agentDir, "skills", "greet"))
	mustNotExist(t, filepath.Join(agentDir, "skills", "summarize"))

	mustExist(t, filepath.Join(agentDir, "knowledge", "general", "about.md"))
	mustNotExist(t, filepath.Join(agentDir, "knowledge", "about.md"))

	// Backup exists under .trash and contains at least one of the
	// original skill files.
	matches, _ := filepath.Glob(filepath.Join(root, ".trash", "migrate-*", slug, "skills", "greet", "SKILL.md"))
	if len(matches) != 1 {
		t.Errorf("expected one backup of skills/greet/SKILL.md, got %d", len(matches))
	}
}

// TestMigrateLayoutIdempotent: an already-categorized tree is a
// no-op. No backup directory should be created.
func TestMigrateLayoutIdempotent(t *testing.T) {
	root := t.TempDir()
	slug := "bob"
	agentDir := filepath.Join(root, "agents", slug)
	mustWrite(t, filepath.Join(agentDir, "skills", "general", "greet", "SKILL.md"), skillBody("greet"))
	mustWrite(t, filepath.Join(agentDir, "knowledge", "general", "about.md"), "hello\n")

	if err := MigrateLayout(root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mustExist(t, filepath.Join(agentDir, "skills", "general", "greet", "SKILL.md"))

	if _, err := os.Stat(filepath.Join(root, ".trash")); !os.IsNotExist(err) {
		t.Errorf(".trash should not exist for already-categorized tree (err=%v)", err)
	}
}

// TestMigrateLayoutMixed: an agent with one flat skill AND one
// already-categorized skill should keep the categorized one in
// place and only move the flat one into general.
func TestMigrateLayoutMixed(t *testing.T) {
	root := t.TempDir()
	slug := "carol"
	agentDir := filepath.Join(root, "agents", slug)
	mustWrite(t, filepath.Join(agentDir, "skills", "greet", "SKILL.md"), skillBody("greet"))                  // flat
	mustWrite(t, filepath.Join(agentDir, "skills", "writing", "polish", "SKILL.md"), skillBody("polish"))    // categorized

	if err := MigrateLayout(root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mustExist(t, filepath.Join(agentDir, "skills", "general", "greet", "SKILL.md"))
	mustExist(t, filepath.Join(agentDir, "skills", "writing", "polish", "SKILL.md"))
	mustNotExist(t, filepath.Join(agentDir, "skills", "greet", "SKILL.md"))
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s NOT to exist (err=%v)", path, err)
	}
}

func skillBody(name string) string {
	return "---\nname: " + name + "\ndescription: test skill " + name + "\n---\n\nbody\n"
}
