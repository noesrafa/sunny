// Package tsnet wraps the local `tailscale` CLI just enough for
// sunny to discover the tailnet without taking on libtailscale as
// a dependency. We shell out because:
//
//   - libtailscale (`tailscale.com/...`) is several megabytes of
//     transitive deps for what amounts to two read-only commands.
//   - The CLI's stdout is stable across versions; the Go module is
//     not.
//   - Users who don't run Tailscale don't pay for the dep.
//
// All operations are best-effort: `Available()` is the right guard
// for "should we even try?" — when it returns false, the rest of
// the package's calls return errors the caller is expected to
// downgrade to "Tailscale not configured" UX.
package tsnet

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Peer is one tailnet node.
type Peer struct {
	HostName string
	IP       string // first IPv4 (the v6 is rarely useful for sunny's HTTP daemon)
	OS       string // "linux" / "macOS" / "iOS" / …
	Online   bool
	IsSelf   bool
	// UserID is the tailscale account that owns this node. Two nodes
	// with the same UserID belong to the same person — sunny uses
	// this for zero-config trust ("are you me?"). 0 means unknown
	// (stale tailscale CLI, missing field, etc.).
	UserID int64
}

// Status is the parsed result of `tailscale status --json`. Self is
// the local node; Peers is everyone else on the tailnet (no Self
// duplicate).
type Status struct {
	Self  Peer
	Peers []Peer
}

// SameUser reports whether the given IP belongs to a peer in the
// same tailscale account as Self. Non-tailnet IPs return false.
// 0 UserID on either side returns false (defensive — never trust
// when identity is unknown).
func (s Status) SameUser(ip string) bool {
	if s.Self.UserID == 0 {
		return false
	}
	if s.Self.IP == ip {
		return true
	}
	for _, p := range s.Peers {
		if p.IP == ip {
			return p.UserID != 0 && p.UserID == s.Self.UserID
		}
	}
	return false
}

// Available reports whether the `tailscale` CLI is installed.
// Cheap PATH lookup; no exec. Most callers only need this guard
// before deciding whether to surface tailnet UX at all.
func Available() bool {
	_, err := exec.LookPath("tailscale")
	return err == nil
}

// LocalIP runs `tailscale ip -4` and returns the local node's first
// IPv4 on the tailnet. Returns ("", error) when:
//   - the CLI isn't installed (errors.Is would match exec.ErrNotFound)
//   - the user isn't logged in (`tailscale ip` fails with a friendly
//     stderr we surface verbatim)
//   - the timeout (3s) fires, very rare on local IPC
func LocalIP() (string, error) {
	if !Available() {
		return "", fmt.Errorf("tsnet: `tailscale` CLI not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tsnet: tailscale ip: %s", strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", fmt.Errorf("tsnet: tailscale ip: empty output")
}

// FetchStatus runs `tailscale status --json` and returns the parsed
// result with Self separated from peers. Preferred over Peers()
// when you need identity (UserID) — Peers is a convenience that
// drops the Self/peers distinction.
func FetchStatus() (*Status, error) {
	raw, err := runStatus()
	if err != nil {
		return nil, err
	}
	return parseStatusFull(raw)
}

// Peers runs `tailscale status --json` and returns every reachable
// node on the tailnet, including Self. Sunny callers typically
// filter Self out; we leave it in so the caller can render the
// local node consistently with remotes.
func Peers() ([]Peer, error) {
	st, err := FetchStatus()
	if err != nil {
		return nil, err
	}
	out := []Peer{st.Self}
	out = append(out, st.Peers...)
	return out, nil
}

// runStatus is the shared exec wrapper.
func runStatus() ([]byte, error) {
	if !Available() {
		return nil, fmt.Errorf("tsnet: `tailscale` CLI not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("tsnet: tailscale status: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("tsnet: tailscale status: %w", err)
	}
	return out, nil
}

// rawStatus mirrors the subset of `tailscale status --json` we
// actually consume. The schema is huge; mapping only what we read
// keeps us decoupled from the bits we don't.
type rawStatus struct {
	Self *rawNode            `json:"Self"`
	Peer map[string]*rawNode `json:"Peer"`
}

type rawNode struct {
	HostName     string   `json:"HostName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	OS           string   `json:"OS"`
	Online       bool     `json:"Online"`
	UserID       int64    `json:"UserID"`
}

// parseStatusFull is the canonical parser; parseStatus is kept as a
// thin wrapper for the older (Self-included) shape used by Peers().
func parseStatusFull(raw []byte) (*Status, error) {
	var s rawStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("tsnet: parse status: %w", err)
	}
	out := &Status{}
	if s.Self != nil {
		out.Self = toPeer(s.Self, true)
	}
	for _, n := range s.Peer {
		out.Peers = append(out.Peers, toPeer(n, false))
	}
	return out, nil
}

// parseStatus is the legacy shape (Self folded into the slice).
// Kept so existing tests still pass without rewriting them.
func parseStatus(raw []byte) ([]Peer, error) {
	st, err := parseStatusFull(raw)
	if err != nil {
		return nil, err
	}
	out := []Peer{st.Self}
	out = append(out, st.Peers...)
	return out, nil
}

func toPeer(n *rawNode, self bool) Peer {
	return Peer{
		HostName: n.HostName,
		IP:       firstIPv4(n.TailscaleIPs),
		OS:       n.OS,
		Online:   n.Online,
		IsSelf:   self,
		UserID:   n.UserID,
	}
}

// firstIPv4 picks the first dotted-quad from the slice. tailscale
// always lists v4 before v6 in our experience, but we scan
// defensively.
func firstIPv4(ips []string) string {
	for _, ip := range ips {
		if strings.Count(ip, ".") == 3 && !strings.Contains(ip, ":") {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}
