package session

import (
	"strings"
	"testing"
)

func TestExpandImageMarkersBasic(t *testing.T) {
	atts := []Attachment{
		{Index: 1, Path: "/Users/mac/Desktop/foo.png", MediaType: "image/png"},
	}
	got := ExpandImageMarkers("look [Image #1] please", atts)
	want := "look /Users/mac/Desktop/foo.png please"
	if got != want {
		t.Fatalf("expand = %q, want %q", got, want)
	}
}

func TestExpandImageMarkersMultiple(t *testing.T) {
	atts := []Attachment{
		{Index: 1, Path: "/a.png"},
		{Index: 2, Path: "/b.png"},
		{Index: 3, Path: "/c.png"},
	}
	got := ExpandImageMarkers("[Image #1] then [Image #2] and [Image #3]", atts)
	want := "/a.png then /b.png and /c.png"
	if got != want {
		t.Fatalf("expand multi = %q", got)
	}
}

func TestExpandImageMarkersOrphanSurvives(t *testing.T) {
	// Index 9 has no backing attachment. Marker stays literal so the user
	// can see something is off instead of the text vanishing.
	got := ExpandImageMarkers("before [Image #9] after", nil)
	if got != "before [Image #9] after" {
		t.Fatalf("orphan stripped: %q", got)
	}
}

func TestExpandImageMarkersNoMarkersNoOp(t *testing.T) {
	atts := []Attachment{{Index: 1, Path: "/x.png"}}
	got := ExpandImageMarkers("no markers here", atts)
	if got != "no markers here" {
		t.Fatalf("mutated input: %q", got)
	}
}

func TestExpandImageMarkersUnknownIndexKept(t *testing.T) {
	// Index 1 is known, index 7 isn't. The known one expands; the
	// orphan stays as text so it's visible (same contract as pasted).
	atts := []Attachment{{Index: 1, Path: "/known.png"}}
	got := ExpandImageMarkers("[Image #1] vs [Image #7]", atts)
	want := "/known.png vs [Image #7]"
	if got != want {
		t.Fatalf("mixed = %q, want %q", got, want)
	}
}

func TestBuildWireMessagesExpandsImageMarkers(t *testing.T) {
	items := []Item{
		UserItem{
			Text: "history [Image #1]",
			Attachments: []Attachment{
				{Index: 1, Path: "/old.png", MediaType: "image/png"},
			},
		},
		AssistantTextItem{Text: "ack"},
	}
	currentAtts := []Attachment{
		{Index: 1, Path: "/new.png", MediaType: "image/png"},
		{Index: 2, Path: "/two.png", MediaType: "image/png"},
	}
	wire := buildWireMessages(items, "look [Image #1] [Image #2]", nil, currentAtts)
	if len(wire) != 3 {
		t.Fatalf("want 3 wire msgs, got %d", len(wire))
	}
	if !strings.Contains(wire[0].Content, "/old.png") {
		t.Fatalf("history user wasn't expanded: %q", wire[0].Content)
	}
	if wire[2].Content != "look /new.png /two.png" {
		t.Fatalf("current user not expanded: %q", wire[2].Content)
	}
}

func TestBuildWireMessagesCombinesPastedAndImage(t *testing.T) {
	// Both expansions must run on the same turn — pasted text first,
	// then image. Verifies the combined path inside buildWireMessages.
	atts := []Attachment{{Index: 1, Path: "/img.png"}}
	pastes := []PastedText{{Index: 1, Text: "BLOB", Lines: 1}}
	wire := buildWireMessages(nil, "see [Image #1] also [Pasted text #1 +1 lines]", pastes, atts)
	if len(wire) != 1 {
		t.Fatalf("want 1 wire msg, got %d", len(wire))
	}
	want := "see /img.png also BLOB"
	if wire[0].Content != want {
		t.Fatalf("combined = %q, want %q", wire[0].Content, want)
	}
}
