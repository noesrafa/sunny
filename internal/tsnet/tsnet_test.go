package tsnet

import "testing"

func TestParseStatus(t *testing.T) {
	raw := []byte(`{
		"Self": {
			"HostName": "mac-rafael",
			"TailscaleIPs": ["100.64.0.5", "fd7a:115c::1"],
			"OS": "macOS",
			"Online": true
		},
		"Peer": {
			"abc123": {
				"HostName": "vps-1",
				"TailscaleIPs": ["100.64.0.10"],
				"OS": "linux",
				"Online": true
			},
			"def456": {
				"HostName": "pi",
				"TailscaleIPs": ["100.64.0.20"],
				"OS": "linux",
				"Online": false
			}
		}
	}`)
	peers, err := parseStatus(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(peers) != 3 {
		t.Fatalf("got %d peers, want 3", len(peers))
	}
	// Self should be first
	if !peers[0].IsSelf || peers[0].HostName != "mac-rafael" || peers[0].IP != "100.64.0.5" {
		t.Errorf("self = %+v", peers[0])
	}
	// Find vps and pi
	byName := map[string]Peer{}
	for _, p := range peers {
		byName[p.HostName] = p
	}
	if p := byName["vps-1"]; !p.Online || p.IP != "100.64.0.10" || p.IsSelf {
		t.Errorf("vps-1 = %+v", p)
	}
	if p := byName["pi"]; p.Online || p.IP != "100.64.0.20" {
		t.Errorf("pi = %+v", p)
	}
}

func TestFirstIPv4(t *testing.T) {
	cases := map[string]struct {
		in   []string
		want string
	}{
		"v4 first":   {[]string{"100.64.0.5", "fd7a::1"}, "100.64.0.5"},
		"v6 first":   {[]string{"fd7a::1", "100.64.0.5"}, "100.64.0.5"},
		"only v6":    {[]string{"fd7a::1"}, "fd7a::1"}, // best-effort fallback
		"empty":      {nil, ""},
		"odd string": {[]string{"weirdo"}, "weirdo"}, // no v4-looking string; fallback to first
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := firstIPv4(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
