package doctor

import (
	"testing"
	"time"
)

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                                "0s",
		15 * time.Second:                 "15s",
		59*time.Second + 999*time.Millisecond: "59s",
		1 * time.Minute:                  "1m0s",
		1*time.Minute + 30*time.Second:   "1m30s",
		59*time.Minute + 59*time.Second:  "59m59s",
		1 * time.Hour:                    "1h0m",
		1*time.Hour + 23*time.Minute:     "1h23m",
		25*time.Hour + 3*time.Minute:     "25h3m",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestHasDigit(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"abc":      false,
		"v1.2.3":   true,
		"build42":  true,
		"_-+":      false,
	}
	for in, want := range cases {
		if got := hasDigit(in); got != want {
			t.Errorf("hasDigit(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"plain text":                "plain text",
		"\x1b[1mbold\x1b[0m":        "bold",
		"\x1b[31mred\x1b[0m line\n": "red line\n",
	}
	for in, want := range cases {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVDetail(t *testing.T) {
	cases := []struct {
		ver, suffix, want string
	}{
		{"", "no providers authed", "no providers authed"},
		{"1.14.33", "no providers authed", "v1.14.33 on PATH, no providers authed"},
	}
	for _, c := range cases {
		if got := vDetail(c.ver, c.suffix); got != c.want {
			t.Errorf("vDetail(%q, %q) = %q, want %q", c.ver, c.suffix, got, c.want)
		}
	}
}
