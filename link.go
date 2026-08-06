package main

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LinkInfo describes how this machine is attached to the network: over a cable
// or over Wi-Fi, and if Wi-Fi, on which band and channel.
//
// It is diagnostic context, not measurement. Every field is best effort: the
// tools that report it differ per operating system, some need privileges, and
// some print localised labels. A field that could not be read is simply absent,
// and nothing here can fail the application or delay a test.
type LinkInfo struct {
	Interface string `json:"interface,omitempty"`
	// Type is "wifi", "ethernet" or "unknown".
	Type string `json:"type"`

	SSID    string `json:"ssid,omitempty"`
	BSSID   string `json:"bssid,omitempty"`
	FreqMHz int    `json:"freq_mhz,omitempty"`
	// Band and Channel are derived from FreqMHz when the tool does not report
	// them directly.
	Band     string `json:"band,omitempty"`
	Channel  int    `json:"channel,omitempty"`
	WidthMHz int    `json:"width_mhz,omitempty"`
	// PHYMode is the 802.11 generation, e.g. "802.11ax".
	PHYMode  string `json:"phy_mode,omitempty"`
	Security string `json:"security,omitempty"`

	// SignalDBm is the received signal level; SignalPercent is what Windows
	// reports instead. Zero means unknown in both cases.
	SignalDBm     int `json:"signal_dbm,omitempty"`
	SignalPercent int `json:"signal_percent,omitempty"`

	// TxRateMbps and RxRateMbps are the negotiated radio rates — the ceiling
	// the link could reach, not what the test measured. LinkSpeedMbps is the
	// equivalent for a wired link.
	TxRateMbps    float64 `json:"tx_rate_mbps,omitempty"`
	RxRateMbps    float64 `json:"rx_rate_mbps,omitempty"`
	LinkSpeedMbps int     `json:"link_speed_mbps,omitempty"`
	Duplex        string  `json:"duplex,omitempty"`

	// Source names the tool the numbers came from, so a surprising value can be
	// traced back to what reported it.
	Source string `json:"source,omitempty"`
	// Note explains why fields are missing, when there is a reason worth saying.
	Note string `json:"note,omitempty"`
}

// Link types.
const (
	LinkWiFi     = "wifi"
	LinkEthernet = "ethernet"
	LinkUnknown  = "unknown"
)

// linkProbeTimeout bounds each external command. macOS system_profiler in
// particular can take seconds, which is fine in the background but must never
// be unbounded.
const linkProbeTimeout = 6 * time.Second

// safeIface guards the interface name before it reaches a command line. The
// name comes from the kernel, not from a user, but a shell-out deserves the
// check anyway.
var safeIface = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,32}$`)

// ── frequency maths ─────────────────────────────────────────────────────────

// bandFromFreq names the band a centre frequency belongs to.
func bandFromFreq(mhz int) string {
	switch {
	case mhz >= 2400 && mhz <= 2500:
		return "2.4 GHz"
	case mhz >= 4900 && mhz <= 5895:
		return "5 GHz"
	case mhz >= 5925 && mhz <= 7125:
		return "6 GHz"
	}
	return ""
}

// channelFromFreq converts a centre frequency to a channel number. The three
// bands number their channels from different anchors, which is why this is not
// one formula.
func channelFromFreq(mhz int) int {
	switch {
	case mhz == 2484: // channel 14, Japan only, sits outside the regular spacing
		return 14
	case mhz >= 2412 && mhz <= 2472:
		return (mhz - 2407) / 5
	case mhz >= 5160 && mhz <= 5895:
		return (mhz - 5000) / 5
	case mhz >= 5925 && mhz <= 7125:
		return (mhz - 5950) / 5
	}
	return 0
}

// fillDerived completes band and channel from the frequency when the reporting
// tool gave one but not the others.
func (l *LinkInfo) fillDerived() {
	if l.FreqMHz > 0 {
		if l.Band == "" {
			l.Band = bandFromFreq(l.FreqMHz)
		}
		if l.Channel == 0 {
			l.Channel = channelFromFreq(l.FreqMHz)
		}
	}
	if l.Type == "" {
		l.Type = LinkUnknown
	}
}

// ── parsers ─────────────────────────────────────────────────────────────────
//
// Each parser takes the raw output of one tool and returns what it could read.
// They are split out from the commands that produce the text so they can be
// tested against captured output from systems this build cannot run on.

var (
	reFloat = regexp.MustCompile(`(\d+(?:\.\d+)?)`)
	reInt   = regexp.MustCompile(`(-?\d+)`)
)

func firstFloat(s string) float64 {
	m := reFloat.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

func firstInt(s string) int {
	m := reInt.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.Atoi(m[1])
	return v
}

// parseIWLink reads `iw dev <iface> link`.
func parseIWLink(out string) LinkInfo {
	info := LinkInfo{Type: LinkWiFi, Source: "iw"}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		low := strings.ToLower(line)
		switch {
		case strings.HasPrefix(low, "connected to "):
			rest := strings.TrimSpace(line[len("Connected to "):])
			if i := strings.Index(rest, " "); i > 0 {
				rest = rest[:i]
			}
			info.BSSID = rest
		case strings.HasPrefix(low, "ssid:"):
			info.SSID = strings.TrimSpace(line[5:])
		case strings.HasPrefix(low, "freq:"):
			info.FreqMHz = firstInt(line[5:])
		case strings.HasPrefix(low, "signal:"):
			info.SignalDBm = firstInt(line[7:])
		case strings.HasPrefix(low, "tx bitrate:"):
			info.TxRateMbps = firstFloat(line[11:])
			info.WidthMHz = widthFromBitrateLine(line)
		case strings.HasPrefix(low, "rx bitrate:"):
			info.RxRateMbps = firstFloat(line[11:])
			if info.WidthMHz == 0 {
				info.WidthMHz = widthFromBitrateLine(line)
			}
		}
	}
	info.fillDerived()
	return info
}

// widthFromBitrateLine pulls the channel width out of an iw bitrate line, which
// carries it as a token such as "80MHz".
var reWidth = regexp.MustCompile(`(\d+)MHz`)

func widthFromBitrateLine(line string) int {
	if m := reWidth.FindStringSubmatch(line); m != nil {
		v, _ := strconv.Atoi(m[1])
		return v
	}
	return 0
}

// parseNmcliWifi reads the active row of
// `nmcli -t -f active,ssid,bssid,chan,freq,rate,signal,security dev wifi`.
// Colons inside a BSSID are escaped by nmcli as "\:", which is why the split
// cannot be a plain strings.Split.
func parseNmcliWifi(out string) LinkInfo {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "yes:") {
			continue
		}
		f := splitNmcli(line)
		if len(f) < 8 {
			continue
		}
		info := LinkInfo{
			Type:          LinkWiFi,
			Source:        "nmcli",
			SSID:          f[1],
			BSSID:         f[2],
			Channel:       firstInt(f[3]),
			FreqMHz:       firstInt(f[4]),
			RxRateMbps:    firstFloat(f[5]),
			SignalPercent: firstInt(f[6]),
			Security:      strings.TrimSpace(f[7]),
		}
		info.fillDerived()
		return info
	}
	return LinkInfo{}
}

// splitNmcli splits a terse nmcli row on unescaped colons.
func splitNmcli(line string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line):
			cur.WriteByte(line[i+1])
			i++
		case line[i] == ':':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	out = append(out, cur.String())
	return out
}

// parseSystemProfilerAirPort reads the "Current Network Information" block of
// `system_profiler SPAirPortDataType` on macOS.
func parseSystemProfilerAirPort(out string) LinkInfo {
	info := LinkInfo{Type: LinkWiFi, Source: "system_profiler"}
	lines := strings.Split(out, "\n")

	inCurrent := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "Current Network Information:") {
			inCurrent = true
			// The next non-empty line is "<SSID>:".
			for j := i + 1; j < len(lines); j++ {
				name := strings.TrimSpace(lines[j])
				if name == "" {
					continue
				}
				info.SSID = strings.TrimSuffix(name, ":")
				break
			}
			continue
		}
		if !inCurrent {
			continue
		}
		// The block ends when the next top-level section starts.
		if strings.HasPrefix(line, "Other Local Wi-Fi Networks:") ||
			strings.HasPrefix(line, "awdl0:") {
			break
		}

		key, value, ok := splitLabelled(line)
		if !ok {
			continue
		}
		switch key {
		case "PHY Mode":
			info.PHYMode = value
		case "Channel":
			// "44 (5GHz, 80MHz)"
			info.Channel = firstInt(value)
			if strings.Contains(value, "6GHz") {
				info.Band = "6 GHz"
			} else if strings.Contains(value, "5GHz") {
				info.Band = "5 GHz"
			} else if strings.Contains(value, "2GHz") || strings.Contains(value, "2.4GHz") {
				info.Band = "2.4 GHz"
			}
			if m := reWidth.FindStringSubmatch(value); m != nil {
				w, _ := strconv.Atoi(m[1])
				info.WidthMHz = w
			}
		case "Security":
			info.Security = value
		case "BSSID":
			info.BSSID = value
		case "Signal / Noise":
			// "-45 dBm / -92 dBm"
			info.SignalDBm = firstInt(value)
		case "Transmit Rate":
			info.TxRateMbps = firstFloat(value)
		}
	}
	info.fillDerived()
	return info
}

// parseNetshWlan reads `netsh wlan show interfaces` on Windows.
//
// The labels are localised, so matching is on a set of known spellings plus the
// acronyms that stay the same in every locale. A locale this does not cover
// yields fewer fields, never wrong ones.
func parseNetshWlan(out string) LinkInfo {
	info := LinkInfo{Type: LinkWiFi, Source: "netsh"}
	for _, raw := range strings.Split(out, "\n") {
		key, value, ok := splitLabelled(strings.TrimSpace(raw))
		if !ok || value == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "bssid":
			info.BSSID = value
		case "ssid":
			info.SSID = value
		case "radio type", "tip radio", "typ radiowy":
			info.PHYMode = value
		case "band", "bandă", "banda":
			info.Band = normaliseBand(value)
		case "channel", "canal", "kanal":
			info.Channel = firstInt(value)
		case "receive rate (mbps)", "rata de recepție (mbps)":
			info.RxRateMbps = firstFloat(value)
		case "transmit rate (mbps)", "rata de transmisie (mbps)":
			info.TxRateMbps = firstFloat(value)
		case "signal", "semnal", "sygnał":
			info.SignalPercent = firstInt(value)
		case "authentication", "autentificare":
			info.Security = value
		case "name", "nume":
			if info.Interface == "" {
				info.Interface = value
			}
		}
	}
	info.fillDerived()
	return info
}

// normaliseBand turns the various spellings of a band into one form.
func normaliseBand(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(v, "2.4"), strings.HasPrefix(v, "2,4"):
		return "2.4 GHz"
	case strings.HasPrefix(v, "5"):
		return "5 GHz"
	case strings.HasPrefix(v, "6"):
		return "6 GHz"
	}
	return v
}

// parseNetworksetupMedia reads `networksetup -getmedia <iface>` on macOS,
// whose useful line looks like "Current: 1000baseT <full-duplex>".
func parseNetworksetupMedia(out string) LinkInfo {
	info := LinkInfo{Type: LinkEthernet, Source: "networksetup"}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "Current:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "Current:"))
		if strings.Contains(v, "full-duplex") {
			info.Duplex = "full"
		} else if strings.Contains(v, "half-duplex") {
			info.Duplex = "half"
		}
		if n := firstInt(v); n > 0 {
			info.LinkSpeedMbps = n
		}
	}
	info.fillDerived()
	return info
}

// splitLabelled splits a "Label : value" line. Values may contain colons (a
// BSSID does), so only the first separator counts.
func splitLabelled(line string) (string, string, bool) {
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

// ── detection ───────────────────────────────────────────────────────────────

func runTool(name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), linkProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// isWirelessLinux reports whether an interface is a radio, by asking sysfs
// rather than guessing from the name (wlan0, wlp3s0, en0 and wifi0 are all
// real spellings).
func isWirelessLinux(iface string) bool {
	for _, p := range []string{"/sys/class/net/" + iface + "/wireless", "/sys/class/net/" + iface + "/phy80211"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func detectLink(iface string) LinkInfo {
	if !safeIface.MatchString(iface) {
		return LinkInfo{Type: LinkUnknown, Note: "nume de interfață neașteptat"}
	}
	switch runtime.GOOS {
	case "linux":
		return detectLinkLinux(iface)
	case "darwin":
		return detectLinkDarwin(iface)
	case "windows":
		return detectLinkWindows(iface)
	}
	return LinkInfo{Interface: iface, Type: LinkUnknown,
		Note: "detecția legăturii nu e implementată pe " + runtime.GOOS}
}

func detectLinkLinux(iface string) LinkInfo {
	if isWirelessLinux(iface) {
		if out, ok := runTool("iw", "dev", iface, "link"); ok && strings.Contains(out, "Connected to") {
			info := parseIWLink(out)
			info.Interface = iface
			mergeNmcli(&info)
			return info
		}
		if out, ok := runTool("nmcli", "-t", "-f",
			"active,ssid,bssid,chan,freq,rate,signal,security", "dev", "wifi"); ok {
			info := parseNmcliWifi(out)
			if info.SSID != "" {
				info.Interface = iface
				return info
			}
		}
		return LinkInfo{Interface: iface, Type: LinkWiFi,
			Note: "instalează iw sau nmcli pentru detalii de radio"}
	}

	info := LinkInfo{Interface: iface, Type: LinkEthernet, Source: "sysfs"}
	if b, err := os.ReadFile("/sys/class/net/" + iface + "/speed"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
			info.LinkSpeedMbps = n
		}
	}
	if b, err := os.ReadFile("/sys/class/net/" + iface + "/duplex"); err == nil {
		// sysfs reports "unknown" when the port has no carrier, which is not
		// worth printing.
		if d := strings.TrimSpace(string(b)); d != "" && d != "unknown" {
			info.Duplex = d
		}
	}
	info.fillDerived()
	return info
}

// mergeNmcli fills in what iw does not report: security, and the signal as a
// percentage. It is additive, so a value iw already gave is kept.
func mergeNmcli(info *LinkInfo) {
	out, ok := runTool("nmcli", "-t", "-f",
		"active,ssid,bssid,chan,freq,rate,signal,security", "dev", "wifi")
	if !ok {
		return
	}
	extra := parseNmcliWifi(out)
	if extra.SSID == "" || (info.SSID != "" && extra.SSID != info.SSID) {
		return
	}
	if info.Security == "" {
		info.Security = extra.Security
	}
	if info.SignalPercent == 0 {
		info.SignalPercent = extra.SignalPercent
	}
	if info.Channel == 0 {
		info.Channel = extra.Channel
	}
}

func detectLinkDarwin(iface string) LinkInfo {
	// system_profiler is the one source that still works on current macOS; the
	// old airport binary was removed in 14.4.
	if out, ok := runTool("system_profiler", "SPAirPortDataType"); ok &&
		strings.Contains(out, "Current Network Information:") {
		info := parseSystemProfilerAirPort(out)
		info.Interface = iface
		return info
	}
	if out, ok := runTool("networksetup", "-getairportnetwork", iface); ok &&
		!strings.Contains(out, "not associated") {
		if i := strings.Index(out, ": "); i >= 0 {
			info := LinkInfo{Interface: iface, Type: LinkWiFi, Source: "networksetup",
				SSID: strings.TrimSpace(out[i+2:])}
			info.fillDerived()
			return info
		}
	}
	if out, ok := runTool("networksetup", "-getmedia", iface); ok {
		info := parseNetworksetupMedia(out)
		info.Interface = iface
		return info
	}
	return LinkInfo{Interface: iface, Type: LinkUnknown}
}

func detectLinkWindows(iface string) LinkInfo {
	if out, ok := runTool("netsh", "wlan", "show", "interfaces"); ok &&
		strings.Contains(strings.ToLower(out), "ssid") {
		info := parseNetshWlan(out)
		if info.SSID != "" {
			if info.Interface == "" {
				info.Interface = iface
			}
			return info
		}
	}
	return LinkInfo{Interface: iface, Type: LinkEthernet, Source: "netsh",
		Note: "nicio conexiune Wi-Fi activă; presupun cablu"}
}

// ── monitor ─────────────────────────────────────────────────────────────────

// LinkMonitor keeps a cached snapshot of the link, refreshed in the background.
//
// The cache exists because detection shells out to tools that can take seconds
// (system_profiler notably), and neither the page load nor the end of a test
// may wait on that. Nothing reads the link during a measurement, so a probe can
// never perturb a result.
type LinkMonitor struct {
	iface string
	mu    sync.RWMutex
	info  LinkInfo
}

func NewLinkMonitor(iface string) *LinkMonitor {
	return &LinkMonitor{iface: iface, info: LinkInfo{Interface: iface, Type: LinkUnknown}}
}

// Info returns the last snapshot.
func (m *LinkMonitor) Info() LinkInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.info
}

// Refresh probes now and stores the result. Safe to call from anywhere.
func (m *LinkMonitor) Refresh() LinkInfo {
	info := detectLink(m.iface)
	info.Interface = m.iface
	m.mu.Lock()
	m.info = info
	m.mu.Unlock()
	return info
}

// Start refreshes in the background, so a laptop that moves from cable to
// Wi-Fi mid-session reports what it is on now rather than what it started on.
func (m *LinkMonitor) Start(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			m.Refresh()
		}
	}()
}

// Describe renders the link as one line for the terminal banner.
func (l LinkInfo) Describe() string {
	switch l.Type {
	case LinkWiFi:
		parts := []string{"Wi-Fi"}
		if l.SSID != "" {
			parts = append(parts, l.SSID)
		}
		var radio []string
		if l.Band != "" {
			radio = append(radio, l.Band)
		}
		if l.Channel > 0 {
			ch := "canal " + strconv.Itoa(l.Channel)
			if l.WidthMHz > 0 {
				ch += "/" + strconv.Itoa(l.WidthMHz) + " MHz"
			}
			radio = append(radio, ch)
		}
		if l.PHYMode != "" {
			radio = append(radio, l.PHYMode)
		}
		if l.SignalDBm != 0 {
			radio = append(radio, strconv.Itoa(l.SignalDBm)+" dBm")
		} else if l.SignalPercent > 0 {
			radio = append(radio, strconv.Itoa(l.SignalPercent)+"%")
		}
		if len(radio) > 0 {
			parts = append(parts, "("+strings.Join(radio, ", ")+")")
		}
		if l.TxRateMbps > 0 {
			parts = append(parts, "· rată radio "+FormatMbps(l.TxRateMbps))
		}
		return strings.Join(parts, " ")
	case LinkEthernet:
		s := "Cablu"
		if l.LinkSpeedMbps > 0 {
			s += " " + strconv.Itoa(l.LinkSpeedMbps) + " Mbps"
		}
		if l.Duplex != "" {
			s += " " + l.Duplex + "-duplex"
		}
		return s
	}
	return "tip de legătură necunoscut"
}
