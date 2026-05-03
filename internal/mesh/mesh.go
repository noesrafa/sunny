// Package mesh manages the shared symmetric key that identifies a
// sunny installation as part of a federated mesh — Plex-style.
//
// The mesh.key is a 32-byte random value generated at first boot
// (or imported from another machine via `sunny mesh import`). Any
// daemon on your tailnet that holds the same key is automatically
// trusted as a peer: the TUI auto-discovers it via /sunny/identity
// and the daemon accepts authenticated requests from it without a
// per-peer bearer.
//
// Threat model:
//   - The tailnet is the first perimeter — only nodes on your
//     Tailscale network can reach the daemon at all (the daemon
//     binds to the tailnet IP, not 0.0.0.0).
//   - The mesh.key is the second perimeter — only daemons that
//     share it identify as part of YOUR mesh. Without it, a tailnet
//     peer running sunny is just another sunny instance, not
//     authorized to call your APIs.
//   - The pair flow (separate, per-peer bearer) remains the
//     recommended path for hosts NOT on the same tailnet.
//
// Rotation: re-running `sunny mesh init` overwrites the key. Every
// daemon that wants to stay in the mesh has to re-import the new
// one. Acceptable for v0.16 because mesh-keys are bundle credentials
// in spirit, like SSH host keys.
package mesh

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the basename of the mesh key file under the runtime
// root. Mode 0600 — same posture as token + secrets.yaml.
const FileName = "mesh.key"

// KeySize is the random byte count behind the encoded form.
// 32 bytes (256 bits) gives plenty of headroom for HMAC-SHA256 and
// is the same size as the bearer token.
const KeySize = 32

// Key is the encoded form of a mesh key — base64url, no padding,
// human-typable for `sunny mesh import`. Aliasing string keeps
// it value-typed and zero-cost to copy.
type Key string

// Path returns the absolute path to the mesh key file.
func Path(root string) string { return filepath.Join(root, FileName) }

// Load reads the mesh key from disk. Returns ErrAbsent when the
// file does not exist, so callers can distinguish "no mesh
// configured" from "real I/O failure".
func Load(root string) (Key, error) {
	data, err := os.ReadFile(Path(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrAbsent
		}
		return "", fmt.Errorf("mesh: read key: %w", err)
	}
	k := Key(strings.TrimSpace(string(data)))
	if !k.Valid() {
		return "", fmt.Errorf("mesh: %s contents are not a valid key", FileName)
	}
	return k, nil
}

// ErrAbsent is returned by Load when the file isn't there. Sentinel
// (not wrapped) so errors.Is works for callers branching on it.
var ErrAbsent = errors.New("mesh: key not configured")

// Save writes the key with mode 0600. Creates the parent dir if
// needed. Idempotent — same key written twice produces the same
// file content.
func Save(root string, k Key) error {
	if !k.Valid() {
		return fmt.Errorf("mesh: refusing to save invalid key")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("mesh: mkdir runtime: %w", err)
	}
	return os.WriteFile(Path(root), []byte(string(k)+"\n"), 0o600)
}

// Generate mints a fresh random key. crypto/rand failures are
// genuinely fatal here — without entropy there's nothing safe to
// do, so we surface the error verbatim.
func Generate() (Key, error) {
	buf := make([]byte, KeySize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mesh: rand: %w", err)
	}
	return Key(base64.RawURLEncoding.EncodeToString(buf)), nil
}

// Valid reports whether the encoded form decodes to KeySize bytes.
// Used by Load + Save to refuse corrupt input early.
func (k Key) Valid() bool {
	if k == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(k))
	if err != nil {
		return false
	}
	return len(raw) == KeySize
}

// Fingerprint is a short public identifier derived from the key.
// First 8 hex chars of SHA-256(key) — collision-resistant enough
// for mesh-membership checks, short enough to grep for, public
// enough to expose without leaking the key itself.
//
// Two daemons share a mesh ⇔ they share a fingerprint ⇔ they share
// the underlying key.
func (k Key) Fingerprint() string {
	sum := sha256.Sum256([]byte(k))
	return hex.EncodeToString(sum[:])[:8]
}

// Equal compares two keys in constant time. Use this in auth code
// paths so timing leaks don't trickle out via "wrong key" responses.
func (k Key) Equal(other Key) bool {
	return subtle.ConstantTimeCompare([]byte(k), []byte(other)) == 1
}
