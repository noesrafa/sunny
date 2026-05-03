package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// offerPairing is POST /pairing/offer (auth required). The body is
// optional today — future versions may carry name/url hints.
//
// Response: {code, expires_at}. We intentionally do NOT echo the
// bearer back to the operator: it's already in `~/.sunny/token` on
// this machine, surfacing it again would just invite "what's this
// long string for?" support questions.
func (s *server) offerPairing(w http.ResponseWriter, r *http.Request) {
	if s.pairs == nil {
		http.Error(w, "pairing not configured", http.StatusServiceUnavailable)
		return
	}
	o, err := s.pairs.Offer(0) // default TTL
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}{
		Code:      o.Code,
		ExpiresAt: o.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// claimPairing is POST /pairing/claim (UNAUTHENTICATED). The code is
// the credential — that's the entire point of the dance. Bodies look
// like {"code": "A4F7K2"} and responses {"token": "...", "issuer": "<this daemon's URL hint>"}.
//
// Single-use semantics live in pairing.Service: a successful claim
// removes the code, an expired or unknown one returns 404 with no
// hint of which case it was. We deliberately don't 401 — that would
// imply the request was authenticated against the wrong principal.
func (s *server) claimPairing(w http.ResponseWriter, r *http.Request) {
	if s.pairs == nil {
		http.Error(w, "pairing not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	code := strings.ToUpper(strings.TrimSpace(body.Code))
	if code == "" {
		http.Error(w, "code required", http.StatusBadRequest)
		return
	}
	bearer, ok := s.pairs.Claim(code)
	if !ok {
		http.Error(w, "code unknown or expired", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Token string `json:"token"`
	}{Token: bearer})
}

// pairingExempt reports whether a path bypasses bearer auth because
// the pairing flow does its own credential check (the code itself).
// Kept centralized so requireBearer + the route table stay in sync.
func pairingExempt(path string) bool {
	return path == "/pairing/claim"
}
