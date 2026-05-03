package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/noesrafa/sunny/internal/auth"
	"github.com/noesrafa/sunny/internal/peers"
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

