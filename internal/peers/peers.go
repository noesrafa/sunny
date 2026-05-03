// Package peers loads the federation roster: a YAML list of remote
// sunny daemons the local TUI should also talk to.
//
// File location: ~/.sunny/peers.yaml. Optional; when absent the local
// daemon is the only peer.
//
// Shape:
//
//	- name: vps
//	  url: http://100.64.0.5:7777
//	  token: <bearer>
//	- name: pi
//	  url: http://192.168.1.50:7777
//	  token: <bearer>
//
// The local daemon is always implicit as `name: local` and never
// appears in the file. Names must be unique among all peers
// (including "local"), match [a-z0-9][a-z0-9-]*, and stay short — they
// surface in the TUI as the prefix on agent rows.
package peers

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LocalName is the reserved name for the daemon running on the same
// host as the TUI. Shows up in the agent picker as "local/<slug>".
const LocalName = "local"

// FileName is the basename inside the runtime root.
const FileName = "peers.yaml"

// validName mirrors the agent slug regex: lowercase alphanum + dash.
// Keeps the "<peer>/<slug>" rendered form unambiguous to parse back.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Peer is one remote daemon entry.
type Peer struct {
	Name  string `yaml:"name"`
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// Roster is the full peer list, including the implicit local entry.
// Use Load to construct one.
type Roster struct {
	Local  Peer
	Remote []Peer
}

// All returns every peer with local first. Useful when you want to
// iterate over the whole federation in display order.
func (r Roster) All() []Peer {
	out := make([]Peer, 0, 1+len(r.Remote))
	out = append(out, r.Local)
	out = append(out, r.Remote...)
	return out
}

// ByName returns the peer with the given name, or false. Names are
// case-sensitive (we normalize on write, not read, to keep the file
// the human-edit source of truth).
func (r Roster) ByName(name string) (Peer, bool) {
	if name == LocalName || name == "" {
		return r.Local, true
	}
	for _, p := range r.Remote {
		if p.Name == name {
			return p, true
		}
	}
	return Peer{}, false
}

// Load reads peers.yaml from the runtime root and returns the
// roster. A missing file is not an error — you get a roster with
// only the local peer. localAddr/localToken populate the implicit
// entry.
func Load(root, localAddr, localToken string) (Roster, error) {
	r := Roster{
		Local: Peer{Name: LocalName, URL: "http://" + localAddr, Token: localToken},
	}
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return r, fmt.Errorf("peers: read %s: %w", FileName, err)
	}
	var raw []Peer
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return r, fmt.Errorf("peers: parse %s: %w", FileName, err)
	}
	for i := range raw {
		raw[i].Name = strings.TrimSpace(raw[i].Name)
		raw[i].URL = strings.TrimSpace(raw[i].URL)
		raw[i].Token = strings.TrimSpace(raw[i].Token)
	}
	if err := validate(raw); err != nil {
		return r, err
	}
	r.Remote = raw
	return r, nil
}

// Save rewrites peers.yaml with the given remote list. The local
// entry is implicit and never written. Sorts by name for stability.
func Save(root string, remote []Peer) error {
	cp := append([]Peer(nil), remote...)
	for i := range cp {
		cp[i].Name = strings.TrimSpace(cp[i].Name)
		cp[i].URL = strings.TrimSpace(cp[i].URL)
		cp[i].Token = strings.TrimSpace(cp[i].Token)
	}
	if err := validate(cp); err != nil {
		return err
	}
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name < cp[j].Name })

	out, err := yaml.Marshal(cp)
	if err != nil {
		return fmt.Errorf("peers: marshal: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("peers: mkdir: %w", err)
	}
	path := filepath.Join(root, FileName)
	// Mode 0600 because tokens live in here.
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("peers: write %s: %w", FileName, err)
	}
	return nil
}

// validate enforces invariants on a remote slice: unique names, no
// collision with `local`, well-formed URL, non-empty token.
func validate(remote []Peer) error {
	seen := map[string]bool{LocalName: true}
	for _, p := range remote {
		if p.Name == "" {
			return fmt.Errorf("peers: every entry needs a name")
		}
		if !validName.MatchString(p.Name) {
			return fmt.Errorf("peers: %q is not a valid name (lowercase alphanum + dash, starts with letter/digit)", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("peers: duplicate name %q (or collides with reserved %q)", p.Name, LocalName)
		}
		seen[p.Name] = true
		if p.URL == "" {
			return fmt.Errorf("peers: %s: url required", p.Name)
		}
		u, err := url.Parse(p.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("peers: %s: malformed url %q", p.Name, p.URL)
		}
		if p.Token == "" {
			return fmt.Errorf("peers: %s: token required", p.Name)
		}
	}
	return nil
}
