//go:build desktop && !production

package main

// Wails needs its own build tag on top of ours.
//
// With `desktop` but without `production`, Wails links a stub whose entire
// behaviour is to return "Wails applications will not build without the correct
// build tags" — and it returns it at *runtime*, after the server has already
// started, bound its listeners and printed the QR code. That looks like a
// server bug rather than a build mistake, which is exactly how it was first
// reported.
//
// This file turns that runtime failure into a build failure. The undefined
// function below is the error message: the compiler prints its name.
func init() {
	build_the_desktop_shell_with_tags_desktop_production_plus_webkit2_41_on_linux()
}
