//go:build !windows
// +build !windows

package main

import (
	"errors"
	"image"
)

type WindowsVirtualCamera struct{}

func NewVirtualCamera(w, h int) (*WindowsVirtualCamera, error) {
	return nil, errors.New("virtual camera is only supported on Windows")
}

func (c *WindowsVirtualCamera) WriteFrame(img *image.RGBA) error {
	return nil
}

func (c *WindowsVirtualCamera) Close() error {
	return nil
}
