package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/noesrafa/sunny/internal/auth"
	"github.com/noesrafa/sunny/internal/peers"
)

// pairCmd implements the `sunny pair offer` / `sunny pair claim`
// dance. The flow is intentionally tiny on purpose: the operator
// types one command on each side, no SSH, no token copy-paste.
//
//	# on the daemon you want to expose
//	sunny pair offer
//	→ Pair code: A4F7K2 (valid 5 min)
//
//	# on the client (your laptop)
//	sunny pair claim http://100.64.0.5:7777 A4F7K2
//	→ ✓ added peer 100-64-0-5
func pairCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sunny pair {offer|claim <url> <code>}")
	}
	switch args[0] {
	case "offer":
		return pairOffer(args[1:])
	case "claim":
		return pairClaim(args[1:])
	}
	return fmt.Errorf("unknown subcommand: %s", args[0])
}

// pairOffer hits POST /pairing/offer on the local daemon and prints
// the code. The daemon is expected to be running — pairing without
// a daemon makes no sense (there's no bearer to share).
func pairOffer(args []string) error {
	fs := flag.NewFlagSet("pair offer", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	addr := fs.String("addr", "127.0.0.1:7777", "local daemon address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tok, err := auth.LoadToken(*root)
	if err != nil {
		return fmt.Errorf("load token: %w (is the daemon running?)", err)
	}
	resp, err := postJSON("http://"+*addr+"/pairing/offer", tok, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon refused: %s", readErr(resp))
	}
	var body struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	fmt.Println()
	fmt.Println("  Pair code:  " + body.Code)
	fmt.Println("  Valid for:  5 minutes")
	fmt.Println()
	fmt.Println("On the client machine, run:")
	fmt.Printf("  sunny pair claim http://<this-host>:7777 %s\n", body.Code)
	return nil
}

// pairClaim POSTs the code at the remote and persists the bearer it
// gets back into ~/.sunny/peers.yaml. We name the peer after a
// hostname-derived slug so the operator doesn't have to invent one;
// --name overrides.
func pairClaim(args []string) error {
	fs := flag.NewFlagSet("pair claim", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	name := fs.String("name", "", "name for the peer in peers.yaml (default: derived from URL host)")

	// Accept flags on either side of the positionals so
	// `sunny pair claim <url> <code> --name vps` works the same as
	// `sunny pair claim --name vps <url> <code>`. flag.Parse's
	// strict ordering is annoying for hand-typed CLIs.
	var positional []string
	var flagArgs []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || (len(flagArgs) > 0 && !strings.HasPrefix(flagArgs[len(flagArgs)-1], "--")) {
			// Heuristic: anything starting with - is a flag; the next
			// arg after a "--name"-style flag is its value.
		}
		_ = a
	}
	// Simpler partition: collect positionals (no leading -) and pass
	// the rest to flag.Parse.
	positional = positional[:0]
	flagArgs = flagArgs[:0]
	skip := false
	for i, a := range args {
		if skip {
			skip = false
			flagArgs = append(flagArgs, a)
			continue
		}
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			// If this is "--name=foo" the value is bundled; otherwise
			// the next arg is the value (eat it).
			if !strings.Contains(a, "=") && i+1 < len(args) {
				skip = true
			}
			continue
		}
		positional = append(positional, a)
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: sunny pair claim <url> <code> [--name <name>]")
	}
	rawURL := positional[0]
	code := strings.ToUpper(strings.TrimSpace(positional[1]))

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid url %q (expected http://host:port)", rawURL)
	}

	if *name == "" {
		*name = peerNameFromHost(parsed.Host)
	}
	if *name == peers.LocalName {
		return fmt.Errorf("name %q is reserved for the local daemon", peers.LocalName)
	}

	body, _ := json.Marshal(map[string]string{"code": code})
	resp, err := postJSON(strings.TrimRight(rawURL, "/")+"/pairing/claim", "", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("claim failed: %s", readErr(resp))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if out.Token == "" {
		return fmt.Errorf("claim succeeded but server returned no token")
	}

	// Append to peers.yaml. Reload first so concurrent edits don't
	// stomp; reject if name already exists.
	tok, _ := auth.LoadToken(*root)
	r, err := peers.Load(*root, "127.0.0.1:7777", tok)
	if err != nil {
		return err
	}
	for _, p := range r.Remote {
		if p.Name == *name {
			return fmt.Errorf("peer %q already exists; remove it first or pass --name", *name)
		}
	}
	updated := append([]peers.Peer{}, r.Remote...)
	updated = append(updated, peers.Peer{Name: *name, URL: rawURL, Token: out.Token})
	if err := peers.Save(*root, updated); err != nil {
		return err
	}

	fmt.Printf("\n✓ paired %s → %s\n", *name, rawURL)
	fmt.Println("\nThe TUI picks up new peers on next launch. Verify with:")
	fmt.Println("  sunny doctor")
	return nil
}

// peerNameFromHost makes a safe slug out of a URL host: drops the
// port, replaces dots/colons with dashes, lowercases. Falls back to
// "remote" when nothing usable comes through.
func peerNameFromHost(hostport string) string {
	host := hostport
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	host = strings.ReplaceAll(host, ".", "-")
	host = strings.ReplaceAll(host, ":", "-")
	host = strings.Trim(host, "-")
	if host == "" || host == "127-0-0-1" || host == "localhost" {
		return "remote"
	}
	return host
}

// postJSON is a small helper shared by offer + claim. token may be
// empty (claim doesn't need one — the code IS the credential).
func postJSON(url, token string, body []byte) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return http.DefaultClient.Do(req)
}

func readErr(resp *http.Response) string {
	raw, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(raw))
}
