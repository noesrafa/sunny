package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/noesrafa/sunny/internal/mesh"
	"github.com/noesrafa/sunny/internal/tsnet"
)

// MeshHeader is the request header carrying the shared mesh key.
// Same casing pattern as Tailscale's own X-Tailscale-User —
// X-prefix for "this is custom", short and grep-able.
const MeshHeader = "X-Sunny-Mesh"

// IdentityResponse is what `GET /sunny/identity` returns. Public
// (no auth) so the client side can decide whether to trust this
// daemon as part of the same mesh BEFORE sending any credential.
//
// Field naming is snake_case to match the rest of the JSON wire
// shape (agent slug, conv_id, etc.) — the daemon stays consistent
// even when the new endpoint is conceptually different.
type IdentityResponse struct {
	App         string `json:"app"`
	Version     string `json:"version"`
	Mesh        string `json:"mesh_fingerprint,omitempty"`
	InstanceID  string `json:"instance_id,omitempty"`
}

// streamIdentity handles GET /sunny/identity. No auth — the whole
// point is to let unauthed clients identify which mesh this
// daemon belongs to before deciding to talk to it.
func (s *server) streamIdentity(w http.ResponseWriter, _ *http.Request) {
	out := IdentityResponse{
		App:        "sunny",
		Version:    s.version,
		InstanceID: s.instanceID,
	}
	if s.meshKey != "" {
		out.Mesh = s.meshKey.Fingerprint()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// identityExempt reports whether a path bypasses bearer auth
// because identity is intentionally public — same role as
// pairingExempt, kept side-by-side so the auth-skip decisions
// stay in one place.
func identityExempt(path string) bool {
	return path == "/sunny/identity"
}

// MeshAuth wraps the given handler with the "tailnet + matching
// mesh key" shortcut. When a request arrives from a known tailnet
// IP AND carries the right X-Sunny-Mesh header, we mark it as
// authenticated and forward to h. Anything else passes through
// unchanged — requireBearer downstream still gets to enforce its
// own policy.
//
// tailnetIPs is a snapshot function so the caller controls
// refresh cadence (5 min default in serve.go); the middleware
// just consults it on every request, which is cheap (map lookup).
func MeshAuth(key mesh.Key, tailnetIPs func() map[string]bool, h http.Handler) http.Handler {
	if key == "" {
		return h // mesh disabled — middleware is a no-op
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mustAuthMethod(r) {
			h.ServeHTTP(w, r)
			return
		}
		got := mesh.Key(strings.TrimSpace(r.Header.Get(MeshHeader)))
		if got == "" || !key.Equal(got) {
			h.ServeHTTP(w, r)
			return
		}
		ip := remoteHost(r)
		if ip == "" {
			h.ServeHTTP(w, r)
			return
		}
		ips := tailnetIPs()
		if !ips[ip] {
			h.ServeHTTP(w, r)
			return
		}
		// All three conditions met — short-circuit auth.
		h.ServeHTTP(w, withMeshAuthed(r))
	})
}

// mustAuthMethod is true for any HTTP method we'd otherwise gate
// behind bearer auth. /healthz and the like are already exempt
// upstream so there's nothing to short-circuit on those paths.
func mustAuthMethod(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// remoteHost extracts just the IP from r.RemoteAddr. http server
// gives "1.2.3.4:54321"; tailscale gives us bare IPs to compare.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // already bare? best-effort
	}
	return host
}

// meshAuthedKey is the context key marking a request as already
// authenticated by the mesh middleware. requireBearer reads this
// to skip its own check.
type meshAuthedKey struct{}

func withMeshAuthed(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), meshAuthedKey{}, true))
}

func isMeshAuthed(r *http.Request) bool {
	v, _ := r.Context().Value(meshAuthedKey{}).(bool)
	return v
}

// TailnetCache wraps tsnet.FetchStatus() with a small TTL cache so
// every inbound request doesn't shell out to `tailscale status`.
// Refreshes lazily: first lookup after expiry blocks briefly, the
// rest read from the cache.
//
// Carries the full Status so callers can ask both "is this IP on
// my tailnet?" (IPs map) AND "does this IP belong to my own
// tailscale account?" (Status.SameUser).
type TailnetCache struct {
	mu      sync.Mutex
	expires time.Time
	ttl     time.Duration
	ips     map[string]bool
	status  *tsnet.Status
}

func NewTailnetCache(ttl time.Duration) *TailnetCache {
	return &TailnetCache{ttl: ttl, ips: map[string]bool{}}
}

// IPs returns the set of all tailnet IPs (Self + Peers). Used by
// MeshAuth to decide "is this request from my tailnet at all?"
// before checking the shared key.
func (c *TailnetCache) IPs() map[string]bool {
	c.refresh()
	return c.ips
}

// SameUser reports whether the given IP belongs to a peer in the
// same tailscale account as this daemon. The basis for the
// zero-config auto-trust path: my own tailscale node = me.
func (c *TailnetCache) SameUser(ip string) bool {
	c.refresh()
	if c.status == nil {
		return false
	}
	return c.status.SameUser(ip)
}

func (c *TailnetCache) refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expires) {
		return
	}
	if !tsnet.Available() {
		c.ips = map[string]bool{}
		c.status = nil
		c.expires = time.Now().Add(c.ttl)
		return
	}
	st, err := tsnet.FetchStatus()
	if err != nil {
		// Keep stale cache on error; better to authenticate yesterday's
		// peers than to lock everyone out because tailscaled hiccupped.
		c.expires = time.Now().Add(c.ttl)
		return
	}
	fresh := map[string]bool{}
	if st.Self.IP != "" {
		fresh[st.Self.IP] = true
	}
	for _, p := range st.Peers {
		if p.IP != "" {
			fresh[p.IP] = true
		}
	}
	c.ips = fresh
	c.status = st
	c.expires = time.Now().Add(c.ttl)
}

// TailnetIdentityAuth is the zero-config auto-trust middleware.
// When a request arrives from a tailnet IP that belongs to the
// same tailscale account as this daemon, it's marked as
// authenticated without any header check at all. This is the
// "Plex on my own LAN" path — the operator never has to share
// keys or codes between their own machines.
//
// Falls through unchanged for non-tailnet IPs and for tailnet IPs
// belonging to a different tailscale user (shared tailnet, work
// tailnet with permissions). Those go through the next middleware
// (MeshAuth, then requireBearer).
//
// Pass cache.SameUser as the second arg so the cache lifecycle
// stays with the server (caller refreshes on its schedule).
func TailnetIdentityAuth(enabled bool, sameUser func(string) bool, h http.Handler) http.Handler {
	if !enabled {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mustAuthMethod(r) {
			h.ServeHTTP(w, r)
			return
		}
		ip := remoteHost(r)
		if ip == "" || !sameUser(ip) {
			h.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, withMeshAuthed(r))
	})
}
