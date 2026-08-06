package main

import (
	"strings"
	"testing"
)

func TestBandFromFreq(t *testing.T) {
	tests := []struct {
		mhz  int
		want string
	}{
		{2412, "2.4 GHz"},
		{2437, "2.4 GHz"},
		{2484, "2.4 GHz"},
		{5180, "5 GHz"},
		{5745, "5 GHz"},
		{5955, "6 GHz"},
		{6415, "6 GHz"},
		{900, ""},
		{0, ""},
	}
	for _, tc := range tests {
		if got := bandFromFreq(tc.mhz); got != tc.want {
			t.Errorf("bandFromFreq(%d) = %q, want %q", tc.mhz, got, tc.want)
		}
	}
}

func TestChannelFromFreq(t *testing.T) {
	tests := []struct {
		mhz  int
		want int
	}{
		{2412, 1},
		{2437, 6},
		{2472, 13},
		{2484, 14}, // sits outside the regular 5 MHz spacing
		{5180, 36},
		{5220, 44},
		{5745, 149},
		{5955, 1}, // 6 GHz numbering restarts
		{6175, 45},
		{1000, 0},
	}
	for _, tc := range tests {
		if got := channelFromFreq(tc.mhz); got != tc.want {
			t.Errorf("channelFromFreq(%d) = %d, want %d", tc.mhz, got, tc.want)
		}
	}
}

// ── Linux: iw ────────────────────────────────────────────────────────────────

func TestParseIWLink(t *testing.T) {
	out := `Connected to 9c:3d:cf:11:22:33 (on wlp3s0)
	SSID: Acasa_5G
	freq: 5220
	RX: 88123456 bytes (61234 packets)
	TX: 12345678 bytes (23456 packets)
	signal: -47 dBm
	rx bitrate: 866.7 MBit/s VHT-MCS 9 80MHz short GI VHT-NSS 2
	tx bitrate: 780.0 MBit/s VHT-MCS 8 80MHz short GI VHT-NSS 2
	bss flags:	short-slot-time
	dtim period:	2
	beacon int:	100`

	got := parseIWLink(out)
	if got.Type != LinkWiFi {
		t.Errorf("type: got %q", got.Type)
	}
	if got.SSID != "Acasa_5G" {
		t.Errorf("ssid: got %q", got.SSID)
	}
	if got.BSSID != "9c:3d:cf:11:22:33" {
		t.Errorf("bssid: got %q", got.BSSID)
	}
	if got.FreqMHz != 5220 {
		t.Errorf("freq: got %d", got.FreqMHz)
	}
	// Band and channel are derived, not reported by iw.
	if got.Band != "5 GHz" || got.Channel != 44 {
		t.Errorf("derived band/channel: got %q / %d, want 5 GHz / 44", got.Band, got.Channel)
	}
	if got.WidthMHz != 80 {
		t.Errorf("width: got %d, want 80", got.WidthMHz)
	}
	if got.SignalDBm != -47 {
		t.Errorf("signal: got %d, want -47", got.SignalDBm)
	}
	if got.TxRateMbps != 780 || got.RxRateMbps != 866.7 {
		t.Errorf("rates: got tx %v rx %v", got.TxRateMbps, got.RxRateMbps)
	}
}

func TestParseIWLink24GHz(t *testing.T) {
	out := `Connected to aa:bb:cc:dd:ee:ff (on wlan0)
	SSID: Acasa
	freq: 2437
	signal: -62 dBm
	tx bitrate: 144.4 MBit/s MCS 15 short GI`

	got := parseIWLink(out)
	if got.Band != "2.4 GHz" || got.Channel != 6 {
		t.Errorf("got %q / channel %d, want 2.4 GHz / 6", got.Band, got.Channel)
	}
	if got.WidthMHz != 0 {
		t.Errorf("no width in this line, got %d", got.WidthMHz)
	}
}

// ── Linux: nmcli ─────────────────────────────────────────────────────────────

func TestParseNmcliWifi(t *testing.T) {
	// nmcli escapes the colons inside a BSSID, which is exactly the thing a
	// naive split on ":" gets wrong.
	out := `no:Vecinul:AA\:BB\:CC\:11\:22\:33:6:2437 MHz:270 Mbit/s:47:WPA2
yes:Acasa_5G:9C\:3D\:CF\:11\:22\:33:44:5220 MHz:866 Mbit/s:82:WPA2 WPA3
no:AltaRetea:11\:22\:33\:44\:55\:66:1:2412 MHz:130 Mbit/s:30:WPA1 WPA2`

	got := parseNmcliWifi(out)
	if got.SSID != "Acasa_5G" {
		t.Errorf("ssid: got %q", got.SSID)
	}
	if got.BSSID != "9C:3D:CF:11:22:33" {
		t.Errorf("bssid: got %q, want the unescaped form", got.BSSID)
	}
	if got.Channel != 44 {
		t.Errorf("channel: got %d", got.Channel)
	}
	if got.FreqMHz != 5220 || got.Band != "5 GHz" {
		t.Errorf("freq/band: got %d / %q", got.FreqMHz, got.Band)
	}
	if got.SignalPercent != 82 {
		t.Errorf("signal: got %d", got.SignalPercent)
	}
	if got.Security != "WPA2 WPA3" {
		t.Errorf("security: got %q", got.Security)
	}
}

func TestParseNmcliWifiNoActiveNetwork(t *testing.T) {
	out := "no:Vecinul:AA\\:BB\\:CC\\:11\\:22\\:33:6:2437 MHz:270 Mbit/s:47:WPA2"
	if got := parseNmcliWifi(out); got.SSID != "" {
		t.Errorf("nothing is connected; got %+v", got)
	}
}

func TestSplitNmcli(t *testing.T) {
	got := splitNmcli(`yes:My\:Net:AA\:BB:44`)
	want := []string{"yes", "My:Net", "AA:BB", "44"}
	if len(got) != len(want) {
		t.Fatalf("got %d fields %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// ── macOS: system_profiler ───────────────────────────────────────────────────

func TestParseSystemProfilerAirPort(t *testing.T) {
	out := `Wi-Fi:

      Software Versions:
          CoreWLAN: 16.0
      Interfaces:
        en0:
          Card Type: Wi-Fi  (0x14E4, 0x4387)
          Status: Connected
          Current Network Information:
            Acasa_5G:
              PHY Mode: 802.11ax
              Channel: 44 (5GHz, 80MHz)
              Country Code: RO
              Network Type: Infrastructure
              Security: WPA2 Personal
              Signal / Noise: -47 dBm / -92 dBm
              Transmit Rate: 866
              MCS Index: 9
          Other Local Wi-Fi Networks:
            Vecinul:
              PHY Mode: 802.11n
              Channel: 6 (2GHz, 20MHz)
              Signal / Noise: -80 dBm / -92 dBm`

	got := parseSystemProfilerAirPort(out)
	if got.SSID != "Acasa_5G" {
		t.Errorf("ssid: got %q", got.SSID)
	}
	if got.PHYMode != "802.11ax" {
		t.Errorf("phy: got %q", got.PHYMode)
	}
	if got.Channel != 44 || got.Band != "5 GHz" || got.WidthMHz != 80 {
		t.Errorf("channel/band/width: got %d / %q / %d", got.Channel, got.Band, got.WidthMHz)
	}
	if got.Security != "WPA2 Personal" {
		t.Errorf("security: got %q", got.Security)
	}
	if got.SignalDBm != -47 {
		t.Errorf("signal: got %d, want -47 (the first of the pair)", got.SignalDBm)
	}
	if got.TxRateMbps != 866 {
		t.Errorf("tx rate: got %v", got.TxRateMbps)
	}
	// The neighbour list must not overwrite the connected network.
	if got.Channel == 6 {
		t.Error("parsing continued into Other Local Wi-Fi Networks")
	}
}

func TestParseSystemProfilerAirPort24GHz(t *testing.T) {
	out := `          Current Network Information:
            Acasa:
              PHY Mode: 802.11n
              Channel: 11 (2GHz, 20MHz)
              Security: WPA2 Personal
              Signal / Noise: -66 dBm / -90 dBm`

	got := parseSystemProfilerAirPort(out)
	if got.Band != "2.4 GHz" || got.Channel != 11 || got.WidthMHz != 20 {
		t.Errorf("got %q / %d / %d", got.Band, got.Channel, got.WidthMHz)
	}
}

// ── Windows: netsh ───────────────────────────────────────────────────────────

func TestParseNetshWlan(t *testing.T) {
	out := `There is 1 interface on the system:

    Name                   : Wi-Fi
    Description            : Intel(R) Wi-Fi 6E AX211 160MHz
    GUID                   : 1a2b3c4d-0000-1111-2222-333344445555
    Physical address       : a4:bb:cc:dd:ee:ff
    State                  : connected
    SSID                   : Acasa_5G
    BSSID                  : 9c:3d:cf:11:22:33
    Network type           : Infrastructure
    Radio type             : 802.11ax
    Authentication         : WPA2-Personal
    Cipher                 : CCMP
    Connection mode        : Profile
    Band                   : 5 GHz
    Channel                : 44
    Receive rate (Mbps)    : 1201
    Transmit rate (Mbps)   : 1201
    Signal                 : 96%
    Profile                : Acasa_5G`

	got := parseNetshWlan(out)
	if got.SSID != "Acasa_5G" {
		t.Errorf("ssid: got %q", got.SSID)
	}
	if got.BSSID != "9c:3d:cf:11:22:33" {
		t.Errorf("bssid: got %q — a value containing colons must survive", got.BSSID)
	}
	if got.Band != "5 GHz" || got.Channel != 44 {
		t.Errorf("band/channel: got %q / %d", got.Band, got.Channel)
	}
	if got.PHYMode != "802.11ax" {
		t.Errorf("phy: got %q", got.PHYMode)
	}
	if got.RxRateMbps != 1201 || got.TxRateMbps != 1201 {
		t.Errorf("rates: got %v / %v", got.RxRateMbps, got.TxRateMbps)
	}
	if got.SignalPercent != 96 {
		t.Errorf("signal: got %d", got.SignalPercent)
	}
	if got.Security != "WPA2-Personal" {
		t.Errorf("security: got %q", got.Security)
	}
	if got.Interface != "Wi-Fi" {
		t.Errorf("interface: got %q", got.Interface)
	}
}

// netsh prints localised labels. An unrecognised locale must yield fewer
// fields, never wrong ones — the acronyms stay readable everywhere.
func TestParseNetshWlanLocalised(t *testing.T) {
	out := `    Nume                   : Wi-Fi
    SSID                   : Acasa
    BSSID                  : 9c:3d:cf:11:22:33
    Tip radio              : 802.11ac
    Canal                  : 36
    Semnal                 : 88%`

	got := parseNetshWlan(out)
	if got.SSID != "Acasa" {
		t.Errorf("SSID is an acronym in every locale; got %q", got.SSID)
	}
	if got.Channel != 36 {
		t.Errorf("channel: got %d", got.Channel)
	}
	if got.SignalPercent != 88 {
		t.Errorf("signal: got %d", got.SignalPercent)
	}
}

func TestParseNetshWlanNotConnected(t *testing.T) {
	out := `There is 1 interface on the system:

    Name                   : Wi-Fi
    State                  : disconnected`

	if got := parseNetshWlan(out); got.SSID != "" {
		t.Errorf("nothing connected; got ssid %q", got.SSID)
	}
}

// ── macOS: wired ─────────────────────────────────────────────────────────────

func TestParseNetworksetupMedia(t *testing.T) {
	out := `Current: 1000baseT <full-duplex,flow-control>
Active: 1000baseT <full-duplex,flow-control>`

	got := parseNetworksetupMedia(out)
	if got.Type != LinkEthernet {
		t.Errorf("type: got %q", got.Type)
	}
	if got.LinkSpeedMbps != 1000 {
		t.Errorf("speed: got %d", got.LinkSpeedMbps)
	}
	if got.Duplex != "full" {
		t.Errorf("duplex: got %q", got.Duplex)
	}
}

// ── labelled lines ───────────────────────────────────────────────────────────

func TestSplitLabelled(t *testing.T) {
	// Only the first colon separates: a MAC address is all colons after that.
	k, v, ok := splitLabelled("BSSID : 9c:3d:cf:11:22:33")
	if !ok || k != "BSSID" || v != "9c:3d:cf:11:22:33" {
		t.Errorf("got %q = %q (ok=%v)", k, v, ok)
	}
	if _, _, ok := splitLabelled("no separator here"); ok {
		t.Error("a line without a colon is not a labelled line")
	}
	if _, _, ok := splitLabelled(": leading"); ok {
		t.Error("an empty label is not a labelled line")
	}
}

// ── description ──────────────────────────────────────────────────────────────

func TestDescribeWiFi(t *testing.T) {
	l := LinkInfo{
		Type: LinkWiFi, SSID: "Acasa_5G", Band: "5 GHz", Channel: 44,
		WidthMHz: 80, PHYMode: "802.11ax", SignalDBm: -47, TxRateMbps: 866,
	}
	got := l.Describe()
	for _, want := range []string{"Wi-Fi", "Acasa_5G", "5 GHz", "canal 44", "80 MHz", "802.11ax", "-47 dBm"} {
		if !strings.Contains(got, want) {
			t.Errorf("description %q missing %q", got, want)
		}
	}
}

func TestDescribeEthernet(t *testing.T) {
	got := LinkInfo{Type: LinkEthernet, LinkSpeedMbps: 1000, Duplex: "full"}.Describe()
	if !strings.Contains(got, "Cablu") || !strings.Contains(got, "1000 Mbps") {
		t.Errorf("got %q", got)
	}
}

func TestDescribeUnknown(t *testing.T) {
	if got := (LinkInfo{Type: LinkUnknown}).Describe(); got == "" {
		t.Error("an unknown link still needs something to show")
	}
}

// Windows reports signal as a percentage rather than dBm; the description has
// to use whichever one it actually got.
func TestDescribePrefersRealSignalUnit(t *testing.T) {
	got := LinkInfo{Type: LinkWiFi, SSID: "X", SignalPercent: 96}.Describe()
	if !strings.Contains(got, "96%") {
		t.Errorf("got %q, expected the percentage", got)
	}
	if strings.Contains(got, "dBm") {
		t.Errorf("got %q, must not invent a dBm reading", got)
	}
}

// ── monitor ──────────────────────────────────────────────────────────────────

func TestLinkMonitorStartsUsable(t *testing.T) {
	m := NewLinkMonitor("eth0")
	info := m.Info()
	if info.Interface != "eth0" {
		t.Errorf("interface: got %q", info.Interface)
	}
	if info.Type != LinkUnknown {
		t.Errorf("before probing, the type is unknown; got %q", info.Type)
	}
	if info.Describe() == "" {
		t.Error("an unprobed monitor must still describe itself")
	}
}

// A hostile or malformed interface name must never reach a command line.
func TestDetectLinkRejectsOddInterfaceNames(t *testing.T) {
	for _, name := range []string{"eth0; rm -rf /", "../../etc", "a b", strings.Repeat("x", 64), ""} {
		got := detectLink(name)
		if got.Type != LinkUnknown || got.Note == "" {
			t.Errorf("detectLink(%q) should refuse, got %+v", name, got)
		}
	}
}
