package prompt

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/noesrafa/sunny/internal/tsnet"
)

// cdmxLocation is the canonical timezone for sunny's environment
// block. The date the agent reads in its system prompt should match
// the user's wall clock; CDMX is hardcoded because the user lives
// there and switching machines or env vars shouldn't drift the
// reported date. If a future user wants a different TZ, this is the
// one knob to surface.
var cdmxLocation = func() *time.Location {
	loc, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		// Fallback: UTC. Should never happen on a machine with a
		// working tzdata; if it does, drift is the lesser evil vs.
		// crashing the prompt builder.
		return time.UTC
	}
	return loc
}()

// Env captures the daemon's runtime environment for inclusion in the
// system prompt. Sampled fresh per turn so the agent always knows
// today's date and where it's running.
//
// Fields are best-effort: any of LocalIPv4 / TailnetIPv4 / DaemonAddr
// may be empty. The block renders skipping empty rows.
type Env struct {
	// Now is the moment the env was sampled. Rendered in CDMX.
	Now time.Time
	// Hostname is os.Hostname() (e.g. "rafael-mbp", "vmi3091691").
	Hostname string
	// Platform is "<os>/<arch>" plus a coarse label ("macOS" / "Linux").
	// E.g. "macOS (darwin/arm64)" or "Linux (linux/amd64)".
	Platform string
	// LocalIPv4 is the daemon's primary non-loopback IPv4, or "" if
	// none could be determined.
	LocalIPv4 string
	// TailnetIPv4 is the tailscale-assigned IPv4 (100.x.y.z), or ""
	// when tailscale isn't installed/up.
	TailnetIPv4 string
	// DaemonAddr is the address the daemon is listening on
	// (e.g. "127.0.0.1:7777").
	DaemonAddr string
}

// SampleEnv returns the current environment for the running daemon.
// Hostname / platform / IPs are read inline; tailscale is best-effort
// (skipped silently when not on PATH or not up).
func SampleEnv(daemonAddr string) *Env {
	hostname, _ := os.Hostname()
	env := &Env{
		Now:        time.Now(),
		Hostname:   hostname,
		Platform:   formatPlatform(),
		LocalIPv4:  primaryIPv4(),
		DaemonAddr: daemonAddr,
	}
	if tsnet.Available() {
		if ip, err := tsnet.LocalIP(); err == nil {
			env.TailnetIPv4 = ip
		}
	}
	return env
}

// formatPlatform produces a one-line label for the OS/arch pair.
// runtime.GOOS values get a friendly prefix; everything else falls
// through to the raw pair.
func formatPlatform() string {
	pretty := runtime.GOOS
	switch runtime.GOOS {
	case "darwin":
		pretty = "macOS"
	case "linux":
		pretty = "Linux"
	case "windows":
		pretty = "Windows"
	}
	return fmt.Sprintf("%s (%s/%s)", pretty, runtime.GOOS, runtime.GOARCH)
}

// primaryIPv4 returns the first non-loopback, non-link-local IPv4
// address found among the host's interfaces, or "" if none.
// Cheap pure-Go probe — no shelling out.
func primaryIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			continue
		}
		return ip4.String()
	}
	return ""
}

// formatEnv renders the env block markdown. Returned text is meant
// to live AFTER the cache breakpoint — date and IPs may drift turn
// to turn, and we don't want them invalidating the cached prefix.
func formatEnv(env *Env) string {
	if env == nil {
		return ""
	}
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(cdmxLocation)

	var b strings.Builder
	b.WriteString("## Environment\n\n")
	b.WriteString("- **Now**: ")
	b.WriteString(now.Format("Monday, 2006-01-02 15:04 MST"))
	b.WriteString("\n")
	if env.Hostname != "" {
		b.WriteString("- **Host**: ")
		b.WriteString(env.Hostname)
		b.WriteString("\n")
	}
	if env.Platform != "" {
		b.WriteString("- **Platform**: ")
		b.WriteString(env.Platform)
		b.WriteString("\n")
	}
	if env.LocalIPv4 != "" {
		b.WriteString("- **LAN IPv4**: ")
		b.WriteString(env.LocalIPv4)
		b.WriteString("\n")
	}
	if env.TailnetIPv4 != "" {
		b.WriteString("- **Tailnet IPv4**: ")
		b.WriteString(env.TailnetIPv4)
		b.WriteString("\n")
	}
	if env.DaemonAddr != "" {
		b.WriteString("- **Daemon**: ")
		b.WriteString(env.DaemonAddr)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
