//go:build !windows
// +build !windows

package main

func StartHotkeyHandler(mode string, onHotkey func()) {
	// Not supported on this platform
}
