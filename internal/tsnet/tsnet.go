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
	"net"
	"os/exec"
	"strings"
	"time"
)

// Peer is one tailnet node.
type Peer struct {
	HostName string
	// DNSName is the MagicDNS FQDN with trailing dot (e.g.
	// "rafael-mac.taildbeec.ts.net."). Empty when MagicDNS is
	// disabled on the tailnet. Callers stripping the trailing
	// dot should use MagicDNSName for self.
	DNSName string
	IP      string // first IPv4 (the v6 is rarely useful for sunny's HTTP daemon)
	OS      string // "linux" / "macOS" / "iOS" / …
	Online  bool
	IsSelf  bool
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
	// Keep stdout and stderr separate. The tailscale CLI emits
	// non-fatal warnings (e.g. client/daemon version mismatch) on
	// stderr — CombinedOutput mixed them into the address line and
	// callers ended up using "Warning: ..." as the IP.
	cmd := exec.CommandContext(ctx, "tailscale", "ip", "-4")
	stdout, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("tsnet: tailscale ip: %s", stderr)
	}
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Defense in depth: only accept lines that parse as an IP,
		// in case future tailscale versions add hint lines on stdout.
		if ip := net.ParseIP(line); ip != nil {
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
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	OS           string   `json:"OS"`
	Online       bool     `json:"Online"`
	UserID       int64    `json:"UserID"`
}

// MagicDNSName returns the local node's FQDN on the tailnet, e.g.
// "rafael-mac.taildbeec.ts.net" (no trailing dot). This is the only
// name TLS certs from `tailscale cert` are issued for, so it's the
// identifier the daemon must publish to the app for HTTPS pairing.
// Returns ("", error) when tailscale isn't installed/logged in or
// MagicDNS isn't enabled (the field comes back blank in that case).
func MagicDNSName() (string, error) {
	st, err := FetchStatus()
	if err != nil {
		return "", err
	}
	dns := strings.TrimSuffix(st.Self.DNSName, ".")
	if dns == "" {
		return "", fmt.Errorf("tsnet: empty DNSName (MagicDNS disabled?)")
	}
	return dns, nil
}

// IssueCert shells out to `tailscale cert` to obtain (or refresh) a
// Let's Encrypt TLS cert for the given hostname (which MUST be the
// local node's *.ts.net FQDN — Tailscale only signs certs for nodes
// it owns). Writes cert+key to the supplied paths; the operation is
// idempotent — when an unexpired cert already exists, tailscale
// silently re-uses it.
//
// Requires "HTTPS Certificates" enabled in the tailnet admin
// (https://login.tailscale.com/admin/dns) and MagicDNS on. When
// either is missing, the CLI returns a helpful stderr we surface
// verbatim so the operator can act.
func IssueCert(hostname, certPath, keyPath string) error {
	if !Available() {
		return fmt.Errorf("tsnet: `tailscale` CLI not installed")
	}
	if hostname == "" {
		return fmt.Errorf("tsnet: empty hostname")
	}
	// `tailscale cert` is a network operation (provisions via Let's
	// Encrypt on first issue). Default-renews are local-only and fast,
	// but a cold issue can take 10-30s — give it generous headroom.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tailscale", "cert",
		"--cert-file", certPath,
		"--key-file", keyPath,
		hostname,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tsnet: tailscale cert: %s", strings.TrimSpace(string(out)))
	}
	return nil
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
		DNSName:  n.DNSName,
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
