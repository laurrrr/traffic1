# lantest — LAN Throughput & Latency Tester

Test your local network speed between a laptop and a phone using nothing but a
browser. No app install, no certificates, plain HTTP.

## Quickstart

```bash
go build -o lantest .
./lantest
```

The server prints every private LAN IP as a clickable URL and shows a QR code
for the first one. Scan the QR or type the URL into your phone's browser, then
tap **Start Test**.

### Options

```
./lantest -port 9090   # use a different port (default: 8080)
```

## What it measures

| Metric | Method |
|--------|--------|
| Latency | 50 WebSocket PING/PONG round trips; reports min, p50, p95, jitter |
| Download | Server streams random chunks for 10 s; client samples every 250 ms |
| Upload | Client streams chunks for 10 s; server samples every 250 ms |
| Packet loss | **NOT MEASURED** — TCP hides retransmissions |

Results use p50/p95 percentiles, never bare means. The tool detects and warns
about browser throttling (sample-interval jitter > 50 ms) and tab visibility
changes. Runs aborted by screen lock are marked incomplete.

## Troubleshooting

### Phone can't reach the server

**AP isolation / client isolation** — many consumer routers and all "guest"
Wi-Fi networks block traffic between wireless clients. If the phone and laptop
are both on Wi-Fi, the router may silently drop packets between them. Fix:

1. Connect the laptop via Ethernet and the phone via Wi-Fi, or
2. Disable AP isolation in your router's settings (often under
   Wireless → Advanced), or
3. Use a different SSID that doesn't have isolation enabled.

### Firewall blocking port 8080

On macOS, allow incoming connections when prompted. On Linux:

```bash
sudo ufw allow 8080/tcp        # or
sudo iptables -I INPUT -p tcp --dport 8080 -j ACCEPT
```

### "No private LAN addresses found"

The tool only binds to RFC 1918 (10.x, 172.16–31.x, 192.168.x) and link-local
addresses. If you're on a VPN or public-only interface, disconnect the VPN or
connect to a local network.

## Export

Tap **Export JSON** after a run. The file includes `schema_version: 1` and a
`caveats` array listing every condition that degraded the run.

## Not implemented (future work)

- **WebRTC DataChannel mode** — unreliable transport for real packet loss and
  jitter measurement. The protocol has a clean seam for this (see protocol.go).
- **IPv6 link-local** — skipped due to zone-ID requirements in URLs.
- **Multiple concurrent clients** — each connection works independently but
  they compete for bandwidth; results will be inaccurate.
- **TLS** — this is a LAN-only tool; plain HTTP is intentional.
