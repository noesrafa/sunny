// `sunny app` renders a QR for the iOS/Android app to scan and pair
// with this daemon in one shot. The QR encodes a short-lived pair
// code (5min TTL via /pairing/offer), NOT the bearer token — so a
// photo of the QR after the fact doesn't grant access.
//
// Flow:
//
//  1. CLI calls POST /pairing/offer locally → gets a 6-char code
//  2. CLI picks the best reachable URL: tailnet IP > LAN IPv4 > 127.0.0.1
//  3. CLI prints `sunny://pair?url=<best>&code=<code>&name=<host>` as a QR
//  4. App scans, parses, POSTs /pairing/claim → receives bearer
//  5. App saves peer, navigates to chats
//
// The fallback URLs are printed too so the operator can pick a
// different one (e.g. when the phone is on a different network than
// the chosen default).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mdp/qrterminal/v3"

	"github.com/noesrafa/sunny/internal/auth"
	"github.com/noesrafa/sunny/internal/lifecycle"
	"github.com/noesrafa/sunny/internal/tsnet"
)

func appCmd(args []string) error {
	fs := flag.NewFlagSet("app", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	addrFlag := fs.String("addr", "", "override daemon address (default: read from state.json)")
	tlsPortFlag := fs.String("tls-port", "7778", "HTTPS port the daemon listens on (matches `serve --tls-addr`)")
	nameFlag := fs.String("name", "", "name to prefill on the app (default: hostname)")
	prefer := fs.String("prefer", "", "force the URL shown in the QR (tls|tailnet|lan|localhost)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tok, err := auth.LoadToken(*root)
	if err != nil {
		return fmt.Errorf("load token: %w (is the daemon running?)", err)
	}

	// Where is the daemon listening? state.json carries the addr the
	// running daemon picked at start; --addr can override.
	localAddr := *addrFlag
	if localAddr == "" {
		paths := lifecycle.PathsFor(*root)
		if st, err := paths.LoadState(); err == nil && st != nil {
			localAddr = st.Addr
		}
	}
	if localAddr == "" {
		localAddr = "127.0.0.1:7777"
	}
	port := portOf(localAddr)

	// Ask the local daemon for a pair code (5min TTL).
	resp, err := postJSON("http://"+localAddr+"/pairing/offer", tok, nil)
	if err != nil {
		return fmt.Errorf("offer pair code: %w (is the daemon up at %s?)", err, localAddr)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon refused offer: %s", readErr(resp))
	}
	var offered struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&offered); err != nil {
		return fmt.Errorf("decode offer: %w", err)
	}

	// Build the candidate URL list. Order = preference for the QR
	// payload, but every option is printed as a fallback below the QR.
	cands := candidates(port, *tlsPortFlag)
	pref := strings.ToLower(*prefer)
	if pref != "" {
		cands = filterByKind(cands, pref)
		if len(cands) == 0 {
			return fmt.Errorf("no %s address available on this host", pref)
		}
	}
	if len(cands) == 0 {
		return fmt.Errorf("no reachable address found for daemon at port %s", port)
	}
	chosen := cands[0]

	name := *nameFlag
	if name == "" {
		host, _ := os.Hostname()
		name = sanitizeHost(host)
	}

	// Build the pair URL with the chosen URL as the primary AND every
	// candidate as alternates. The app tries each in order until one
	// connects, so a user without tailscale on their phone can still
	// pair via the LAN address from a single QR scan. Older apps that
	// only read `url=` are unaffected.
	altURLs := make([]string, 0, len(cands))
	for _, c := range cands {
		altURLs = append(altURLs, c.URL)
	}
	pairURL := buildPairURL(chosen.URL, altURLs, offered.Code, name)

	// Render. qrterminal.M is medium error correction, plenty for a
	// payload this small (under 200 bytes), and it keeps the matrix
	// compact enough to fit a typical 80-col terminal.
	cfg := qrterminal.Config{
		Level:     qrterminal.M,
		Writer:    os.Stdout,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	}
	fmt.Println()
	qrterminal.GenerateWithConfig(pairURL, cfg)
	fmt.Println()
	fmt.Println("  Escanea con Sunny (iOS/Android)")
	fmt.Println("  Código:", offered.Code, " · expira en 5 min")
	fmt.Println()
	fmt.Println("  Si no funciona el QR, pega esto en la app:")
	fmt.Println("   ", pairURL)
	if len(cands) > 1 {
		fmt.Println()
		fmt.Println("  Direcciones alternativas (usa --prefer para forzar):")
		for _, c := range cands {
			marker := "  "
			if c.URL == chosen.URL {
				marker = "→ "
			}
			fmt.Printf("    %s%-9s %s\n", marker, "["+c.Kind+"]", c.URL)
		}
	}
	return nil
}

type addrCandidate struct {
	URL  string
	Kind string // "tailnet" | "lan" | "localhost"
}

// candidates returns the URLs the app could connect to, in
// preference order:
//
//	1. tls (HTTPS to <host>.<tailnet>.ts.net) — only path that works
//	   from iOS 26 production builds, since NSAllowsArbitraryLoads
//	   is now ignored for HTTP. Relies on the daemon's TLS listener
//	   bringing up the Tailscale Let's Encrypt cert.
//	2. tailnet (HTTP to the tailnet IP) — kept as a fallback for
//	   non-iOS clients (TUI, curl) and older app builds.
//	3. lan (HTTP to a local-WiFi IP) — same WiFi, no tailscale.
//	4. localhost — only useful for the iOS simulator on this Mac.
//
// The TLS port comes from --tls-addr (default :7778); when --no-tls
// is set or MagicDNS isn't available, it's just absent.
func candidates(httpPort, tlsPort string) []addrCandidate {
	out := []addrCandidate{}
	// Pair TLS with the tailnet hostname: certs are issued for the
	// MagicDNS FQDN, not the IP. iOS validates SNI/CN against the
	// URL, so an IP-based HTTPS URL would fail name validation.
	if tlsPort != "" {
		if dns, err := tsnet.MagicDNSName(); err == nil && dns != "" {
			out = append(out, addrCandidate{
				URL:  fmt.Sprintf("https://%s:%s", dns, tlsPort),
				Kind: "tls",
			})
		}
	}
	if ip, err := tsnet.LocalIP(); err == nil && ip != "" {
		out = append(out, addrCandidate{URL: fmt.Sprintf("http://%s:%s", ip, httpPort), Kind: "tailnet"})
	}
	for _, ip := range lanIPv4s() {
		out = append(out, addrCandidate{URL: fmt.Sprintf("http://%s:%s", ip, httpPort), Kind: "lan"})
	}
	out = append(out, addrCandidate{URL: "http://127.0.0.1:" + httpPort, Kind: "localhost"})
	return out
}

func filterByKind(in []addrCandidate, kind string) []addrCandidate {
	out := []addrCandidate{}
	for _, c := range in {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// lanIPv4s returns every non-loopback IPv4 attached to a `up` interface.
// We skip docker/utun/bridge interfaces by name to avoid surfacing
// addresses that won't actually route from a phone on the same WiFi.
func lanIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		// Common interfaces we don't want to advertise to a phone:
		//   utun*    — point-to-point tunnels (incl. tailscale on macOS,
		//              already covered by tsnet.LocalIP)
		//   awdl*    — Apple Wireless Direct Link
		//   bridge*  — docker / vm bridges
		//   docker*  — docker linux
		//   llw*     — low-latency wlan (Apple)
		if strings.HasPrefix(name, "utun") ||
			strings.HasPrefix(name, "awdl") ||
			strings.HasPrefix(name, "llw") ||
			strings.HasPrefix(name, "bridge") ||
			strings.HasPrefix(name, "docker") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "7777"
	}
	return port
}

func buildPairURL(daemonURL string, altURLs []string, code, name string) string {
	v := url.Values{}
	v.Set("url", daemonURL)
	v.Set("code", code)
	if name != "" {
		v.Set("name", name)
	}
	// Comma-separated alternate URLs the app can try when the primary
	// is unreachable from the phone (e.g. tailnet primary but the
	// phone has no tailscale → fall back to LAN). Always includes the
	// primary too so the app can treat `urls` as authoritative; we
	// keep `url` as a back-compat shim for older app builds.
	if len(altURLs) > 0 {
		v.Set("urls", strings.Join(altURLs, ","))
	}
	return "sunny://pair?" + v.Encode()
}

func sanitizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".local")
	if h == "" {
		return "host"
	}
	return h
}
