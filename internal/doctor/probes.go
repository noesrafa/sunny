package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/noesrafa/sunny/internal/lifecycle"
	"github.com/noesrafa/sunny/internal/peers"
	"github.com/noesrafa/sunny/internal/secrets"
	"github.com/noesrafa/sunny/internal/tsnet"
)

// CheckClaudeCode probes for the `claude` binary. We don't try to
// detect the login state — the only reliable signal is making an
// authenticated call, which costs tokens. Installed binary is
// reported as OK; the setup flow tells the user to run `claude
// /login` to finish, and the user gets a clear error on first use
// if they skipped that step.
func CheckClaudeCode() Result {
	r := Result{Name: "claude-code"}
	bin, err := exec.LookPath("claude")
	if err != nil {
		r.Status = StatusFail
		r.Detail = "binary not on PATH"
		r.Hint = "sunny setup claude-code"
		return r
	}
	ver := briefVersion(bin, "--version")
	r.Status = StatusOK
	if ver != "" {
		r.Detail = fmt.Sprintf("v%s on PATH", ver)
	} else {
		r.Detail = "on PATH"
	}
	return r
}

// CheckOpencode probes for the `opencode` binary AND queries `auth
// list` so we can distinguish "binary present, no providers authed"
// (warn) from "binary present, at least one provider authed" (ok).
func CheckOpencode() Result {
	r := Result{Name: "opencode"}
	bin, err := exec.LookPath("opencode")
	if err != nil {
		r.Status = StatusFail
		r.Detail = "binary not on PATH"
		r.Hint = "sunny setup opencode"
		return r
	}
	ver := briefVersion(bin, "--version")
	creds := opencodeCredentialCount(bin)
	switch {
	case creds < 0:
		// Probe failed (auth list errored); treat as warn so we still
		// surface the binary but flag that something's off.
		r.Status = StatusWarn
		r.Detail = vDetail(ver, "could not query auth state")
		r.Hint = "opencode auth list"
	case creds == 0:
		r.Status = StatusWarn
		r.Detail = vDetail(ver, "no providers authed")
		r.Hint = "sunny setup opencode"
	default:
		r.Status = StatusOK
		r.Detail = vDetail(ver, fmt.Sprintf("%d provider(s) authed", creds))
	}
	return r
}

// CheckAnthropic surfaces whether secrets.yaml or ANTHROPIC_API_KEY
// has a value. We don't validate the key over the network here —
// that would burn tokens on every `sunny doctor`.
func CheckAnthropic(store *secrets.Store) Result {
	return checkAPIKey(store, "anthropic", "api_key", "ANTHROPIC_API_KEY")
}

// CheckOllama is symmetric with CheckAnthropic. base_url is optional
// (defaults to https://ollama.com); we don't surface it.
func CheckOllama(store *secrets.Store) Result {
	return checkAPIKey(store, "ollama", "api_key", "OLLAMA_API_KEY")
}

// checkAPIKey is the shared shape for providers whose readiness
// reduces to "is a key reachable?".
func checkAPIKey(store *secrets.Store, provider, field, env string) Result {
	r := Result{Name: provider}
	if hasKey(store, provider, field, env) {
		r.Status = StatusOK
		r.Detail = field + " configured"
		return r
	}
	r.Status = StatusFail
	r.Detail = "no API key"
	r.Hint = "sunny setup " + provider
	return r
}

// CheckDaemon reads the on-disk state file and verifies the recorded
// PID is alive. We do not hit /healthz here — that requires the
// bearer token and adds a network dependency to a "doctor" command.
func CheckDaemon(root string) Result {
	r := Result{Name: "daemon"}
	paths := lifecycle.PathsFor(root)
	state, err := paths.LoadState()
	if err != nil {
		r.Status = StatusFail
		r.Detail = "not running"
		r.Hint = "sunny start"
		return r
	}
	if !lifecycle.IsAlive(state.PID) {
		r.Status = StatusFail
		r.Detail = fmt.Sprintf("stale state.json (pid %d gone)", state.PID)
		r.Hint = "sunny start"
		return r
	}
	uptime := time.Since(state.StartedAt).Round(time.Second)
	r.Status = StatusOK
	r.Detail = fmt.Sprintf("pid %d, %s, up %s", state.PID, state.Addr, humanDuration(uptime))
	return r
}

// CheckTailscale returns nil when tailscale isn't installed — sunny
// works fine without it and we don't want to nag solo-install users.
// When the CLI exists we probe LocalIP() and surface either the
// tailnet IP (ok) or the friendly error from the CLI (warn).
func CheckTailscale() *Result {
	if !tsnet.Available() {
		return nil
	}
	r := Result{Name: "tailscale"}
	ip, err := tsnet.LocalIP()
	if err != nil {
		r.Status = StatusWarn
		r.Detail = err.Error()
		r.Hint = "tailscale up"
		return &r
	}
	r.Status = StatusOK
	r.Detail = "tailnet IP " + ip + " — try: sunny peers scan"
	return &r
}

// CheckPeers reads ~/.sunny/peers.yaml and probes each remote with a
// short-timeout GET /agents (which doubles as an auth check —
// /healthz is unauthenticated and would only prove the URL resolves).
// Returns one Result per remote peer, empty when no peers.yaml.
func CheckPeers(root string) []Result {
	r, err := peers.Load(root, "127.0.0.1:7777", "")
	if err != nil {
		return []Result{{Name: "peers", Status: StatusFail, Detail: err.Error(), Hint: "fix ~/.sunny/peers.yaml"}}
	}
	out := make([]Result, 0, len(r.Remote))
	for _, p := range r.Remote {
		out = append(out, probePeer(p))
	}
	return out
}

func probePeer(p peers.Peer) Result {
	r := Result{Name: p.Name}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+"/agents", nil)
	if err != nil {
		r.Status = StatusFail
		r.Detail = err.Error()
		return r
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.Status = StatusFail
		r.Detail = fmt.Sprintf("%s — %s", p.URL, err.Error())
		r.Hint = "is the remote daemon running?"
		return r
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		r.Status = StatusFail
		r.Detail = fmt.Sprintf("%s — 401 Unauthorized (token rejected)", p.URL)
		r.Hint = "sunny peers remove " + p.Name + " && sunny peers add " + p.Name + " " + p.URL
		return r
	}
	if resp.StatusCode != http.StatusOK {
		r.Status = StatusWarn
		r.Detail = fmt.Sprintf("%s — HTTP %d", p.URL, resp.StatusCode)
		return r
	}
	r.Status = StatusOK
	r.Detail = fmt.Sprintf("%s — reachable", p.URL)
	return r
}

// CheckRuntime verifies ~/.sunny exists and reports a coarse count
// of agents and conversations. It's the closest thing to "did
// bootstrap run cleanly" we can answer without re-walking the store.
func CheckRuntime(root string) Result {
	r := Result{Name: "runtime"}
	if _, err := os.Stat(root); err != nil {
		r.Status = StatusFail
		r.Detail = fmt.Sprintf("missing: %s", root)
		r.Hint = "sunny start"
		return r
	}
	agents, convs := countAgentsAndConvs(root)
	r.Status = StatusOK
	r.Detail = fmt.Sprintf("%s — %d agents, %d conversations", root, agents, convs)
	return r
}
