package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RunStatus mirrors runs.Status. Keeping the type here lets the TUI
// import only client/, not the daemon-side runs package.
type RunStatus string

const (
	RunStopped RunStatus = "stopped"
	RunRunning RunStatus = "running"
	RunExited  RunStatus = "exited"
	RunFailed  RunStatus = "failed"
)

// RunState is the live state portion of a Run view.
type RunState struct {
	PID       int        `json:"pid,omitempty"`
	Status    RunStatus  `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	ExitedAt  *time.Time `json:"exited_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
}

// Run is the wire shape of one background-service definition + its
// runtime state. Mirrors server.runView.
type Run struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Cwd       string    `json:"cwd"`
	Command   string    `json:"command"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	State     RunState  `json:"state"`
}

// CreateRunRequest is the body of POST /runs.
type CreateRunRequest struct {
	Name    string `json:"name"`
	Cwd     string `json:"cwd"`
	Command string `json:"command"`
}

// PatchRunRequest is the body of PATCH /runs/{id}. nil pointers
// leave the corresponding field untouched.
type PatchRunRequest struct {
	Name    *string `json:"name,omitempty"`
	Cwd     *string `json:"cwd,omitempty"`
	Command *string `json:"command,omitempty"`
}

// LogLine is one line of a run's captured stdout/stderr.
type LogLine struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

func (c *Client) ListRuns(ctx context.Context) ([]Run, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/runs", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("GET /runs", resp)
	}
	var out []Run
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetRun(ctx context.Context, id string) (*Run, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/runs/"+id, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("run %q not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("GET /runs/"+id, resp)
	}
	var out Run
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CreateRun(ctx context.Context, body CreateRunRequest) (*Run, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, "/runs", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, errorFromBody("POST /runs", resp)
	}
	var out Run
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PatchRun(ctx context.Context, id string, body PatchRunRequest) (*Run, error) {
	resp, err := c.doJSON(ctx, http.MethodPatch, "/runs/"+id, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("PATCH /runs/"+id, resp)
	}
	var out Run
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRun stops the run if it's alive, then removes the
// definition. Idempotent on missing ids.
func (c *Client) DeleteRun(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/runs/"+id, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return errorFromBody("DELETE /runs/"+id, resp)
	}
	return nil
}

func (c *Client) StartRun(ctx context.Context, id string) (*Run, error) {
	return c.runLifecycle(ctx, id, "start")
}

func (c *Client) StopRun(ctx context.Context, id string) (*Run, error) {
	return c.runLifecycle(ctx, id, "stop")
}

func (c *Client) RestartRun(ctx context.Context, id string) (*Run, error) {
	return c.runLifecycle(ctx, id, "restart")
}

// runLifecycle wraps the three POST endpoints whose semantics are
// identical: 200 + the post-action Run, or an error body.
func (c *Client) runLifecycle(ctx context.Context, id, action string) (*Run, error) {
	path := "/runs/" + id + "/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("POST "+path, resp)
	}
	var out Run
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RunLogs fetches a snapshot of the run's log file. tail<=0 returns
// the whole file; otherwise the last N lines.
func (c *Client) RunLogs(ctx context.Context, id string, tail int) ([]LogLine, error) {
	url := c.base + "/runs/" + id + "/logs"
	if tail > 0 {
		url += "?tail=" + strconv.Itoa(tail)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("GET /runs/"+id+"/logs", resp)
	}
	var out []LogLine
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// WatchRunLogs opens an SSE stream of log lines from the run.
// Returns a channel that drains each line as it arrives. The
// channel closes when the run exits, ctx is cancelled, or the
// connection drops.
func (c *Client) WatchRunLogs(ctx context.Context, id string) (<-chan LogLine, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/runs/"+id+"/logs/watch", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, errorFromBody("GET /runs/"+id+"/logs/watch", resp)
	}
	out := make(chan LogLine, 64)
	go pumpRunLogs(ctx, resp.Body, out)
	return out, nil
}

// pumpRunLogs parses the SSE stream into LogLine values. We track
// the current event name so payloads tagged `event: end` shut the
// channel cleanly without emitting bogus lines.
func pumpRunLogs(ctx context.Context, body io.ReadCloser, out chan<- LogLine) {
	defer close(out)
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<14), 1<<20)
	var eventName string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if eventName != "log" {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			var ll LogLine
			if err := json.Unmarshal([]byte(payload), &ll); err != nil {
				continue
			}
			select {
			case out <- ll:
			case <-ctx.Done():
				return
			}
		case line == "":
			eventName = ""
		}
	}
}
