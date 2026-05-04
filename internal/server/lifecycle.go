package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
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
