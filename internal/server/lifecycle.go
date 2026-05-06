package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// versionResponse is what GET /sunny/version returns. Distinct from
// /sunny/identity (no auth, mesh-fingerprint focused) so the app can
// poll for a started_at change after restart/update without dragging
// the public identity payload along.
type versionResponse struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
}

func (s *server) getSunnyVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, versionResponse{
		Version:   s.version,
		StartedAt: s.startedAt,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	})
}

// versionCheckResponse is the body of GET /sunny/version/check. The
// daemon compares its linker-set version against the latest GitHub
// release tag and reports whether an update is worth offering. Errors
// are non-fatal — `error` carries the reason and `update_available`
// stays false so the UI can stay quiet rather than nag.
type versionCheckResponse struct {
	Current         string    `json:"current"`
	Latest          string    `json:"latest,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	ReleaseURL      string    `json:"release_url,omitempty"`
	PublishedAt     string    `json:"published_at,omitempty"`
	CheckedAt       time.Time `json:"checked_at"`
	Error           string    `json:"error,omitempty"`
}

// updateCheckCache rate-limits external GitHub calls so a polling TUI
// (every minute, say) doesn't burn the unauth API budget (60 req/hr
// per IP). Five minutes is plenty for "is there an update?" UX —
// users who pressed the button moments ago aren't surprised by a
// stale result.
type updateCheckCache struct {
	mu        sync.Mutex
	resp      versionCheckResponse
	checkedAt time.Time
}

const (
	updateCheckTTL = 5 * time.Minute
	updateCheckURL = "https://api.github.com/repos/noesrafa/sunny/releases/latest"
)

var updateCheck updateCheckCache

func (s *server) getSunnyVersionCheck(w http.ResponseWriter, r *http.Request) {
	updateCheck.mu.Lock()
	if time.Since(updateCheck.checkedAt) < updateCheckTTL && !updateCheck.checkedAt.IsZero() {
		out := updateCheck.resp
		updateCheck.mu.Unlock()
		// Stamp current from the live daemon so the cache is portable
		// across daemon restarts that share a binary.
		out.Current = s.version
		out.UpdateAvailable = isNewer(out.Latest, s.version)
		writeJSON(w, http.StatusOK, out)
		return
	}
	updateCheck.mu.Unlock()

	out := versionCheckResponse{
		Current:   s.version,
		CheckedAt: time.Now().UTC(),
	}
	latest, releaseURL, publishedAt, err := fetchLatestRelease(r.Context())
	if err != nil {
		out.Error = err.Error()
		writeJSON(w, http.StatusOK, out) // 200 with embedded error — easier on clients
		return
	}
	out.Latest = latest
	out.ReleaseURL = releaseURL
	out.PublishedAt = publishedAt
	out.UpdateAvailable = isNewer(latest, s.version)

	updateCheck.mu.Lock()
	updateCheck.resp = out
	updateCheck.checkedAt = out.CheckedAt
	updateCheck.mu.Unlock()

	writeJSON(w, http.StatusOK, out)
}

// fetchLatestRelease hits the GitHub API for the most recent release.
// Anonymous, rate-limited to 60/hr per IP — paired with the 5-min
// cache that's a hard ceiling of 12 calls/hr from one daemon, leaving
// plenty of headroom.
func fetchLatestRelease(ctx context.Context) (tag, htmlURL, publishedAt string, err error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, updateCheckURL, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", "", fmt.Errorf("decode: %w", err)
	}
	if body.Draft || body.Prerelease {
		// Skip drafts/prereleases — the user is on a stable channel.
		// A future release_channel knob can opt in.
		return "", body.HTMLURL, body.PublishedAt, fmt.Errorf("latest release is draft/prerelease")
	}
	return body.TagName, body.HTMLURL, body.PublishedAt, nil
}

// isNewer reports whether latest > current using semver-ish ordering.
// Tolerates a leading "v" on either side and falls back to plain
// string compare for anything not parseable. "dev" or empty current
// always reports false — local builds shouldn't trigger update prompts.
func isNewer(latest, current string) bool {
	latest = strings.TrimSpace(latest)
	current = strings.TrimSpace(current)
	if latest == "" || current == "" || current == "dev" {
		return false
	}
	la := parseSemver(latest)
	cu := parseSemver(current)
	if la == nil || cu == nil {
		return latest != current
	}
	for i := 0; i < 3; i++ {
		if la[i] != cu[i] {
			return la[i] > cu[i]
		}
	}
	return false
}

// parseSemver parses "vX.Y.Z" (or "X.Y.Z") into a 3-int slice. Returns
// nil on malformed input so the caller can fall back to string compare.
func parseSemver(s string) []int {
	s = strings.TrimPrefix(s, "v")
	// Drop pre-release / build suffix — "1.2.3-rc1+meta" → "1.2.3".
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// lifecycleAck is the 202 body for restart/update. started_at is the
// CURRENT daemon's boot time — clients poll /sunny/version after this
// call and consider the action complete when started_at changes.
// Stop returns the same shape (with a "stop" flag elsewhere).
type lifecycleAck struct {
	StartedAt time.Time `json:"started_at"`
}

// postSunnyRestart spawns a detached `sunny restart` helper that will
// SIGTERM this daemon and then start a fresh one. The handler does
// NOT terminate the current process — that's the helper's job. If the
// spawn fails, the daemon stays alive and the handler returns 500.
func (s *server) postSunnyRestart(w http.ResponseWriter, _ *http.Request) {
	if err := s.spawnLifecycleHelper("restart"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, lifecycleAck{StartedAt: s.startedAt})
}

// postSunnyUpdate spawns a detached `sunny update` helper that does
// brew upgrade (or GitHub release fallback) and then restart. Same
// safety contract as restart: handler doesn't kill the daemon, the
// helper does, and the helper guarantees a daemon comes back up.
func (s *server) postSunnyUpdate(w http.ResponseWriter, _ *http.Request) {
	if err := s.spawnLifecycleHelper("update"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, lifecycleAck{StartedAt: s.startedAt})
}

// postSunnyStop spawns a detached `sunny stop` helper. Requires the
// caller to send {"confirm": true} in the body — there's no auto-
// recovery after stop, so the client UI is responsible for warning
// the user before sending this.
func (s *server) postSunnyStop(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !body.Confirm {
		http.Error(w, `stop requires {"confirm": true}`, http.StatusBadRequest)
		return
	}
	if err := s.spawnLifecycleHelper("stop"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, lifecycleAck{StartedAt: s.startedAt})
}

// spawnLifecycleHelper launches a detached `sunny <subcmd>` process.
// The helper is detached (Setsid) so it survives this daemon's
// imminent SIGTERM and can finish its restart/update/stop dance
// independently. We forward --root so the helper targets this exact
// daemon (important when running multiple roots side-by-side).
func (s *server) spawnLifecycleHelper(subcmd string) error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}
	args := []string{subcmd}
	if s.root != "" {
		args = append(args, "--root", s.root)
	}
	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn sunny %s: %w", subcmd, err)
	}
	if err := cmd.Process.Release(); err != nil {
		s.log.Warn("release lifecycle helper", "subcmd", subcmd, "err", err.Error())
	}
	return nil
}
