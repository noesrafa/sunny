package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/noesrafa/sunny/internal/avatar"
	"github.com/noesrafa/sunny/internal/events"
)

// putAvatar accepts a raw image body (PNG/JPEG/WebP), normalizes it
// to a 512×512 lossless WebP, and stores it as avatar.webp in the
// agent's directory. Replaces any existing avatar atomically.
func (s *server) putAvatar(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a, ok := s.store.Agent(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	defer r.Body.Close()
	data, err := avatar.Process(r.Body)
	if err != nil {
		switch {
		case errors.Is(err, avatar.ErrTooLarge):
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		case errors.Is(err, avatar.ErrUnsupported):
			http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	if err := avatar.Save(a.Dir, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.store.SetHasAvatar(slug, true)
	s.publish(events.AgentUpdated, slug, "")
	w.WriteHeader(http.StatusNoContent)
}

// getAvatar serves avatar.webp directly. Public (no bearer) so the
// app can render avatars via a plain <Image> tag without juggling
// auth headers. The route is exempted by avatarExempt below.
func (s *server) getAvatar(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a, ok := s.store.Agent(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := avatar.Path(a.Dir)
	if path == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, path)
}

// deleteAvatar removes avatar.webp from the agent's directory.
// Idempotent — already-missing returns 204.
func (s *server) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a, ok := s.store.Agent(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := avatar.Remove(a.Dir); err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.store.SetHasAvatar(slug, false)
	s.publish(events.AgentUpdated, slug, "")
	w.WriteHeader(http.StatusNoContent)
}

// avatarExempt reports whether path is `GET /agents/<slug>/avatar`.
// requireBearer skips bearer enforcement for these so the app can
// render <Image source={{uri: ...}}/> without auth-header gymnastics.
// Only GETs are exempt — PUT/DELETE still require the bearer.
func avatarExempt(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	// /agents/{slug}/avatar — exactly four segments, last == "avatar".
	clean := strings.TrimPrefix(filepath.ToSlash(path), "/")
	parts := strings.Split(clean, "/")
	return len(parts) == 3 && parts[0] == "agents" && parts[2] == "avatar"
}
