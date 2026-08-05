//go:build desktop

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// runShell opens the desktop window.
//
// The window is served by the *same* http.Handler as the LAN listeners, so the
// desktop frontend and the phone frontend are the identical bytes. The window
// then talks to the server over a normal WebSocket on 127.0.0.1, exactly like
// the phone does over the LAN address — no Wails bindings are involved in any
// measurement. The bindings below exist only for things a browser tab cannot
// do: a native save dialog and opening a link in the system browser.
func runShell(cfg shellConfig) {
	app := &DesktopApp{cfg: cfg}

	err := wails.Run(&options.App{
		Title:     "lantest — test de rețea LAN",
		Width:     1180,
		Height:    840,
		MinWidth:  920,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Handler: cfg.Handler,
		},
		OnStartup: app.startup,
		Bind:      []any{app},
	})
	if err != nil {
		log.Fatalf("desktop shell failed: %v", err)
	}
}

// DesktopApp holds the Wails bindings. Deliberately tiny: window plumbing and
// file saving only.
type DesktopApp struct {
	cfg shellConfig
	ctx context.Context
}

func (a *DesktopApp) startup(ctx context.Context) {
	a.ctx = ctx
}

// SaveFile opens a native save dialog and writes base64-encoded data to the
// chosen path. Returns the path, or an empty string if the user cancelled.
func (a *DesktopApp) SaveFile(name, b64 string) (string, error) {
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		DefaultFilename:      name,
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil // cancelled
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("date invalide: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// OpenURL opens a link in the system browser rather than inside the window.
func (a *DesktopApp) OpenURL(url string) {
	wailsruntime.BrowserOpenURL(a.ctx, url)
}

// LANURLs reports the addresses a phone can use to reach this server.
func (a *DesktopApp) LANURLs() []string {
	return a.cfg.URLs
}
