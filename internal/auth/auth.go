// Package auth manages the daemon's bearer token: a 32-byte random value
// stored at ~/.sunny/token (mode 0600) that every HTTP client must send
// in `Authorization: Bearer <token>` headers.
//
// The token is generated once at first daemon boot and reused on every
// subsequent boot. `sunny token rotate` regenerates it. Clients
// (TUI, curl, future bridges) read the file directly — file permissions
// are the trust boundary, so the daemon never exposes the token over
// HTTP.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the basename of the token file under the runtime root.
const FileName = "token"

// Path returns the absolute path to the token file for a given runtime root.
func Path(root string) string {
	return filepath.Join(root, FileName)
}

// EnsureToken returns the token at root/token, generating one if missing.
// The file is always written with mode 0600. Returns the token string.
func EnsureToken(root string) (string, error) {
	p := Path(root)
	if tok, err := readToken(p); err == nil {
		return tok, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tok, err := generate()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}

// LoadToken reads root/token and returns its contents. Returns
// os.ErrNotExist if the file is missing — callers can decide whether
// that means "daemon never started" or "token rotated externally".
func LoadToken(root string) (string, error) {
	return readToken(Path(root))
}

// Rotate forces a new token, overwriting the existing file. Returns the
// new token. In-flight clients with the old token will start getting 401s
// on their next request.
func Rotate(root string) (string, error) {
	tok, err := generate()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(Path(root), []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}

func readToken(p string) (string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", p)
	}
	return tok, nil
}

func generate() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
