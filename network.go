package main

import (
	"fmt"
	"net"
)

func getLANAddresses() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var ips []net.IP
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
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.IsLoopback() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				if isPrivateV4(ip4) {
					ips = append(ips, ip4)
				}
			} else if isPrivateV6(ip) {
				ips = append(ips, ip)
			}
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no private LAN addresses found; this tool only runs on RFC1918/link-local networks")
	}
	return ips, nil
}

func isPrivateV4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	if ip4[0] == 10 {
		return true
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return true
	}
	if ip4[0] == 192 && ip4[1] == 168 {
		return true
	}
	if ip4[0] == 169 && ip4[1] == 254 {
		return true
	}
	return false
}

func isPrivateV6(ip net.IP) bool {
	if len(ip) != net.IPv6len {
		return false
	}
	// ULA fc00::/7
	return (ip[0] & 0xfe) == 0xfc
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
