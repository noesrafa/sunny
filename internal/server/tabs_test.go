package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noesrafa/sunny/internal/tabs"
)

func TestRebindTabConvSwapsConvAndPreservesTab(t *testing.T) {
	impl, slug, oldConvID := newStatsImpl(t)
	tab, err := impl.tabs.Add(&tabs.Tab{AgentID: slug, ConvID: oldConvID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tabs/{id}/conversation", impl.rebindTabConv)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/tabs/"+tab.ID+"/conversation", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, body=%s", resp.StatusCode, body)
	}

	var got tabs.Tab
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != tab.ID {
		t.Errorf("tab id changed: got %s, want %s", got.ID, tab.ID)
	}
	if got.ConvID == "" || got.ConvID == oldConvID {
		t.Errorf("conv_id not rotated: got %q, old %q", got.ConvID, oldConvID)
	}
	if got.AgentID != slug {
		t.Errorf("agent id changed: got %s, want %s", got.AgentID, slug)
	}

	stored, err := impl.tabs.Get(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConvID != got.ConvID {
		t.Errorf("server-side tab not updated: stored=%s, response=%s", stored.ConvID, got.ConvID)
	}
}

func TestRebindTabConvMissingTab(t *testing.T) {
	impl, _, _ := newStatsImpl(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /tabs/{id}/conversation", impl.rebindTabConv)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/tabs/tab_does_not_exist/conversation", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
}
