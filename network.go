package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// LANAddr is a private address the server is willing to bind to, together with
// the network it belongs to.
type LANAddr struct {
	IP    net.IP
	Net   *net.IPNet
	Iface string
}

// getLANAddresses enumerates every up, non-loopback interface and keeps only
// addresses on private networks. Binding per-address rather than to 0.0.0.0 is
// deliberate: this tool must never be reachable from a public interface.
func getLANAddresses() ([]LANAddr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var out []LANAddr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() {
				continue
			}
			ip := ipNet.IP
			if ip4 := ip.To4(); ip4 != nil {
				if isPrivateV4(ip4) {
					out = append(out, LANAddr{IP: ip4, Net: ipNet, Iface: iface.Name})
				}
				continue
			}
			// IPv6 link-local is skipped on purpose: it needs a zone ID
			// (fe80::1%en0) that browsers will not accept in a URL.
			if isPrivateV6(ip) {
				out = append(out, LANAddr{IP: ip, Net: ipNet, Iface: iface.Name})
			}
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no private LAN addresses found; this tool only runs on RFC1918/link-local networks")
	}
	return out, nil
}

func isPrivateV4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	case ip4[0] == 169 && ip4[1] == 254: // link-local
		return true
	}
	return false
}

func isPrivateV6(ip net.IP) bool {
	if ip.To4() != nil || len(ip) != net.IPv6len {
		return false
	}
	// Unique local addresses, fc00::/7.
	return ip[0]&0xfe == 0xfc
}

// isPrivateHost reports whether a bare host (no port) is an address this server
// is allowed to answer on: loopback, or a private/link-local address. Hostnames
// are rejected — a name can resolve anywhere, and accepting them would defeat
// the point of binding per-address.
func isPrivateHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	// Strip an IPv6 zone, e.g. fe80::1%en0.
	if i := strings.Index(host, "%"); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		return isPrivateV4(ip4)
	}
	return isPrivateV6(ip) || ip.IsLinkLocalUnicast()
}

// hostOnly strips a port from an authority if one is present.
func hostOnly(authority string) string {
	if h, _, err := net.SplitHostPort(authority); err == nil {
		return h
	}
	return strings.Trim(authority, "[]")
}

func formatListenAddr(ip net.IP, port int) string {
	if ip.To4() != nil {
		return fmt.Sprintf("%s:%d", ip, port)
	}
	return fmt.Sprintf("[%s]:%d", ip, port)
}

func formatURL(ip net.IP, port int) string {
	if ip.To4() != nil {
		return fmt.Sprintf("http://%s:%d", ip, port)
	}
	return fmt.Sprintf("http://[%s]:%d", ip, port)
}

// subnetOf renders the network an address belongs to, e.g. 192.168.1.0/24.
// It is the fallback network identity when no SSID can be read, and it is
// stable enough to group runs made on the same LAN.
func subnetOf(a LANAddr) string {
	if a.Net == nil {
		return ""
	}
	return (&net.IPNet{IP: a.IP.Mask(a.Net.Mask), Mask: a.Net.Mask}).String()
}

// NetworkIdentity describes which network a run happened on, so runs can be
// compared only against comparable runs.
type NetworkIdentity struct {
	SSID   string `json:"ssid,omitempty"`
	Subnet string `json:"subnet,omitempty"`
	Key    string `json:"key"`
}

// identifyNetwork prefers the Wi-Fi SSID and falls back to the IPv4 subnet.
// SSID detection is best effort: it shells out to the platform tool and
// returns an empty string on any failure, which is not an error condition.
func identifyNetwork(addrs []LANAddr) NetworkIdentity {
	id := NetworkIdentity{SSID: detectSSID()}
	for _, a := range addrs {
		if a.IP.To4() != nil {
			id.Subnet = subnetOf(a)
			break
		}
	}
	switch {
	case id.SSID != "":
		id.Key = "ssid:" + id.SSID
	case id.Subnet != "":
		id.Key = "subnet:" + id.Subnet
	default:
		id.Key = "unknown"
	}
	return id
}

// detectSSID returns the current Wi-Fi network name, or "" if it cannot be
// determined (wired connection, missing tool, permission denied, no Wi-Fi).
func detectSSID() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "linux":
		if out, err := exec.CommandContext(ctx, "iwgetid", "-r").Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
		if out, err := exec.CommandContext(ctx, "nmcli", "-t", "-f", "active,ssid", "dev", "wifi").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "yes:") {
					return strings.TrimSpace(strings.TrimPrefix(line, "yes:"))
				}
			}
		}
	case "darwin":
		for _, iface := range []string{"en0", "en1"} {
			out, err := exec.CommandContext(ctx, "networksetup", "-getairportnetwork", iface).Output()
			if err != nil {
				continue
			}
			s := strings.TrimSpace(string(out))
			if i := strings.Index(s, ": "); i >= 0 && !strings.Contains(s, "not associated") {
				return strings.TrimSpace(s[i+2:])
			}
		}
	case "windows":
		out, err := exec.CommandContext(ctx, "netsh", "wlan", "show", "interfaces").Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(out), "\n") {
			t := strings.TrimSpace(line)
			// Match "SSID : name" but not "BSSID : ..".
			if !strings.HasPrefix(t, "SSID") {
				continue
			}
			if i := strings.Index(t, ":"); i >= 0 {
				return strings.TrimSpace(t[i+1:])
			}
		}
	}
	return ""
}

// describeUA turns a User-Agent string into something a person can recognise on
// the host screen, e.g. "Android / Chrome". Deliberately crude: this is a label
// in the UI, not a decision input.
func describeUA(ua string) string {
	if ua == "" {
		return "dispozitiv necunoscut"
	}
	var os, browser string
	switch {
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	default:
		os = "necunoscut"
	}
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	default:
		browser = "browser"
	}
	return os + " / " + browser
}
