//go:build !windows
// +build !windows

package main

import (
	"errors"
	"image"
)

type StubVirtualCamera struct{}

func NewVirtualCamera(w, h int, useMJPEG, useNative bool, name string, mjpegPort int) (VirtualCamera, error) {
	return nil, errors.New("virtual camera device is not yet implemented for this platform")
}

func (c *StubVirtualCamera) WriteFrame(img *image.RGBA) error {
	return nil
}

func (c *StubVirtualCamera) GetURL() string {
	return ""
}

func (c *StubVirtualCamera) Close() error {
	return nil
}
