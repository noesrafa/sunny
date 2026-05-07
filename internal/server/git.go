package server

import (
	"net/http"
	"path/filepath"

	"github.com/noesrafa/sunny/internal/git"
)

// gitStatus answers GET /git/status?cwd=<abs-path> with the active
// branch + per-bucket change counts for the working tree at cwd.
//
// Returns zero values inside a 200 when cwd isn't a repo / git is
// unavailable; the TUI renders an empty pill in that case. Treats a
// missing or non-absolute cwd as 400 since callers always know what
// they're asking about.
func (s *server) gitStatus(w http.ResponseWriter, r *http.Request) {
	cwd, ok := requireGitCwd(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Branch  string          `json:"branch"`
		Changes git.ChangeStats `json:"changes"`
	}{
		Branch:  git.Branch(cwd),
		Changes: git.Changes(cwd),
	})
}

// gitFiles answers GET /git/files?cwd=<abs-path> with the changed-file
// list — the diff dialog's left pane. Empty array (not null) on a clean
// tree, so the TUI never has to nil-check.
func (s *server) gitFiles(w http.ResponseWriter, r *http.Request) {
	cwd, ok := requireGitCwd(w, r)
	if !ok {
		return
	}
	files := git.Files(cwd)
	if files == nil {
		files = []git.File{}
	}
	writeJSON(w, http.StatusOK, files)
}

// gitDiff answers GET /git/diff?cwd=<abs-path>&path=<rel-file> with
// the raw unified diff body. Untracked files come back as "+ line"
// fabrications (see git.Diff). The TUI colorizes; we keep the body
// raw so multiple viewers can style independently.
func (s *server) gitDiff(w http.ResponseWriter, r *http.Request) {
	cwd, ok := requireGitCwd(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	body, err := git.Diff(cwd, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Body string `json:"body"`
	}{Body: body})
}

// requireGitCwd extracts and validates the `cwd` query parameter
// shared by every /git/* route. Empty or relative cwds are rejected
// with 400 — the daemon never guesses a working directory.
func requireGitCwd(w http.ResponseWriter, r *http.Request) (string, bool) {
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "missing cwd", http.StatusBadRequest)
		return "", false
	}
	if !filepath.IsAbs(cwd) {
		http.Error(w, "cwd must be absolute", http.StatusBadRequest)
		return "", false
	}
	return cwd, true
}
