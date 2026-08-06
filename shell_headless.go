//go:build !desktop

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

// runShell is the server-only shell: no window, no CGO, no GUI libraries. This
// is the default build so that `go build ./...`, `go vet` and `go test` work on
// any machine and in CI without GTK/WebKit installed. Build with
// `-tags desktop` for the windowed application.
func runShell(cfg shellConfig) {
	log.Printf("Gata — server pornit pe portul %d, %d adrese LAN. Ctrl-C pentru oprire.",
		cfg.Port, len(cfg.URLs))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("oprire")
}
