package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noesrafa/sunny/internal/agent"
	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/store"
)

// newTestAgent writes a minimal agent dir under t.TempDir and returns
// the loaded *store.Agent. The agent has a prompt.md so we can assert
// the meta block ends up BEFORE the agent's own prompt.
func newTestAgent(t *testing.T, cfg agent.Config, prompt string) *store.Agent {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "agents", "test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"),
		[]byte("name: "+cfg.Name+"\nmodel: "+cfg.Model+"\n"+
			optBool("no_runtime_context", cfg.NoRuntimeContext)),
		0o644); err != nil {
		t.Fatal(err)
	}
	if prompt != "" {
		if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(prompt), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := st.Agent("test")
	if !ok {
		t.Fatal("agent test not loaded")
	}
	return a
}

func optBool(key string, v bool) string {
	if !v {
		return ""
	}
	return key + ": true\n"
}

func TestBuildInjectsRuntimeContextByDefault(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x"}, "Soy el agente test.")
	blocks, err := Build(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) < 2 {
		t.Fatalf("expected >=2 blocks (meta + prompt), got %d", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "Runtime context (injected by sunny)") {
		t.Errorf("first block does not look like meta-prompt:\n%s", blocks[0].Text)
	}
	if !strings.Contains(blocks[0].Text, "Do NOT run") {
		t.Errorf("meta-prompt missing the safety warning")
	}
	if blocks[1].Text != "Soy el agente test." {
		t.Errorf("second block should be agent prompt, got: %q", blocks[1].Text)
	}
	// Cache breakpoint must be on the LAST block, not the meta.
	if blocks[0].CacheControl {
		t.Errorf("meta block should NOT carry cache_control")
	}
	if !blocks[len(blocks)-1].CacheControl {
		t.Errorf("last block should carry cache_control")
	}
}

func TestBuildHonorsAgentOptOut(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x", NoRuntimeContext: true}, "Solo yo.")
	blocks, err := Build(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		if strings.Contains(b.Text, "Runtime context (injected by sunny)") {
			t.Fatalf("opted-out agent still got the meta block")
		}
	}
	if len(blocks) != 1 || blocks[0].Text != "Solo yo." {
		t.Errorf("expected single agent-prompt block, got %d blocks", len(blocks))
	}
}

func TestBuildHonorsEnvOptOut(t *testing.T) {
	t.Setenv("SUNNY_NO_META_PROMPT", "1")
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x"}, "Hola.")
	blocks, err := Build(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		if strings.Contains(b.Text, "Runtime context (injected by sunny)") {
			t.Fatalf("env opt-out failed to skip meta")
		}
	}
}

// TestBuildMetaWithoutPrompt covers the empty-agent edge case: no
// prompt.md, no skills, no knowledge. Default fallback "You are X, a
// personal agent." should still be present, AND the meta block should
// still come first.
func TestBuildMetaWithoutPrompt(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Bare", Model: "x"}, "")
	blocks, err := Build(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected meta + fallback (2 blocks), got %d", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "Runtime context") {
		t.Errorf("first block should be meta")
	}
	if !strings.Contains(blocks[1].Text, "Bare") {
		t.Errorf("second block should be the fallback")
	}
}

// TestBuildMetaCarriesAgentPaths verifies the meta-prompt includes
// the agent's absolute skills/knowledge paths and the priority/
// categorization rules — without this guidance the model has no way
// to find or extend its own skills.
func TestBuildMetaCarriesAgentPaths(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x"}, "Soy.")
	blocks, err := Build(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	meta := blocks[0].Text
	for _, want := range []string{
		a.Dir + "/skills/<category>/<name>/SKILL.md",
		a.Dir + "/knowledge/<category>/<file>.md",
		"Prefer your own skills",
		"place it under",
		"INDEX.md",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("meta-prompt missing %q\n--- meta ---\n%s", want, meta)
		}
	}
}

// TestBuildCatalogListsSkillsAndKnowledge: an agent with a
// categorized skill and a categorized knowledge file should get a
// catalog block enumerating both — name + description for skills,
// relative path for knowledge.
func TestBuildCatalogListsSkillsAndKnowledge(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x"}, "Soy.")
	// Seed a skill under skills/general/greet/SKILL.md.
	skillDir := filepath.Join(a.Dir, "skills", "general", "greet")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: greet\ndescription: warm hello\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Seed a knowledge file under knowledge/general/about.md and an
	// INDEX.md whose first body line is the category description.
	knowDir := filepath.Join(a.Dir, "knowledge", "general")
	if err := os.MkdirAll(knowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowDir, "about.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowDir, "INDEX.md"),
		[]byte("# Index\n\nGeneral knowledge bucket.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reload so the store sees the new files.
	a2 := reloadTestAgent(t, a)
	blocks, err := Build(a2, nil)
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(blockTexts(blocks), "\n---BLOCK---\n")
	for _, want := range []string{
		"# Skills available",
		"## general",
		"`greet` — warm hello",
		"# Knowledge available",
		"General knowledge bucket.",
		"general/about.md",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("catalog missing %q\n--- combined ---\n%s", want, combined)
		}
	}
}

// TestBuildMultiFilePersonaOrdersByNumericPrefix: the agent has both
// a prompt.md AND a prompt/ directory with priority-prefixed files.
// Final order is asc by priority; ties broken by filename.
func TestBuildMultiFilePersonaOrdersByNumericPrefix(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Multi", Model: "x"}, "fallback prompt.md")
	dir := filepath.Join(a.Dir, "prompt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"10-soul.md":          "I am soul",
		"30-instructions.md":  "Follow the rules",
		"70-style.md":         "Write tersely",
		"_disabled.md":        "should never appear",
		"no-prefix.md":        "default fifty",
		"not-markdown.txt":    "should be ignored",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	blocks, err := Build(reloadTestAgent(t, a), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Expected persona order (after the runtime-context block at [0]):
	//   10-soul → 30-instructions → no-prefix.md (50) → prompt.md (50) → 70-style
	// Tie-break by name: "no-prefix.md" < "prompt.md" alphabetically.
	personaTexts := []string{}
	for i := 1; i < len(blocks); i++ {
		// Stop at the catalog/env trailing blocks; persona blocks
		// here have no markdown headings starting with `# `.
		if strings.HasPrefix(blocks[i].Text, "# Skills available") ||
			strings.HasPrefix(blocks[i].Text, "# Knowledge available") ||
			strings.HasPrefix(blocks[i].Text, "## Environment") {
			break
		}
		personaTexts = append(personaTexts, blocks[i].Text)
	}
	want := []string{
		"I am soul",
		"Follow the rules",
		"default fifty",
		"fallback prompt.md",
		"Write tersely",
	}
	if len(personaTexts) != len(want) {
		t.Fatalf("expected %d persona blocks, got %d:\n%v", len(want), len(personaTexts), personaTexts)
	}
	for i, w := range want {
		if personaTexts[i] != w {
			t.Errorf("persona[%d]: want %q, got %q", i, w, personaTexts[i])
		}
	}

	// _disabled.md and not-markdown.txt MUST NOT appear anywhere.
	all := strings.Join(blockTexts(blocks), "\n")
	if strings.Contains(all, "should never appear") {
		t.Error("_-prefixed file leaked into prompt")
	}
	if strings.Contains(all, "should be ignored") {
		t.Error("non-md file leaked into prompt")
	}
}

// TestBuildPromptDirOnly: no prompt.md, just a prompt/ dir. Should
// still produce persona blocks and the cache breakpoint should sit
// on the LAST persona block (or the catalog if present).
func TestBuildPromptDirOnly(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "DirOnly", Model: "x"}, "")
	// newTestAgent only writes prompt.md when prompt != ""; with ""
	// no file is created, so prompt/ is the sole source.
	dir := filepath.Join(a.Dir, "prompt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-hi.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	blocks, err := Build(reloadTestAgent(t, a), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Expect: meta + persona (and cache_control on the persona block,
	// since there's no catalog or env block here).
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d: %v", len(blocks), blockTexts(blocks))
	}
	if blocks[1].Text != "hi" {
		t.Errorf("persona block: want %q, got %q", "hi", blocks[1].Text)
	}
	if !blocks[1].CacheControl {
		t.Errorf("cache_control should sit on the last block (the persona)")
	}
}

// TestBuildEnvBlockSitsAfterCacheBreakpoint: when env is non-nil,
// the env block appears LAST and does NOT carry cache_control.
// The previous block (catalog or persona) keeps the breakpoint.
func TestBuildEnvBlockSitsAfterCacheBreakpoint(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x"}, "Hola.")
	env := &Env{
		Now:         time.Date(2026, 5, 7, 16, 42, 0, 0, time.UTC),
		Hostname:    "rafael-mbp",
		Platform:    "macOS (darwin/arm64)",
		LocalIPv4:   "192.168.1.10",
		TailnetIPv4: "100.127.63.124",
		DaemonAddr:  "127.0.0.1:7777",
	}
	blocks, err := Build(a, env)
	if err != nil {
		t.Fatal(err)
	}

	last := blocks[len(blocks)-1]
	if !strings.Contains(last.Text, "## Environment") {
		t.Fatalf("last block should be env, got: %s", last.Text)
	}
	if last.CacheControl {
		t.Errorf("env block must NOT carry cache_control (would invalidate prefix)")
	}

	// The block JUST BEFORE env keeps the breakpoint.
	prev := blocks[len(blocks)-2]
	if !prev.CacheControl {
		t.Errorf("breakpoint should sit on the static block before env")
	}

	// Date is rendered in CDMX. The Now above (16:42 UTC) → 10:42 in
	// America/Mexico_City (UTC-6, no DST that month).
	if !strings.Contains(last.Text, "10:42 CST") {
		t.Errorf("date should render in CDMX (10:42 CST), got: %s", last.Text)
	}
	for _, want := range []string{
		"rafael-mbp",
		"macOS (darwin/arm64)",
		"192.168.1.10",
		"100.127.63.124",
		"127.0.0.1:7777",
	} {
		if !strings.Contains(last.Text, want) {
			t.Errorf("env block missing %q\n--- env ---\n%s", want, last.Text)
		}
	}
}

// TestBuildEnvNilSkipsBlock: env=nil keeps the legacy block list.
func TestBuildEnvNilSkipsBlock(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x"}, "Hola.")
	blocks, err := Build(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		if strings.Contains(b.Text, "## Environment") {
			t.Fatalf("nil env should produce no env block")
		}
	}
}

// TestBuildEnvSkipsEmptyFields: a sparsely-populated env (only Now)
// renders just the Now row, not empty bullets.
func TestBuildEnvSkipsEmptyFields(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "T", Model: "x"}, "Hi.")
	env := &Env{Now: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}
	blocks, err := Build(a, env)
	if err != nil {
		t.Fatal(err)
	}
	last := blocks[len(blocks)-1]
	for _, unwanted := range []string{"Host:", "Platform:", "LAN IPv4:", "Tailnet IPv4:", "Daemon:"} {
		if strings.Contains(last.Text, unwanted) {
			t.Errorf("env block should skip empty %q\n--- env ---\n%s", unwanted, last.Text)
		}
	}
	if !strings.Contains(last.Text, "Now") {
		t.Errorf("env block should always include Now")
	}
}

func blockTexts(blocks []provider.SystemBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.Text)
	}
	return out
}

func reloadTestAgent(t *testing.T, a *store.Agent) *store.Agent {
	t.Helper()
	root := filepath.Dir(filepath.Dir(a.Dir)) // a.Dir = <root>/agents/<id>
	st, err := store.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := st.Agent(a.ID)
	if !ok {
		t.Fatal("agent not loaded after reload")
	}
	return out
}
