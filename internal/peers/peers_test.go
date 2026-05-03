package peers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_NoFile(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir, "127.0.0.1:7777", "tok")
	if err != nil {
		t.Fatalf("Load with no file should be ok, got %v", err)
	}
	if r.Local.Name != "local" || r.Local.URL != "http://127.0.0.1:7777" {
		t.Errorf("local = %+v", r.Local)
	}
	if len(r.Remote) != 0 {
		t.Errorf("remote should be empty, got %v", r.Remote)
	}
	if all := r.All(); len(all) != 1 || all[0].Name != "local" {
		t.Errorf("All should include local only, got %v", all)
	}
}

func TestLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	want := []Peer{
		{Name: "vps", URL: "http://100.64.0.5:7777", Token: "abc"},
		{Name: "pi", URL: "http://192.168.1.50:7777", Token: "xyz"},
	}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stat, _ := os.Stat(filepath.Join(dir, FileName))
	if stat.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600 (tokens are sensitive)", stat.Mode().Perm())
	}
	r, err := Load(dir, "127.0.0.1:7777", "tok")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Save sorts by name, so pi comes before vps.
	if len(r.Remote) != 2 || r.Remote[0].Name != "pi" || r.Remote[1].Name != "vps" {
		t.Errorf("remote = %+v, want sorted [pi, vps]", r.Remote)
	}
}

func TestByName(t *testing.T) {
	r := Roster{
		Local:  Peer{Name: "local", URL: "http://127.0.0.1:7777", Token: "t"},
		Remote: []Peer{{Name: "vps", URL: "http://x:7777", Token: "u"}},
	}
	if p, ok := r.ByName("local"); !ok || p.Name != "local" {
		t.Errorf("local lookup failed")
	}
	if p, ok := r.ByName(""); !ok || p.Name != "local" {
		t.Errorf("empty name should resolve to local, got %v / %v", p, ok)
	}
	if p, ok := r.ByName("vps"); !ok || p.URL != "http://x:7777" {
		t.Errorf("vps lookup got %+v / %v", p, ok)
	}
	if _, ok := r.ByName("missing"); ok {
		t.Errorf("missing peer should return false")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		peers   []Peer
		wantErr bool
	}{
		{"empty list", nil, false},
		{"valid", []Peer{{Name: "vps", URL: "http://x:7777", Token: "t"}}, false},
		{"missing name", []Peer{{URL: "http://x:7777", Token: "t"}}, true},
		{"bad name", []Peer{{Name: "VPS", URL: "http://x:7777", Token: "t"}}, true},
		{"reserved name", []Peer{{Name: "local", URL: "http://x:7777", Token: "t"}}, true},
		{"duplicate", []Peer{
			{Name: "vps", URL: "http://x:7777", Token: "t"},
			{Name: "vps", URL: "http://y:7777", Token: "u"},
		}, true},
		{"missing url", []Peer{{Name: "vps", Token: "t"}}, true},
		{"malformed url", []Peer{{Name: "vps", URL: "not a url", Token: "t"}}, true},
		{"missing token", []Peer{{Name: "vps", URL: "http://x:7777"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validate(c.peers)
			if (err != nil) != c.wantErr {
				t.Errorf("validate err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}
