package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noesrafa/sunny/internal/client"
)

// TestSendBuildsWireWithExpandedAttachments exercises the full
// Session.send path against a fake daemon and asserts the wire body
// carries the attachment's absolute path in place of "[Image #N]".
//
// Guards against regressions where buildWireMessages stops receiving
// s.Attachments (the original bug — the marker survived to the model).
func TestSendBuildsWireWithExpandedAttachments(t *testing.T) {
	var got client.TurnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the /turns POST we care about; ignore anything else.
		if !strings.HasSuffix(r.URL.Path, "/turns") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode turn body: %v\n%s", err, body)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(client.SendTurnResult{ConvID: "conv_x", UserSeq: 1})
	}))
	defer srv.Close()

	c := client.NewFromBase(srv.URL, "test-token")

	s := &Session{
		ID:      "sess_x",
		ConvID:  "conv_x",
		agentID: "agt_test",
	}
	s.c = c

	// Simulate a paste + an attachment in the draft.
	idx := s.AddAttachment("/Users/mac/Desktop/screenshot.png", "image/png")
	if idx != 1 {
		t.Fatalf("AddAttachment idx = %d, want 1", idx)
	}

	if _, err := s.send(context.Background(), "look at [Image #1] please", false); err != nil {
		t.Fatalf("send: %v", err)
	}

	if len(got.Messages) != 1 {
		t.Fatalf("want 1 wire message, got %d", len(got.Messages))
	}
	want := "look at /Users/mac/Desktop/screenshot.png please"
	if got.Messages[0].Content != want {
		t.Fatalf("wire content = %q, want %q", got.Messages[0].Content, want)
	}
}
