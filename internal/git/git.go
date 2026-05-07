// Package git is the daemon-side git interface. Everything that runs
// `git` in sunny goes through here so the TUI can stay machine-agnostic
// — a session bound to a remote peer reaches the right repo because
// the daemon at that peer owns the working tree.
//
// Three operations cover the existing UI surface: branch + change
// summary (sidebar pill, diff dialog title), changed-file list (diff
// dialog left pane), and per-file diff text (diff dialog right pane).
// All three return zero values when the cwd isn't a git repo or git
// isn't installed — no error so callers can render an empty state
// without special-casing.
package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ChangeStats summarizes the working tree against HEAD. Counts are
// file-level — one file with mixed staged + unstaged edits is one
// Modified. A file is bucketed by its most "destructive" status code.
type ChangeStats struct {
	Added     int `json:"added"`
	Modified  int `json:"modified"`
	Deleted   int `json:"deleted"`
	Untracked int `json:"untracked"`
}

// Total is the file count across every bucket.
func (c ChangeStats) Total() int {
	return c.Added + c.Modified + c.Deleted + c.Untracked
}

// Dirty reports whether anything is pending.
func (c ChangeStats) Dirty() bool { return c.Total() > 0 }

// File is one entry in the working-tree change list. Status is the raw
// `git status --porcelain` two-char prefix (`??`, ` M`, `MM`, `A `, etc.).
type File struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Bucket string `json:"bucket"` // "added" | "modified" | "deleted" | "untracked"
}

// Branch returns the current branch of cwd, or "" when cwd is not a
// git repo, git is unavailable, or a detached HEAD.
func Branch(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Changes returns the per-bucket count of pending edits in cwd.
// Empty result on non-repos / git failure.
func Changes(cwd string) ChangeStats {
	out, err := exec.Command("git", "-C", cwd, "status", "--porcelain").Output()
	if err != nil {
		return ChangeStats{}
	}
	var c ChangeStats
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 3 {
			continue
		}
		st := line[:2]
		if st == "??" {
			c.Untracked++
			continue
		}
		x, y := rune(st[0]), rune(st[1])
		switch {
		case x == 'D' || y == 'D':
			c.Deleted++
		case x == 'M' || y == 'M' || x == 'R' || y == 'R' || x == 'C' || y == 'C':
			c.Modified++
		case x == 'A' || y == 'A':
			c.Added++
		default:
			c.Modified++
		}
	}
	return c
}

// Files returns the changed-file list for cwd, sorted with modified
// before added/deleted/untracked so the user lands on "intentional"
// edits first.
func Files(cwd string) []File {
	if cwd == "" {
		return nil
	}
	out, err := exec.Command("git", "-C", cwd, "status", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var files []File
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		st := line[:2]
		path := line[3:]
		// Rename entries look like "old -> new"; we want the destination.
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		files = append(files, File{Path: path, Status: st, Bucket: classifyStatus(st)})
	}
	sort.SliceStable(files, func(i, j int) bool {
		oi, oj := bucketOrder(files[i].Bucket), bucketOrder(files[j].Bucket)
		if oi != oj {
			return oi < oj
		}
		return files[i].Path < files[j].Path
	})
	return files
}

// Diff returns the unified diff of `path` (relative to cwd) against
// HEAD. For untracked files we fabricate a "+ line" body from the
// raw contents — mirroring how a hypothetical staging would render.
//
// The TUI side adds ANSI colors; this layer keeps the body raw so it
// is portable across viewers.
func Diff(cwd, path string) (string, error) {
	if cwd == "" || path == "" {
		return "", nil
	}
	// Untracked files: read directly. `git diff` ignores them since
	// they're not in the index.
	if isUntracked(cwd, path) {
		body, err := os.ReadFile(filepath.Join(cwd, path))
		if err != nil {
			return "", err
		}
		var b strings.Builder
		b.WriteString("untracked file: " + path + "\n")
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			b.WriteString("+ ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	// `git diff HEAD` covers staged + unstaged relative to the last
	// commit — exactly what the user wants to see before committing.
	// `color.ui=never` so the body never carries embedded ANSI.
	out, err := exec.Command("git", "-C", cwd, "-c", "color.ui=never", "diff", "HEAD", "--", path).Output()
	if err != nil || len(out) == 0 {
		// Fresh repo (no HEAD yet) or weird state — fall back to no-ref form.
		out, _ = exec.Command("git", "-C", cwd, "-c", "color.ui=never", "diff", "--", path).Output()
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func isUntracked(cwd, path string) bool {
	out, err := exec.Command("git", "-C", cwd, "status", "--porcelain", "--", path).Output()
	if err != nil {
		return false
	}
	line := strings.TrimRight(string(out), "\n")
	return strings.HasPrefix(line, "??")
}

func classifyStatus(st string) string {
	if st == "??" {
		return "untracked"
	}
	if len(st) < 2 {
		return "modified"
	}
	x, y := rune(st[0]), rune(st[1])
	switch {
	case x == 'D' || y == 'D':
		return "deleted"
	case x == 'M' || y == 'M' || x == 'R' || y == 'R' || x == 'C' || y == 'C':
		return "modified"
	case x == 'A' || y == 'A':
		return "added"
	}
	return "modified"
}

func bucketOrder(b string) int {
	switch b {
	case "modified":
		return 0
	case "added":
		return 1
	case "deleted":
		return 2
	case "untracked":
		return 3
	}
	return 4
}
