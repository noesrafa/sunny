package tui

import (
	"strings"
	"testing"
)

const oscOpen = "\x1b]8;;"
const oscMid = "\x1b\\"
const oscClose = "\x1b]8;;\x1b\\"

func TestLinkify_NoURL(t *testing.T) {
	in := "hola wey, sin links acá"
	if got := linkify(in); got != in {
		t.Errorf("expected passthrough, got %q", got)
	}
}

func TestLinkify_Empty(t *testing.T) {
	if got := linkify(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestLinkify_BasicHTTPS(t *testing.T) {
	in := "go to https://example.com now"
	got := linkify(in)
	want := "go to " + oscOpen + "https://example.com" + oscMid + "https://example.com" + oscClose + " now"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestLinkify_BasicHTTP(t *testing.T) {
	in := "http://example.com"
	got := linkify(in)
	if !strings.Contains(got, oscOpen+"http://example.com"+oscMid) {
		t.Errorf("expected OSC 8 wrap, got %q", got)
	}
}

func TestLinkify_TrimsTrailingPunctuation(t *testing.T) {
	in := "see https://example.com."
	got := linkify(in)
	// URL inside the OSC 8 should NOT include the trailing period.
	want := "see " + oscOpen + "https://example.com" + oscMid + "https://example.com" + oscClose + "."
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestLinkify_TrimsTrailingParen(t *testing.T) {
	in := "(see https://example.com)"
	got := linkify(in)
	want := "(see " + oscOpen + "https://example.com" + oscMid + "https://example.com" + oscClose + ")"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestLinkify_Multiple(t *testing.T) {
	in := "https://a.com and https://b.com"
	got := linkify(in)
	// Two close sequences = two complete OSC 8 wraps. (The open prefix
	// "\x1b]8;;" appears inside the close too, so we count closes which
	// are unambiguous.)
	if c := strings.Count(got, oscClose); c != 2 {
		t.Errorf("expected 2 OSC 8 wraps, got %d in %q", c, got)
	}
}

func TestLinkify_QueryAndFragment(t *testing.T) {
	in := "https://example.com/path?q=1&r=2#frag"
	got := linkify(in)
	want := oscOpen + "https://example.com/path?q=1&r=2#frag" + oscMid + "https://example.com/path?q=1&r=2#frag" + oscClose
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestLinkify_NonHTTPSchemeIgnored(t *testing.T) {
	// We only linkify http(s) on purpose. file:// / ftp:// stay plain.
	in := "see file:///tmp/foo"
	if got := linkify(in); got != in {
		t.Errorf("expected passthrough for non-http scheme, got %q", got)
	}
}

func TestLinkify_NoBareSchemeMatch(t *testing.T) {
	// "https://" with nothing after shouldn't blow up or wrap.
	in := "https://"
	got := linkify(in)
	// Regex requires at least one non-whitespace char after //, so no match.
	if got != in {
		t.Errorf("expected passthrough for empty URL, got %q", got)
	}
}
