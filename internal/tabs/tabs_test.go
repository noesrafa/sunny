package tabs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddPersistsAcrossReload(t *testing.T) {
	root := t.TempDir()
	s, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.Add(&Tab{AgentID: "sunny", ConvID: "conv_x", Title: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID == "" {
		t.Fatal("Add returned tab without ID")
	}
	s2, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	tabs := s2.List()
	if len(tabs) != 1 {
		t.Fatalf("after reload got %d tabs, want 1", len(tabs))
	}
	if tabs[0].ID != stored.ID {
		t.Fatalf("id mismatch: got %s, want %s", tabs[0].ID, stored.ID)
	}
}

func TestRemove(t *testing.T) {
	s, _ := Load(t.TempDir())
	a, _ := s.Add(&Tab{AgentID: "x", ConvID: "c1"})
	b, _ := s.Add(&Tab{AgentID: "x", ConvID: "c2"})
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	tabs := s.List()
	if len(tabs) != 1 || tabs[0].ID != b.ID {
		t.Fatalf("after remove got %+v, want only %s", tabs, b.ID)
	}
	if err := s.Remove("tab_does_not_exist"); err != ErrNotFound {
		t.Fatalf("remove missing: got %v, want ErrNotFound", err)
	}
}

func TestUpdate(t *testing.T) {
	s, _ := Load(t.TempDir())
	a, _ := s.Add(&Tab{AgentID: "x", ConvID: "c1", Title: "old"})
	updated, err := s.Update(a.ID, func(tab *Tab) { tab.Title = "new" })
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "new" {
		t.Fatalf("Update did not apply mutation: %+v", updated)
	}
}

func TestGet(t *testing.T) {
	s, _ := Load(t.TempDir())
	a, _ := s.Add(&Tab{AgentID: "x", ConvID: "c1", Title: "hi"})
	got, err := s.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.AgentID != "x" || got.ConvID != "c1" {
		t.Fatalf("Get returned wrong tab: %+v", got)
	}
	got.Title = "mutated"
	tabs := s.List()
	if tabs[0].Title != "hi" {
		t.Fatal("Get returned a live pointer; should return a copy")
	}
	if _, err := s.Get("tab_missing"); err != ErrNotFound {
		t.Fatalf("Get missing: got %v, want ErrNotFound", err)
	}
}

func TestLoadCorruptFileStartsFresh(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tabs.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(root)
	if err != nil {
		t.Fatalf("expected silent fallback, got %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("expected empty list after corrupt load, got %d", len(s.List()))
	}
	if _, err := s.Add(&Tab{AgentID: "x"}); err != nil {
		t.Fatal(err)
	}
}
