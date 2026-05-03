// Package server exposes a small read-only HTTP API for introspecting the store.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/noesrafa/sunny/internal/store"
)

func New(s *store.Store, log *slog.Logger) http.Handler {
	srv := &server{store: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.health)
	mux.HandleFunc("GET /agents", srv.listAgents)
	mux.HandleFunc("GET /agents/{slug}", srv.getAgent)
	mux.HandleFunc("GET /agents/{slug}/skills/{name}", srv.getSkill)
	mux.HandleFunc("GET /agents/{slug}/knowledge/{file...}", srv.getKnowledge)
	return logging(log, mux)
}

type server struct {
	store *store.Store
	log   *slog.Logger
}

func logging(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Info("request", "method", r.Method, "path", r.URL.Path)
		h.ServeHTTP(w, r)
	})
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type agentItem struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model"`
	Skills      int    `json:"skills"`
	Knowledge   int    `json:"knowledge"`
}

func summarize(a *store.Agent) agentItem {
	return agentItem{
		Slug:        a.Slug,
		Name:        a.Config.Name,
		Description: a.Config.Description,
		Model:       a.Config.Model,
		Skills:      len(a.Skills),
		Knowledge:   len(a.Knowledge),
	}
}

func (s *server) listAgents(w http.ResponseWriter, _ *http.Request) {
	agents := s.store.Agents()
	out := make([]agentItem, 0, len(agents))
	for _, a := range agents {
		out = append(out, summarize(a))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, ok := s.store.Agent(r.PathValue("slug"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	type skillItem struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	type knowledgeItem struct {
		Name string `json:"name"`
	}
	out := struct {
		Slug        string          `json:"slug"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Model       string          `json:"model"`
		Skills      []skillItem     `json:"skills"`
		Knowledge   []knowledgeItem `json:"knowledge"`
	}{
		Slug:        a.Slug,
		Name:        a.Config.Name,
		Description: a.Config.Description,
		Model:       a.Config.Model,
		Skills:      []skillItem{},
		Knowledge:   []knowledgeItem{},
	}
	for _, sk := range a.Skills {
		out.Skills = append(out.Skills, skillItem{Name: sk.Front.Name, Description: sk.Front.Description})
	}
	for _, k := range a.Knowledge {
		out.Knowledge = append(out.Knowledge, knowledgeItem{Name: k.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getSkill(w http.ResponseWriter, r *http.Request) {
	a, ok := s.store.Agent(r.PathValue("slug"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	for _, sk := range a.Skills {
		if sk.Front.Name == name {
			writeJSON(w, http.StatusOK, struct {
				Name         string   `json:"name"`
				Description  string   `json:"description"`
				AllowedTools []string `json:"allowed_tools,omitempty"`
				Body         string   `json:"body"`
			}{
				Name:         sk.Front.Name,
				Description:  sk.Front.Description,
				AllowedTools: sk.Front.AllowedTools,
				Body:         sk.Body,
			})
			return
		}
	}
	http.NotFound(w, r)
}

func (s *server) getKnowledge(w http.ResponseWriter, r *http.Request) {
	a, ok := s.store.Agent(r.PathValue("slug"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	cleaned := filepath.ToSlash(filepath.Clean(r.PathValue("file")))
	if cleaned == "." || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") || filepath.IsAbs(cleaned) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	for _, k := range a.Knowledge {
		if k.Name == cleaned {
			data, err := os.ReadFile(k.Path)
			if err != nil {
				http.Error(w, "read error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Write(data)
			return
		}
	}
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
