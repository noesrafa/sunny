package pairing

import (
	"strings"
	"testing"
	"time"
)

func TestOfferClaim_HappyPath(t *testing.T) {
	s := NewService("secret-bearer")
	o, err := s.Offer(time.Minute)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if len(o.Code) != CodeLen {
		t.Errorf("code len = %d, want %d", len(o.Code), CodeLen)
	}
	for _, c := range o.Code {
		if !strings.ContainsRune(codeAlphabet, c) {
			t.Errorf("code char %q not in alphabet", c)
		}
	}
	if o.Bearer != "secret-bearer" {
		t.Errorf("bearer = %q, want secret-bearer", o.Bearer)
	}

	bearer, ok := s.Claim(o.Code)
	if !ok {
		t.Fatalf("Claim returned !ok for fresh code")
	}
	if bearer != "secret-bearer" {
		t.Errorf("Claim bearer = %q, want secret-bearer", bearer)
	}
}

func TestClaim_SingleUse(t *testing.T) {
	s := NewService("b")
	o, _ := s.Offer(time.Minute)
	if _, ok := s.Claim(o.Code); !ok {
		t.Fatalf("first claim should succeed")
	}
	if _, ok := s.Claim(o.Code); ok {
		t.Errorf("second claim of the same code should fail")
	}
}

func TestClaim_Unknown(t *testing.T) {
	s := NewService("b")
	if _, ok := s.Claim("AAAAAA"); ok {
		t.Errorf("Claim of unknown code should return !ok")
	}
}

func TestClaim_Expired(t *testing.T) {
	s := NewService("b")
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	o, _ := s.Offer(30 * time.Second)
	s.now = func() time.Time { return now.Add(time.Minute) } // jump past expiry
	if _, ok := s.Claim(o.Code); ok {
		t.Errorf("Claim of expired code should fail")
	}
}

func TestOffer_DefaultTTL(t *testing.T) {
	s := NewService("b")
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	o, _ := s.Offer(0)
	want := now.Add(DefaultTTL)
	if !o.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v (default TTL)", o.ExpiresAt, want)
	}
}

func TestPending_GCs(t *testing.T) {
	s := NewService("b")
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	_, _ = s.Offer(time.Minute)
	_, _ = s.Offer(time.Minute)
	if got := s.Pending(); got != 2 {
		t.Errorf("Pending before gc = %d, want 2", got)
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if got := s.Pending(); got != 0 {
		t.Errorf("Pending after expiry = %d, want 0", got)
	}
}

func TestOffer_CodesAreUniqueEnough(t *testing.T) {
	// Sanity: 1000 codes, no collisions. With 31^6 ≈ 887M combinations
	// the expected collision count is vanishingly small; a hit means
	// our RNG or alphabet broke.
	s := NewService("b")
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		o, err := s.Offer(time.Hour)
		if err != nil {
			t.Fatalf("Offer #%d: %v", i, err)
		}
		if seen[o.Code] {
			t.Fatalf("duplicate code %q at iter %d", o.Code, i)
		}
		seen[o.Code] = true
	}
}
