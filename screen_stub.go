//go:build !windows
// +build !windows

package main

import (
	"fmt"
	"image"
)

func CaptureScreen(x, y, w, h int) (*image.RGBA, error) {
	return nil, fmt.Errorf("CaptureScreen is not supported on this platform")
}
