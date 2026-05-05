package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	evts "github.com/noesrafa/sunny/internal/events"
	"github.com/noesrafa/sunny/internal/runs"
)

// runView is the wire shape for one run: persisted definition +
// runtime state (status, pid, exit code). The TUI's sidebar pulls
// this and renders the colored status pill from State.Status.
type runView struct {
	*runs.Run
	State runs.State `json:"state"`
}

func (s *server) makeRunView(r *runs.Run) runView {
	st := runs.State{Status: runs.StatusStopped}
	if s.runtime != nil {
		st = s.runtime.State(r.ID)
	}
	return runView{Run: r, State: st}
}

// listRuns returns every run definition with its current state.
//
// Path: GET /runs
func (s *server) listRuns(w http.ResponseWriter, _ *http.Request) {
	if s.runs == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	defs := s.runs.List()
	out := make([]runView, 0, len(defs))
	for _, d := range defs {
		out = append(out, s.makeRunView(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// getRun returns one run by id.
//
// Path: GET /runs/{id}
func (s *server) getRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	def, err := s.runs.Get(id)
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.makeRunView(def))
}

// createRunRequest is the body of POST /runs.
type createRunRequest struct {
	Name    string `json:"name"`
	Cwd     string `json:"cwd"`
	Command string `json:"command"`
}

// createRun scaffolds a new run. The new run starts in "stopped"
// state — the caller must POST /runs/{id}/start to fire it.
//
// Path: POST /runs
func (s *server) createRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	var req createRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	def, err := s.runs.Create(req.Name, req.Cwd, req.Command)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publishRun(evts.RunCreated, def.ID)
	writeJSON(w, http.StatusCreated, s.makeRunView(def))
}

// patchRunRequest carries the editable fields. nil = leave alone.
type patchRunRequest struct {
	Name    *string `json:"name,omitempty"`
	Cwd     *string `json:"cwd,omitempty"`
	Command *string `json:"command,omitempty"`
}

// updateRun applies a partial update to an existing run. Editing
// while the run is live is allowed — the change takes effect on the
// next start (or the next restart if the user hits restart).
//
// Path: PATCH /runs/{id}
func (s *server) updateRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	var req patchRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	def, err := s.runs.Update(id, runs.Patch{Name: req.Name, Cwd: req.Cwd, Command: req.Command})
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publishRun(evts.RunUpdated, def.ID)
	writeJSON(w, http.StatusOK, s.makeRunView(def))
}

// deleteRun stops the run if it's alive, then drops the definition
// from disk. Idempotent on missing ids.
//
// Path: DELETE /runs/{id}
func (s *server) deleteRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if s.runtime != nil {
		if err := s.runtime.Stop(id); err != nil {
			http.Error(w, "stop before delete: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.runtime.Forget(id)
	}
	if err := s.runs.Remove(id); err != nil && !errors.Is(err, runs.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishRun(evts.RunDeleted, id)
	w.WriteHeader(http.StatusNoContent)
}

// startRun fires the run via Runtime.Start. Returns the post-start
// view (status=running with the new pid).
//
// Path: POST /runs/{id}/start
func (s *server) startRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.runtime == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	def, err := s.runs.Get(id)
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.runtime.Start(id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.makeRunView(def))
}

// stopRun signals the run's process group with SIGTERM and waits up
// to 5s for clean shutdown before SIGKILL. Synchronous — the
// response returns once the run is no longer running.
//
// Path: POST /runs/{id}/stop
func (s *server) stopRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.runtime == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	def, err := s.runs.Get(id)
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.runtime.Stop(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.makeRunView(def))
}

// restartRun stops then starts. Synchronous like stopRun.
//
// Path: POST /runs/{id}/restart
func (s *server) restartRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.runtime == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	def, err := s.runs.Get(id)
	if err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.runtime.Restart(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.makeRunView(def))
}

// getRunLogs returns a snapshot of the run's log file. ?tail=N
// limits to the last N lines (default 200; 0 = all).
//
// Path: GET /runs/{id}/logs
func (s *server) getRunLogs(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.runtime == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if _, err := s.runs.Get(id); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tail := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			tail = n
		}
	}
	lines, err := s.runtime.TailLog(id, tail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if lines == nil {
		lines = []runs.LogLine{}
	}
	writeJSON(w, http.StatusOK, lines)
}

// watchRunLogs streams new log lines via SSE. Each event has type
// "log" and a JSON-encoded LogLine in data. A ping is emitted every
// 30s so flaky middleboxes don't reap idle connections. The stream
// ends when the run exits (sink is closed by the supervisor).
//
// Path: GET /runs/{id}/logs/watch
func (s *server) watchRunLogs(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.runtime == nil {
		http.Error(w, "runs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if _, err := s.runs.Get(id); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, release := s.runtime.WatchLog(id)
	if ch == nil {
		// Run isn't live; emit a single end event so the client
		// knows there's nothing to stream and disconnects cleanly.
		fmt.Fprintf(w, "event: end\ndata: {}\n\n")
		flusher.Flush()
		return
	}
	defer release()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			payload, _ := json.Marshal(line)
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// publishRun broadcasts a run.* event on the global bus. RunID lets
// subscribers refresh just that run's row instead of refetching the
// whole list.
func (s *server) publishRun(t evts.Type, id string) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(evts.Event{Type: t, RunID: id})
}
