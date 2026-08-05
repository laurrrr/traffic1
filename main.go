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

func main() {
	port := flag.Int("port", 8080, "listen port")
	flag.Parse()

	ips, err := getLANAddresses()
	if err != nil {
		log.Fatalf("Refusing to start: %v", err)
	}

	fmt.Println()
	fmt.Println("  LAN Speed Test")
	fmt.Println("  " + strings.Repeat("─", 38))
	fmt.Println("  Open on your phone:")
	fmt.Println()
	for _, ip := range ips {
		fmt.Printf("    %s\n", formatURL(ip, *port))
	}
	fmt.Println()

	firstURL := formatURL(ips[0], *port)
	if qr, err := qrcode.New(firstURL, qrcode.Medium); err == nil {
		fmt.Println(qr.ToSmallString(false))
	}

	srv := newServer()
	webContent, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(webContent)))
	mux.HandleFunc("/ws", srv.handleWebSocket)

	var listeners []net.Listener
	for _, ip := range ips {
		addr := formatListenAddr(ip, *port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("  warning: cannot bind %s: %v", addr, err)
			continue
		}
		listeners = append(listeners, ln)
	}

	if len(listeners) == 0 {
		log.Fatal("Failed to bind to any LAN address")
	}

	log.Printf("Ready — waiting for connections")

	for i := 0; i < len(listeners)-1; i++ {
		go func(ln net.Listener) {
			if err := http.Serve(ln, mux); err != nil {
				log.Printf("listener error: %v", err)
			}
		}(listeners[i])
	}
	log.Fatal(http.Serve(listeners[len(listeners)-1], mux))
}
