//go:build !windows
// +build !windows

package main

import "fmt"

func SelectCaptureArea() (int, int, error) {
	return 0, 0, fmt.Errorf("UI is only supported on Windows")
}
