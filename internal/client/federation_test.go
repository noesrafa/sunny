package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noesrafa/sunny/internal/peers"
)

func newFakeDaemon(t *testing.T, agents []AgentSummary, fail bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agents" {
			http.NotFound(w, r)
			return
		}
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(agents)
	}))
}

func TestFederation_ListAgents_FanOut(t *testing.T) {
	local := newFakeDaemon(t, []AgentSummary{
		{ID: "agt_a", Name: "Alpha"},
		{ID: "agt_b", Name: "Beta"},
	}, false)
	defer local.Close()
	vps := newFakeDaemon(t, []AgentSummary{
		{ID: "agt_z", Name: "Zoro"},
	}, false)
	defer vps.Close()

	roster := peers.Roster{
		Local:  peers.Peer{Name: "local", URL: local.URL, Token: "t"},
		Remote: []peers.Peer{{Name: "vps", URL: vps.URL, Token: "u"}},
	}
	fed := NewFederation(roster)

	got := fed.ListAgents(context.Background())
	if len(got.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", got.Errors)
	}
	if len(got.Agents) != 3 {
		t.Fatalf("got %d agents, want 3 — %+v", len(got.Agents), got.Agents)
	}
	// Sorted by (host, name): local/Alpha, local/Beta, vps/Zoro
	want := []struct{ host, name string }{
		{"local", "Alpha"},
		{"local", "Beta"},
		{"vps", "Zoro"},
	}
	for i, w := range want {
		if got.Agents[i].Host != w.host || got.Agents[i].Name != w.name {
			t.Errorf("[%d] got %s/%s, want %s/%s", i, got.Agents[i].Host, got.Agents[i].Name, w.host, w.name)
		}
	}
}

func TestFederation_ListAgents_PeerFailureIsolated(t *testing.T) {
	local := newFakeDaemon(t, []AgentSummary{{ID: "agt_a", Name: "Alpha"}}, false)
	defer local.Close()
	vps := newFakeDaemon(t, nil, true) // 500
	defer vps.Close()

	roster := peers.Roster{
		Local:  peers.Peer{Name: "local", URL: local.URL, Token: "t"},
		Remote: []peers.Peer{{Name: "vps", URL: vps.URL, Token: "u"}},
	}
	fed := NewFederation(roster)

	got := fed.ListAgents(context.Background())
	if len(got.Agents) != 1 || got.Agents[0].Host != "local" {
		t.Errorf("local should still surface, got %+v", got.Agents)
	}
	if got.Errors["vps"] == nil {
		t.Errorf("vps error should land in Errors map, got %v", got.Errors)
	}
	if got.Errors["local"] != nil {
		t.Errorf("local should not error, got %v", got.Errors["local"])
	}
}

func TestFederation_For(t *testing.T) {
	roster := peers.Roster{
		Local:  peers.Peer{Name: "local", URL: "http://x", Token: "t"},
		Remote: []peers.Peer{{Name: "vps", URL: "http://y", Token: "u"}},
	}
	fed := NewFederation(roster)

	if fed.For("local") == nil {
		t.Errorf("For(local) should resolve")
	}
	if fed.For("") == nil {
		t.Errorf("For(\"\") should fall back to local")
	}
	if fed.For("vps") == nil {
		t.Errorf("For(vps) should resolve")
	}
	if fed.For("missing") != nil {
		t.Errorf("For(missing) should be nil, got %v", fed.For("missing"))
	}
	names := fed.Names()
	if len(names) != 2 || names[0] != "local" || names[1] != "vps" {
		t.Errorf("Names() = %v, want [local, vps]", names)
	}
}
