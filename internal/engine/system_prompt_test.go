package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noesrafa/sunny/internal/agent"
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

func TestBuildSystemPromptInjectsRuntimeContextByDefault(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x"}, "Soy el agente test.")
	blocks, err := BuildSystemPrompt(a)
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

func TestBuildSystemPromptHonorsAgentOptOut(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x", NoRuntimeContext: true}, "Solo yo.")
	blocks, err := BuildSystemPrompt(a)
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

func TestBuildSystemPromptHonorsEnvOptOut(t *testing.T) {
	t.Setenv("SUNNY_NO_META_PROMPT", "1")
	a := newTestAgent(t, agent.Config{Name: "Test", Model: "x"}, "Hola.")
	blocks, err := BuildSystemPrompt(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		if strings.Contains(b.Text, "Runtime context (injected by sunny)") {
			t.Fatalf("env opt-out failed to skip meta")
		}
	}
}

// TestBuildSystemPromptMetaWithoutPrompt covers the empty-agent edge
// case: no prompt.md, no skills, no knowledge. Default fallback
// "You are X, a personal agent." should still be present, AND the
// meta block should still come first.
func TestBuildSystemPromptMetaWithoutPrompt(t *testing.T) {
	a := newTestAgent(t, agent.Config{Name: "Bare", Model: "x"}, "")
	blocks, err := BuildSystemPrompt(a)
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
