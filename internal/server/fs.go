package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GET /fs/list?path=<absolute path>
//
// Lists immediate subdirectories at path. When path is empty, defaults
// to the daemon-user's home dir — gives the TUI a sane starting point
// when picking a cwd for a remote session, where the local filesystem
// is irrelevant.
//
// Filters dotfiles (hidden by convention) and only returns directories
// — files would be noise for the use case (cwd picker).
//
// No sandbox: the daemon process runs as the user, so any path the
// user could read with `ls` is fair game. The bearer auth in front
// of every route is the trust boundary.
type fsListResponse struct {
	Path    string       `json:"path"`
	Parent  string       `json:"parent,omitempty"` // "" when path is filesystem root
	Entries []fsListItem `json:"entries"`
}

type fsListItem struct {
	Name string `json:"name"`
}

func (s *server) fsList(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			http.Error(w, "no default path: home dir not available", http.StatusInternalServerError)
			return
		}
		path = home
	}
	if !filepath.IsAbs(path) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}
	path = filepath.Clean(path)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "path not found", http.StatusNotFound)
			return
		}
		http.Error(w, "stat: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !info.IsDir() {
		http.Error(w, "not a directory", http.StatusBadRequest)
		return
	}

	items, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, "read dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	entries := make([]fsListItem, 0, len(items))
	for _, it := range items {
		name := it.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Resolve symlinks lazily — IsDir() on a DirEntry doesn't
		// follow them, but the picker treats symlinked dirs as dirs.
		if it.IsDir() {
			entries = append(entries, fsListItem{Name: name})
			continue
		}
		if it.Type()&os.ModeSymlink != 0 {
			if sub, err := os.Stat(filepath.Join(path, name)); err == nil && sub.IsDir() {
				entries = append(entries, fsListItem{Name: name})
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	writeJSON(w, http.StatusOK, fsListResponse{
		Path:    path,
		Parent:  parent,
		Entries: entries,
	})
}
