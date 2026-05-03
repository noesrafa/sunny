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

// Peers runs `tailscale status --json` and returns every reachable
// node on the tailnet, including Self. Sunny callers typically
// filter Self out; we leave it in so the caller can render the
// local node consistently with remotes.
func Peers() ([]Peer, error) {
	if !Available() {
		return nil, fmt.Errorf("tsnet: `tailscale` CLI not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		// stderr from tailscale is often cleaner than the wrapped
		// exec error; surface it directly.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("tsnet: tailscale status: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("tsnet: tailscale status: %w", err)
	}
	return parseStatus(out)
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
}

func parseStatus(raw []byte) ([]Peer, error) {
	var s rawStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("tsnet: parse status: %w", err)
	}
	var out []Peer
	if s.Self != nil {
		out = append(out, toPeer(s.Self, true))
	}
	for _, n := range s.Peer {
		out = append(out, toPeer(n, false))
	}
	return out, nil
}

func toPeer(n *rawNode, self bool) Peer {
	return Peer{
		HostName: n.HostName,
		IP:       firstIPv4(n.TailscaleIPs),
		OS:       n.OS,
		Online:   n.Online,
		IsSelf:   self,
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
