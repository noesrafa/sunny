package session

import (
	"strings"
	"testing"
)

func TestExpandPastedTextsBasic(t *testing.T) {
	pastes := []PastedText{
		{Index: 1, Text: "hello\nworld", Lines: 2},
	}
	got := ExpandPastedTexts("see [Pasted text #1 +2 lines] thanks", pastes)
	want := "see hello\nworld thanks"
	if got != want {
		t.Fatalf("expand = %q, want %q", got, want)
	}
}

func TestExpandPastedTextsMultiple(t *testing.T) {
	pastes := []PastedText{
		{Index: 1, Text: "A", Lines: 1},
		{Index: 2, Text: "B", Lines: 1},
	}
	got := ExpandPastedTexts("x [Pasted text #1 +1 lines] y [Pasted text #2 +1 lines] z", pastes)
	if got != "x A y B z" {
		t.Fatalf("expand multi = %q", got)
	}
}

func TestExpandPastedTextsOrphanSurvives(t *testing.T) {
	// Index 5 has no backing blob (e.g. TUI restart dropped it). The
	// marker stays as literal text instead of vanishing silently, so
	// the user notices the orphan and can edit it out.
	got := ExpandPastedTexts("before [Pasted text #5 +9 lines] after", nil)
	if got != "before [Pasted text #5 +9 lines] after" {
		t.Fatalf("orphan stripped: %q", got)
	}
}

func TestExpandPastedTextsToleratesLineCountDrift(t *testing.T) {
	// User manually edited "+50 lines" to "+51 lines" in the marker.
	// We still expand by index so the model gets the right blob.
	pastes := []PastedText{{Index: 3, Text: "BLOB", Lines: 50}}
	got := ExpandPastedTexts("[Pasted text #3 +51 lines]", pastes)
	if got != "BLOB" {
		t.Fatalf("drift broke match: %q", got)
	}
}

func TestExpandPastedTextsNoMarkersNoOp(t *testing.T) {
	pastes := []PastedText{{Index: 1, Text: "x", Lines: 1}}
	got := ExpandPastedTexts("plain text without markers", pastes)
	if got != "plain text without markers" {
		t.Fatalf("mutated input: %q", got)
	}
}

func TestBuildWireMessagesExpandsHistoryAndCurrent(t *testing.T) {
	items := []Item{
		UserItem{
			Text:        "context [Pasted text #1 +3 lines]",
			PastedTexts: []PastedText{{Index: 1, Text: "L1\nL2\nL3", Lines: 3}},
		},
		AssistantTextItem{Text: "ack"},
	}
	currentPastes := []PastedText{{Index: 1, Text: "NEW BLOB", Lines: 1}}
	wire := buildWireMessages(items, "follow [Pasted text #1 +1 lines]", currentPastes)
	if len(wire) != 3 {
		t.Fatalf("want 3 wire msgs, got %d", len(wire))
	}
	if !strings.Contains(wire[0].Content, "L1\nL2\nL3") {
		t.Fatalf("history user wasn't expanded: %q", wire[0].Content)
	}
	if wire[2].Content != "follow NEW BLOB" {
		t.Fatalf("current user not expanded: %q", wire[2].Content)
	}
}

func TestAddPastedTextCountsLines(t *testing.T) {
	s := &Session{}
	idx, n := s.AddPastedText("one\ntwo\nthree")
	if idx != 1 || n != 3 {
		t.Fatalf("first paste: idx=%d lines=%d", idx, n)
	}
	idx, n = s.AddPastedText("single")
	if idx != 2 || n != 1 {
		t.Fatalf("second paste: idx=%d lines=%d", idx, n)
	}
	if len(s.PastedTexts) != 2 {
		t.Fatalf("want 2 stored, got %d", len(s.PastedTexts))
	}
}
