package main

import (
	"net"
	"testing"
)

func TestIsPrivateV4(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.15.0.1", false}, // just below the RFC1918 block
		{"172.32.0.1", false}, // just above it
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		{"192.167.1.1", false},
		{"192.169.1.1", false},
		{"169.254.1.1", true}, // link-local
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"127.0.0.1", false}, // loopback is handled separately, not "LAN"
		{"0.0.0.0", false},
		{"224.0.0.1", false},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
			if got := isPrivateV4(ip); got != tc.want {
				t.Errorf("isPrivateV4(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func TestIsPrivateV4RejectsV6(t *testing.T) {
	if isPrivateV4(net.ParseIP("fd00::1")) {
		t.Error("an IPv6 address must not pass the IPv4 check")
	}
}

func TestIsPrivateV6(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"fd00::1", true}, // ULA
		{"fc00::1", true}, // ULA
		{"fdff:ffff::1", true},
		{"fe80::1", false},     // link-local: skipped, needs a zone ID in URLs
		{"2001:db8::1", false}, // documentation/global
		{"::1", false},         // loopback
		{"fb00::1", false},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
			if got := isPrivateV6(ip); got != tc.want {
				t.Errorf("isPrivateV6(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func TestIsPrivateV6RejectsV4(t *testing.T) {
	if isPrivateV6(net.ParseIP("192.168.1.1")) {
		t.Error("an IPv4 address must not pass the IPv6 check")
	}
}

func TestIsPrivateHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"192.168.1.5", true},
		{"10.1.2.3", true},
		{"172.20.0.9", true},
		{"169.254.10.1", true},
		{"127.0.0.1", true},
		{"localhost", true},
		{"::1", true},
		{"[::1]", true},
		{"fd00::1", true},
		{"fe80::1%en0", true}, // zone stripped, still link-local
		{"8.8.8.8", false},
		{"93.184.216.34", false},
		{"example.com", false},   // names can resolve anywhere
		{"lantest.local", false}, // including mDNS names
		{"", false},
		{"not an ip", false},
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			if got := isPrivateHost(tc.host); got != tc.want {
				t.Errorf("isPrivateHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestHostOnly(t *testing.T) {
	tests := []struct{ in, want string }{
		{"192.168.1.5:8080", "192.168.1.5"},
		{"192.168.1.5", "192.168.1.5"},
		{"[fd00::1]:8080", "fd00::1"},
		{"[fd00::1]", "fd00::1"},
		{"localhost:8080", "localhost"},
	}
	for _, tc := range tests {
		if got := hostOnly(tc.in); got != tc.want {
			t.Errorf("hostOnly(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSubnetOf(t *testing.T) {
	_, n24, _ := net.ParseCIDR("192.168.1.0/24")
	a := LANAddr{IP: net.ParseIP("192.168.1.37").To4(), Net: n24}
	if got := subnetOf(a); got != "192.168.1.0/24" {
		t.Errorf("subnetOf = %q, want 192.168.1.0/24", got)
	}

	_, n16, _ := net.ParseCIDR("10.0.0.0/16")
	b := LANAddr{IP: net.ParseIP("10.0.5.9").To4(), Net: n16}
	if got := subnetOf(b); got != "10.0.0.0/16" {
		t.Errorf("subnetOf = %q, want 10.0.0.0/16", got)
	}

	if got := subnetOf(LANAddr{IP: net.ParseIP("10.0.0.1")}); got != "" {
		t.Errorf("missing mask must yield empty, got %q", got)
	}
}

func TestIdentifyNetworkFallsBackToSubnet(t *testing.T) {
	_, n, _ := net.ParseCIDR("192.168.7.0/24")
	addrs := []LANAddr{{IP: net.ParseIP("192.168.7.22").To4(), Net: n}}

	id := identifyNetwork(addrs, "")
	if id.Subnet != "192.168.7.0/24" {
		t.Errorf("subnet: got %q", id.Subnet)
	}
	if id.Key != "subnet:192.168.7.0/24" {
		t.Errorf("with no SSID the key must come from the subnet, got %q", id.Key)
	}
}

func TestIdentifyNetworkPrefersSSID(t *testing.T) {
	_, n, _ := net.ParseCIDR("192.168.7.0/24")
	addrs := []LANAddr{{IP: net.ParseIP("192.168.7.22").To4(), Net: n}}

	id := identifyNetwork(addrs, "  Acasa_5G  ")
	if id.SSID != "Acasa_5G" {
		t.Errorf("SSID should be trimmed, got %q", id.SSID)
	}
	if id.Key != "ssid:Acasa_5G" {
		t.Errorf("key should come from the SSID, got %q", id.Key)
	}
	if id.Subnet != "192.168.7.0/24" {
		t.Errorf("subnet should still be recorded, got %q", id.Subnet)
	}
}

func TestIdentifyNetworkWithNoAddresses(t *testing.T) {
	id := identifyNetwork(nil, "")
	if id.Key != "unknown" {
		t.Errorf("network key must never be empty, got %q", id.Key)
	}
}

func TestDescribeUA(t *testing.T) {
	tests := []struct{ ua, want string }{
		{"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36", "Android / Chrome"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1", "iPhone / Safari"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36 Edg/120.0", "Windows / Edge"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0", "Linux / Firefox"},
		{"", "dispozitiv necunoscut"},
	}
	for _, tc := range tests {
		if got := describeUA(tc.ua); got != tc.want {
			t.Errorf("describeUA(%.40q) = %q, want %q", tc.ua, got, tc.want)
		}
	}
}

func TestFormatURLAndListenAddr(t *testing.T) {
	v4 := net.ParseIP("192.168.1.5").To4()
	if got := formatURL(v4, 8080); got != "http://192.168.1.5:8080" {
		t.Errorf("formatURL v4 = %q", got)
	}
	if got := formatListenAddr(v4, 8080); got != "192.168.1.5:8080" {
		t.Errorf("formatListenAddr v4 = %q", got)
	}

	v6 := net.ParseIP("fd00::1")
	if got := formatURL(v6, 8080); got != "http://[fd00::1]:8080" {
		t.Errorf("formatURL v6 = %q", got)
	}
	if got := formatListenAddr(v6, 8080); got != "[fd00::1]:8080" {
		t.Errorf("formatListenAddr v6 = %q", got)
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID("abc-123_XYZ"); got != "abc-123_XYZ" {
		t.Errorf("valid ID mangled: %q", got)
	}
	if got := sanitizeID("../../etc/passwd"); got != "etcpasswd" {
		t.Errorf("path characters must be stripped, got %q", got)
	}
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'a'
	}
	if got := sanitizeID(string(long)); len(got) != 64 {
		t.Errorf("expected truncation to 64, got %d", len(got))
	}
}
