// Package pairing implements the short-lived-code dance two sunny
// daemons use to exchange a bearer token without the operator
// having to SSH into the remote and copy-paste it.
//
// Flow:
//
//  1. Operator runs `sunny pair offer` on the remote daemon. The
//     daemon generates a 6-char code, parks it in memory keyed to
//     its own bearer with a 5-minute TTL, and prints it.
//  2. Operator types the code into `sunny pair claim <url> <code>`
//     on the client. The client POSTs to the remote's
//     `/pairing/claim` (one of two endpoints exempt from auth — the
//     code IS the auth) and receives the bearer.
//  3. Client persists {name, url, bearer} to ~/.sunny/peers.yaml.
//
// Codes are single-use (Claim removes them) and TTL-bounded so a
// leaked code becomes useless quickly. The remote's actual bearer
// is shared as-is — there is no per-peer scoping in v0.13. Future
// work: emit a dedicated bearer per pairing so individual peers
// can be revoked without rotating the master token.
package pairing

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// DefaultTTL is how long a code remains claimable. 5 minutes is a
// hand-typing-friendly window without leaving codes warm forever.
const DefaultTTL = 5 * time.Minute

// CodeLen is the number of characters in a pairing code. 6 is the
// AppleTV / Plex sweet spot — short enough to type by hand from a
// phone screen, long enough to give ~30 bits of entropy after
// excluding ambiguous chars.
const CodeLen = 6

// codeAlphabet excludes 0/O/1/I/L to reduce read errors when the
// operator is squinting at one terminal and typing into another.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// Offer is one pending pairing. Returned by Service.Offer for the
// emitter and by Service.Claim for the consumer.
type Offer struct {
	Code      string    `json:"code"`
	Bearer    string    `json:"bearer"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Service tracks codes in memory. Safe for concurrent use. Bearer is
// the daemon's own token, captured at construction so handlers can
// just call Offer() without re-reading from disk on each request.
type Service struct {
	mu      sync.Mutex
	bearer  string
	pending map[string]Offer
	now     func() time.Time // overridable for tests
}

// NewService captures the daemon's bearer. The bearer is what gets
// handed to the client at Claim time.
func NewService(bearer string) *Service {
	return &Service{
		bearer:  bearer,
		pending: map[string]Offer{},
		now:     time.Now,
	}
}

// Offer mints a fresh pairing code, valid for ttl. Returns the code,
// the bearer it will yield (mostly for debug logging — handlers
// don't surface the bearer to the operator at offer time), and the
// absolute expiry. Lazy-cleans any expired codes on the way in.
func (s *Service) Offer(ttl time.Duration) (Offer, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	code, err := newCode()
	if err != nil {
		return Offer{}, fmt.Errorf("pairing: gen code: %w", err)
	}
	o := Offer{
		Code:      code,
		Bearer:    s.bearer,
		ExpiresAt: s.now().Add(ttl),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	s.pending[code] = o
	return o, nil
}

// Claim consumes a code: returns the bearer and clears the entry so
// the same code cannot be used twice. Returns ok=false for unknown
// or expired codes.
func (s *Service) Claim(code string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	o, ok := s.pending[code]
	if !ok {
		return "", false
	}
	delete(s.pending, code)
	if s.now().After(o.ExpiresAt) {
		return "", false
	}
	return o.Bearer, true
}

// Pending returns the count of unexpired offers. Used by /healthz-
// adjacent diagnostics; not load-bearing.
func (s *Service) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	return len(s.pending)
}

// gcLocked drops expired entries. Cheap because pending is rarely
// more than a handful at a time.
func (s *Service) gcLocked() {
	now := s.now()
	for code, o := range s.pending {
		if now.After(o.ExpiresAt) {
			delete(s.pending, code)
		}
	}
}

// newCode draws CodeLen indices from codeAlphabet using crypto/rand.
// Rejection sampling keeps the distribution flat — modulo bias on
// 31 chars over a 256-byte space would skew the lower indices.
func newCode() (string, error) {
	out := make([]byte, CodeLen)
	buf := make([]byte, 1)
	max := byte(len(codeAlphabet))
	limit := byte(256 - (256 % int(max)))
	for i := 0; i < CodeLen; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] >= limit {
			continue // would bias; reroll
		}
		out[i] = codeAlphabet[int(buf[0])%int(max)]
		i++
	}
	return string(out), nil
}
