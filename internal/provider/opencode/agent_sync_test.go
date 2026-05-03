package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noesrafa/sunny/internal/provider"
)

func TestSyncAgentFile_WritesAndReuses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(agentDirEnv, dir)

	sys := []provider.SystemBlock{
		{Text: "  You are a terse helper.  "},
		{Text: "Always reply in 5 words or fewer."},
	}

	name, err := syncAgentFile("oc-test", sys)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if name != "sunny-oc-test" {
		t.Fatalf("name = %q, want %q", name, "sunny-oc-test")
	}

	path := filepath.Join(dir, name+".md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"---\n",
		"description: sunny agent oc-test",
		"mode: primary\n",
		"You are a terse helper.\n\nAlways reply in 5 words or fewer.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file missing %q\n--- file ---\n%s", want, got)
		}
	}

	// Second call with identical content must NOT rewrite the file
	// (verified via mtime). Saves us from gratuitous churn during a
	// long conversation.
	stat1, _ := os.Stat(path)
	if _, err := syncAgentFile("oc-test", sys); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	stat2, _ := os.Stat(path)
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("file was rewritten despite identical content")
	}

	// Third call with changed content MUST rewrite.
	sys[0].Text = "You are a verbose helper."
	if _, err := syncAgentFile("oc-test", sys); err != nil {
		t.Fatalf("third sync: %v", err)
	}
	stat3, _ := os.Stat(path)
	if stat3.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("file was NOT rewritten despite changed content")
	}
	body, _ = os.ReadFile(path)
	if !strings.Contains(string(body), "verbose helper") {
		t.Errorf("file did not pick up new prompt: %s", body)
	}
}

func TestSyncAgentFile_RejectsEmptySlug(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(agentDirEnv, dir)
	if _, err := syncAgentFile("", nil); err == nil {
		t.Errorf("expected error for empty slug, got nil")
	}
}

func TestFlattenSystem(t *testing.T) {
	cases := []struct {
		name string
		in   []provider.SystemBlock
		want string
	}{
		{"nil", nil, ""},
		{"one", []provider.SystemBlock{{Text: "  hi  "}}, "hi"},
		{"two", []provider.SystemBlock{{Text: "a"}, {Text: "b"}}, "a\n\nb"},
		{"trims whitespace", []provider.SystemBlock{{Text: "  alpha\n"}, {Text: "\tbeta  "}}, "alpha\n\nbeta"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := flattenSystem(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
