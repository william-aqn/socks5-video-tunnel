//go:build !windows
// +build !windows

package main

import "fmt"

func SelectCaptureArea() (int, int, error) {
	return 0, 0, fmt.Errorf("UI is only supported on Windows")
}

func FindCaptureWindow(titlePrefix string) (int, int, error) {
	return 0, 0, fmt.Errorf("FindCaptureWindow is only supported on Windows")
}

func ShowCaptureOverlay(mode string, x, y int) {
}

func UpdateCaptureOverlay(x, y int) {
}
