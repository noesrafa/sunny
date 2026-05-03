package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/noesrafa/sunny/internal/auth"
	"github.com/noesrafa/sunny/internal/peers"
	"github.com/noesrafa/sunny/internal/tsnet"
)

// peersCmd is the CLI surface for ~/.sunny/peers.yaml. Mirrors the
// shape of `sunny secrets`:
//
//	sunny peers                       → list local + remote
//	sunny peers add <name> <url>      → reads token from stdin, validates, saves
//	sunny peers remove <name>         → removes the entry
//
// The local daemon is always implicit and never appears in the file;
// it shows up in `peers` listings as `local`.
func peersCmd(args []string) error {
	fs := flag.NewFlagSet("peers", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	addr := fs.String("addr", "127.0.0.1:7777", "local daemon address (only used to label the local row)")
	noVerify := fs.Bool("no-verify", false, "skip the GET /healthz round-trip when adding a peer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()

	if len(rest) == 0 {
		return peersList(*root, *addr)
	}
	switch rest[0] {
	case "add":
		if len(rest) != 3 {
			return fmt.Errorf("usage: sunny peers add <name> <url>  (token read from stdin)")
		}
		return peersAdd(*root, rest[1], rest[2], *noVerify)
	case "remove", "rm", "delete":
		if len(rest) != 2 {
			return fmt.Errorf("usage: sunny peers remove <name>")
		}
		return peersRemove(*root, rest[1])
	case "scan":
		port := "7777"
		if _, p, err := net.SplitHostPort(*addr); err == nil && p != "" {
			port = p
		}
		return peersScan(*root, port)
	}
	return fmt.Errorf("unknown subcommand: %s", strings.Join(rest, " "))
}

// peersList prints local + remote with status icons. Status is purely
// "do we have config" — health checks live in `sunny doctor` so this
// stays fast and offline-friendly.
func peersList(root, addr string) error {
	tok, _ := auth.LoadToken(root) // token may be missing pre-bootstrap; shrug
	r, err := peers.Load(root, addr, tok)
	if err != nil {
		return err
	}
	all := r.All()
	for _, p := range all {
		marker := "·"
		if p.Name == peers.LocalName {
			marker = "★"
		}
		fmt.Printf("  %s %-12s %s\n", marker, p.Name, p.URL)
	}
	if len(all) == 1 {
		fmt.Println()
		fmt.Println("(no remote peers — try: sunny peers add <name> <url>)")
	}
	return nil
}

// peersAdd validates the URL, optionally probes /healthz, reads the
// token from stdin (echo NOT suppressed — same caveat as `secrets
// set`; pipe for safety), and persists the new entry.
func peersAdd(root, name, url string, noVerify bool) error {
	tok, _ := auth.LoadToken(root)
	r, err := peers.Load(root, "127.0.0.1:7777", tok)
	if err != nil {
		return err
	}
	for _, p := range r.Remote {
		if p.Name == name {
			return fmt.Errorf("peer %q already exists (remove it first: sunny peers remove %s)", name, name)
		}
	}
	if name == peers.LocalName {
		return fmt.Errorf("name %q is reserved for the local daemon", peers.LocalName)
	}

	fmt.Printf("token for peer %s (will echo — pipe for sensitive values):\n", name)
	token, err := readSecretValue(name + ".token")
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("empty token — refusing to save")
	}

	if !noVerify {
		if err := pingPeer(url, token); err != nil {
			return fmt.Errorf("peer rejected the handshake: %w (use --no-verify to save anyway)", err)
		}
	}

	updated := append([]peers.Peer{}, r.Remote...)
	updated = append(updated, peers.Peer{Name: name, URL: url, Token: token})
	if err := peers.Save(root, updated); err != nil {
		return err
	}
	fmt.Printf("\n✓ added peer %s → %s\n", name, url)
	fmt.Println("\nThe TUI picks up new peers on next launch.")
	return nil
}

func peersRemove(root, name string) error {
	if name == peers.LocalName {
		return fmt.Errorf("local is implicit — it can't be removed")
	}
	tok, _ := auth.LoadToken(root)
	r, err := peers.Load(root, "127.0.0.1:7777", tok)
	if err != nil {
		return err
	}
	var keep []peers.Peer
	found := false
	for _, p := range r.Remote {
		if p.Name == name {
			found = true
			continue
		}
		keep = append(keep, p)
	}
	if !found {
		return fmt.Errorf("no peer named %q", name)
	}
	if err := peers.Save(root, keep); err != nil {
		return err
	}
	fmt.Printf("removed peer %s\n", name)
	return nil
}

// peersScan walks the tailnet and surfaces every host that responds
// on :<port>/healthz, marking the ones that are already paired in
// peers.yaml. It deliberately does NOT pair anything — pairing is a
// two-side consent flow (offer + claim), so all this command can do
// is help the operator see what's pairable.
//
// Output groups peers into "candidates" (reachable, not yet paired)
// and "already paired" + an "unreachable" list at the end.
func peersScan(root, port string) error {
	if !tsnet.Available() {
		return fmt.Errorf("tailscale CLI not installed (brew install tailscale)")
	}
	hosts, err := tsnet.Peers()
	if err != nil {
		return err
	}
	tok, _ := auth.LoadToken(root)
	r, _ := peers.Load(root, "127.0.0.1:7777", tok) // soft-load; missing peers.yaml is fine
	knownByURL := map[string]string{} // url → peer name
	for _, p := range r.Remote {
		knownByURL[strings.TrimRight(p.URL, "/")] = p.Name
	}

	type result struct {
		host      tsnet.Peer
		url       string
		reachable bool
		paired    string // peer name when already in peers.yaml
	}
	results := make([]result, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		if h.IsSelf || !h.Online || h.IP == "" {
			results[i] = result{host: h}
			continue
		}
		wg.Add(1)
		go func(i int, h tsnet.Peer) {
			defer wg.Done()
			url := "http://" + net.JoinHostPort(h.IP, port)
			results[i] = result{host: h, url: url, reachable: probeHealthz(url), paired: knownByURL[url]}
		}(i, h)
	}
	wg.Wait()

	var candidates, paired, dark []result
	for _, r := range results {
		switch {
		case r.host.IsSelf:
			// drop self
		case !r.host.Online:
			// drop offline (tailscale knows they're not reachable)
		case r.paired != "":
			paired = append(paired, r)
		case r.reachable:
			candidates = append(candidates, r)
		default:
			dark = append(dark, r)
		}
	}

	if len(candidates) > 0 {
		fmt.Println("Candidates (run sunny pair on each side to add):")
		for _, c := range candidates {
			fmt.Printf("  · %-20s %s  [%s]\n", c.host.HostName, c.url, c.host.OS)
		}
	}
	if len(paired) > 0 {
		fmt.Println()
		fmt.Println("Already paired:")
		for _, p := range paired {
			fmt.Printf("  ✓ %-20s %s  (as %q)\n", p.host.HostName, p.url, p.paired)
		}
	}
	if len(dark) > 0 {
		fmt.Println()
		fmt.Println("Tailnet hosts without a sunny daemon on :" + port + ":")
		for _, d := range dark {
			fmt.Printf("  ✗ %-20s %s\n", d.host.HostName, d.url)
		}
	}
	if len(candidates)+len(paired)+len(dark) == 0 {
		fmt.Println("(no other tailnet hosts found)")
	}
	return nil
}

// probeHealthz GETs /healthz with a short timeout. /healthz is the
// one endpoint that doesn't require auth, perfect for "is sunny
// listening here?".
func probeHealthz(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// pingPeer hits GET /agents on the peer with the given token. We use
// /agents (not /healthz) because /healthz is unauthenticated — a 200
// from /agents proves the URL is reachable AND the token is valid.
// Short timeout so a typo'd hostname fails fast.
func pingPeer(base, token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/agents", nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("401 Unauthorized — token rejected")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

