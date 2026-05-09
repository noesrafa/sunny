// Package tlscerts handles the daemon's TLS certificate lifecycle:
// where it lives on disk, how to obtain a fresh one via Tailscale's
// `tailscale cert` (Let's Encrypt under the hood), and when to
// renew. Self-signed fallback for non-Tailscale installs is NOT
// implemented here yet — the bigger blocker on that path is iOS
// cert pinning, which lives app-side. v0.41 ships Tailscale-only.
//
// Why TLS at all: iOS 26 stopped honoring NSAllowsArbitraryLoads
// for production builds, so HTTP cleartext is dead for app→daemon
// connections. Tailscale's HTTPS Certificates feature gives every
// node on a tailnet a free Let's Encrypt cert for its
// `<host>.<tailnet>.ts.net` FQDN. iOS trusts Let's Encrypt out of
// the box; no native module or trust dance required app-side.
package tlscerts

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/noesrafa/sunny/internal/tsnet"
)

// PathsFor returns the cert/key file paths under the daemon root.
// We use a fixed pair of names rather than per-hostname filenames
// because (a) only one cert is active at a time and (b) when the
// hostname changes (e.g. user renames their tailnet node) we want
// the next issue to overwrite, not litter the directory.
func PathsFor(root string) (certPath, keyPath string) {
	dir := filepath.Join(root, "tls")
	return filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
}

// Ensure makes sure a usable cert+key pair exists at the supplied
// paths for the given hostname. If the files are missing, expired,
// or near expiry (renewBefore window), it shells to `tailscale cert`
// to obtain a fresh pair. On success returns the active cert's
// NotAfter so callers can schedule the next check.
//
// renewBefore is the slack window: a cert that expires within
// renewBefore is treated as already needing refresh. Let's Encrypt
// certs from Tailscale have ~90-day lifetimes; 30 days is a
// reasonable default.
func Ensure(root, hostname string, renewBefore time.Duration) (notAfter time.Time, err error) {
	certPath, keyPath := PathsFor(root)
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return time.Time{}, fmt.Errorf("tlscerts: mkdir: %w", err)
	}

	if existing, parseErr := readCertNotAfter(certPath); parseErr == nil {
		if time.Until(existing) > renewBefore {
			return existing, nil
		}
	}

	if err := tsnet.IssueCert(hostname, certPath, keyPath); err != nil {
		return time.Time{}, err
	}
	// Tighten file modes — `tailscale cert` may write 0644 by default,
	// and the private key shouldn't be world-readable.
	_ = os.Chmod(keyPath, 0o600)
	_ = os.Chmod(certPath, 0o644)
	return readCertNotAfter(certPath)
}

// LoadConfig reads the cert+key from the daemon root and returns a
// *tls.Config ready to drop into http.Server.TLSConfig. Returns
// (nil, error) when no cert is present yet — caller is expected to
// call Ensure first.
func LoadConfig(root string) (*tls.Config, error) {
	certPath, keyPath := PathsFor(root)
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("tlscerts: load keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// NotAfter returns the expiration of the cert currently on disk, or
// (zero, error) when no cert is present / readable.
func NotAfter(root string) (time.Time, error) {
	certPath, _ := PathsFor(root)
	return readCertNotAfter(certPath)
}

func readCertNotAfter(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return time.Time{}, errors.New("tlscerts: no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("tlscerts: parse: %w", err)
	}
	return cert.NotAfter, nil
}
