//go:build windows
// +build windows

package main

import (
	"fmt"
	"image"
	"sync"
	"syscall"
	"unsafe"
)

const (
	vcamName = "UnityCaptureStorage"
)

type UnityCaptureHeader struct {
	Width         int32
	Height        int32
	FrameCount    int32
	ReadByteCount int32
	Reserved      int32
}

type WindowsVirtualCamera struct {
	handle syscall.Handle
	addr   uintptr
	data   []byte
	header *UnityCaptureHeader
	pixels []byte
	mu     sync.Mutex
}

func NewVirtualCamera(w, h int) (*WindowsVirtualCamera, error) {
	size := int64(unsafe.Sizeof(UnityCaptureHeader{})) + int64(w*h*4)

	low := uint32(size & 0xFFFFFFFF)
	high := uint32(size >> 32)

	namePtr, _ := syscall.UTF16PtrFromString(vcamName)

	handle, err := syscall.CreateFileMapping(syscall.InvalidHandle, nil, syscall.PAGE_READWRITE, high, low, namePtr)
	if err != nil {
		return nil, fmt.Errorf("CreateFileMapping error: %v", err)
	}

	addr, err := syscall.MapViewOfFile(handle, syscall.FILE_MAP_WRITE, 0, 0, uintptr(size))
	if err != nil {
		syscall.CloseHandle(handle)
		return nil, fmt.Errorf("MapViewOfFile error: %v", err)
	}

	// Превращаем адрес в слайс байтов
	data := unsafe.Slice((*byte)(unsafe.Pointer(addr)), size)

	header := (*UnityCaptureHeader)(unsafe.Pointer(addr))
	header.Width = int32(w)
	header.Height = int32(h)
	header.FrameCount = 0

	pixels := data[unsafe.Sizeof(UnityCaptureHeader{}):]

	return &WindowsVirtualCamera{
		handle: handle,
		addr:   addr,
		data:   data,
		header: header,
		pixels: pixels,
	}, nil
}

func (c *WindowsVirtualCamera) WriteFrame(img *image.RGBA) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(img.Pix) != len(c.pixels) {
		return fmt.Errorf("invalid image size: got %d, want %d", len(img.Pix), len(c.pixels))
	}

	copy(c.pixels, img.Pix)
	c.header.FrameCount++

	return nil
}

func (c *WindowsVirtualCamera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	syscall.UnmapViewOfFile(c.addr)
	syscall.CloseHandle(c.handle)
	return nil
}
