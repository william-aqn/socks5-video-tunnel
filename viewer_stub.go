//go:build !windows
// +build !windows

package main

func StartDebugUI(mode, initialURL, localURL string, x, y int, onURLChange func(string)) {
	// Not supported on this platform
}

func UpdateCaptureStatus(success bool) {
	// Not supported on this platform
}
