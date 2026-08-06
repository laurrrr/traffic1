package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web
var webFS embed.FS

// shellConfig is what the desktop and headless shells both receive. Keeping the
// two shells behind one struct means the server is built and started exactly
// once, in exactly one place, whichever shell is compiled in.
type shellConfig struct {
	Server  *Server
	Handler http.Handler
	URLs    []string
	Port    int
}

func main() {
	port := flag.Int("port", 8080, "listen port")
	streams := flag.Int("streams", 4, "default number of parallel streams (1, 4 or 8)")
	historyFile := flag.String("history", "", "history file path (default: user config dir)")
	flag.Parse()

	addrs, err := getLANAddresses()
	if err != nil {
		log.Fatalf("Refusing to start: %v", err)
	}

	historyPath := *historyFile
	if historyPath == "" {
		if p, err := defaultHistoryPath(); err == nil {
			historyPath = p
		} else {
			log.Printf("history disabled (no user config dir): %v", err)
		}
	}
	hist := NewHistoryStore(historyPath)

	content, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embedded assets: %v", err)
	}

	network := identifyNetwork(addrs)
	srv := newServer(*port, *streams, hist, network, content)
	mux := srv.Mux()

	urls := printBanner(addrs, network, *port, historyPath)
	srv.SetLANURLs(urls)

	if n := startListeners(addrs, *port, mux); n == 0 {
		log.Fatal("Failed to bind to any LAN address")
	}

	runShell(shellConfig{Server: srv, Handler: mux, URLs: urls, Port: *port})
}

// startListeners binds one listener per private address, plus loopback so the
// desktop window can reach the same server the phone does. Binding per address
// rather than to 0.0.0.0 keeps the tool off any public interface.
func startListeners(addrs []LANAddr, port int, handler http.Handler) int {
	targets := []string{fmt.Sprintf("127.0.0.1:%d", port)}
	for _, a := range addrs {
		targets = append(targets, formatListenAddr(a.IP, port))
	}

	live := 0
	for _, target := range targets {
		ln, err := net.Listen("tcp", target)
		if err != nil {
			log.Printf("  warning: cannot bind %s: %v", target, err)
			continue
		}
		live++
		go func(l net.Listener) {
			if err := http.Serve(l, handler); err != nil {
				log.Printf("listener %s stopped: %v", l.Addr(), err)
			}
		}(ln)
	}
	return live
}

func printBanner(addrs []LANAddr, network NetworkIdentity, port int, historyPath string) []string {
	urls := make([]string, 0, len(addrs))
	for _, a := range addrs {
		urls = append(urls, formatURL(a.IP, port))
	}

	fmt.Println()
	fmt.Println("  lantest — test de throughput și latență în LAN")
	fmt.Println("  " + strings.Repeat("─", 46))
	if network.SSID != "" {
		fmt.Printf("  Rețea: %s (%s)\n", network.SSID, network.Subnet)
	} else if network.Subnet != "" {
		fmt.Printf("  Rețea: %s\n", network.Subnet)
	}
	if historyPath != "" {
		fmt.Printf("  Istoric: %s\n", historyPath)
	}
	fmt.Println()
	fmt.Println("  Deschide pe telefon:")
	fmt.Println()
	for _, u := range urls {
		fmt.Printf("    %s\n", u)
	}
	fmt.Println()

	if len(urls) > 0 {
		if qr, err := qrcode.New(urls[0], qrcode.Medium); err == nil {
			fmt.Println(qr.ToSmallString(false))
		}
	}
	return urls
}
