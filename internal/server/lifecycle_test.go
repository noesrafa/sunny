package server

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		// Newer latest → true
		{"v0.20.0", "v0.19.0", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.19.1", "v0.19.0", true},
		{"0.19.1", "v0.19.0", true}, // missing v on one side
		// Same → false
		{"v0.19.0", "v0.19.0", false},
		// Older latest → false (shouldn't happen but safe to handle)
		{"v0.18.0", "v0.19.0", false},
		// Local dev build → never considered out of date
		{"v0.19.0", "dev", false},
		{"", "v0.19.0", false},
		{"v0.19.0", "", false},
		// Pre-release suffix on either side — major.minor.patch wins
		{"v0.19.0-rc1", "v0.18.0", true},
		{"v0.19.0", "v0.19.0-rc1", false}, // both parse to 0.19.0 → equal
		// Garbage strings fall back to string compare
		{"weird", "v0.19.0", true}, // string compare: "weird" != "v0.19.0"
	}
	for _, c := range cases {
		got := isNewer(c.latest, c.current)
		if got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"v1.2.3", []int{1, 2, 3}},
		{"1.2.3", []int{1, 2, 3}},
		{"v0.0.0", []int{0, 0, 0}},
		{"v1.2.3-rc1", []int{1, 2, 3}},
		{"v1.2.3+meta", []int{1, 2, 3}},
		{"v1.2", nil},  // missing patch
		{"abc", nil},   // not numeric
		{"", nil},
	}
	for _, c := range cases {
		got := parseSemver(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseSemver(%q) len = %d, want %d", c.in, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseSemver(%q)[%d] = %d, want %d", c.in, i, got[i], c.want[i])
			}
		}
	}
}
