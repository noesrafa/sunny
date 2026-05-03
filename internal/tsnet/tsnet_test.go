package tsnet

import "testing"

// sampleStatus is what `tailscale status --json` looks like for a
// tailnet where Mac + VPS belong to UserID 42 and "shared-friend"
// is a different account (UserID 99) sharing the network.
var sampleStatus = []byte(`{
	"Self": {
		"HostName": "mac-rafael",
		"TailscaleIPs": ["100.64.0.5", "fd7a:115c::1"],
		"OS": "macOS",
		"Online": true,
		"UserID": 42
	},
	"Peer": {
		"abc123": {
			"HostName": "vps-1",
			"TailscaleIPs": ["100.64.0.10"],
			"OS": "linux",
			"Online": true,
			"UserID": 42
		},
		"def456": {
			"HostName": "pi",
			"TailscaleIPs": ["100.64.0.20"],
			"OS": "linux",
			"Online": false,
			"UserID": 42
		},
		"ghi789": {
			"HostName": "shared-friend",
			"TailscaleIPs": ["100.64.0.99"],
			"OS": "linux",
			"Online": true,
			"UserID": 99
		}
	}
}`)

func TestParseStatus(t *testing.T) {
	peers, err := parseStatus(sampleStatus)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(peers) != 4 {
		t.Fatalf("got %d peers, want 4", len(peers))
	}
	// Self should be first
	if !peers[0].IsSelf || peers[0].HostName != "mac-rafael" || peers[0].IP != "100.64.0.5" {
		t.Errorf("self = %+v", peers[0])
	}
	if peers[0].UserID != 42 {
		t.Errorf("self UserID = %d, want 42", peers[0].UserID)
	}
	byName := map[string]Peer{}
	for _, p := range peers {
		byName[p.HostName] = p
	}
	if p := byName["vps-1"]; !p.Online || p.IP != "100.64.0.10" || p.IsSelf || p.UserID != 42 {
		t.Errorf("vps-1 = %+v", p)
	}
	if p := byName["pi"]; p.Online || p.IP != "100.64.0.20" || p.UserID != 42 {
		t.Errorf("pi = %+v", p)
	}
	if p := byName["shared-friend"]; p.UserID != 99 {
		t.Errorf("shared-friend UserID = %d, want 99", p.UserID)
	}
}

func TestStatus_SameUser(t *testing.T) {
	st, err := parseStatusFull(sampleStatus)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := map[string]bool{
		"100.64.0.5":  true,  // self
		"100.64.0.10": true,  // vps, same UserID
		"100.64.0.20": true,  // pi (offline, but same owner)
		"100.64.0.99": false, // shared-friend, different UserID
		"8.8.8.8":     false, // not on tailnet at all
		"":            false,
	}
	for ip, want := range cases {
		if got := st.SameUser(ip); got != want {
			t.Errorf("SameUser(%q) = %v, want %v", ip, got, want)
		}
	}
}

func TestStatus_SameUser_UnknownSelf(t *testing.T) {
	st := &Status{Self: Peer{UserID: 0}, Peers: []Peer{{IP: "100.64.0.10", UserID: 42}}}
	if st.SameUser("100.64.0.10") {
		t.Errorf("SameUser must return false when self UserID is unknown")
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
