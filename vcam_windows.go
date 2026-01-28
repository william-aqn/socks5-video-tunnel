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

var (
	modmf                     = syscall.NewLazyDLL("mf.dll")
	procMFCreateVirtualCamera = modmf.NewProc("MFCreateVirtualCamera")
)

const (
	MFVirtualCameraType_IPCamera      = 1
	MFVirtualCameraLifetime_Session   = 0
	MFVirtualCameraAccess_CurrentUser = 0
)

var (
	// KSCATEGORY_VIDEO_CAMERA
	KSCATEGORY_VIDEO_CAMERA = syscall.GUID{
		Data1: 0xE5323777,
		Data2: 0xF971,
		Data3: 0x11D0,
		Data4: [8]byte{0x89, 0x40, 0x00, 0xA0, 0xC9, 0x03, 0x49, 0xBE},
	}
)

type WindowsVirtualCamera struct {
	server *MJPEGServer
	vcam   uintptr // IMFVirtualCamera
	mu     sync.Mutex
}

func NewVirtualCamera(w, h int, useMJPEG, useNative bool, name string) (VirtualCamera, error) {
	var server *MJPEGServer
	var err error

	if useMJPEG || useNative {
		server, err = NewMJPEGServer()
		if err != nil {
			return nil, fmt.Errorf("failed to start MJPEG server: %v", err)
		}
		if useMJPEG {
			fmt.Printf("MJPEG Server started at %s\n", server.URL())
		}
	}

	cam := &WindowsVirtualCamera{
		server: server,
	}

	if useNative && server != nil {
		// Попытка зарегистрировать виртуальную камеру через Media Foundation
		if procMFCreateVirtualCamera.Find() == nil {
			namePtr, _ := syscall.UTF16PtrFromString(name)
			urlPtr, _ := syscall.UTF16PtrFromString(server.URL())

			var ppVirtualCamera uintptr
			ret, _, _ := procMFCreateVirtualCamera.Call(
				uintptr(MFVirtualCameraType_IPCamera),
				uintptr(MFVirtualCameraLifetime_Session),
				uintptr(MFVirtualCameraAccess_CurrentUser),
				uintptr(unsafe.Pointer(namePtr)),
				uintptr(unsafe.Pointer(urlPtr)),
				uintptr(unsafe.Pointer(&KSCATEGORY_VIDEO_CAMERA)),
				1,
				uintptr(unsafe.Pointer(&ppVirtualCamera)),
			)

			if ret == 0 { // S_OK
				cam.vcam = ppVirtualCamera
				fmt.Println("Successfully registered Windows Virtual Camera via Media Foundation.")
			} else {
				fmt.Printf("Warning: MFCreateVirtualCamera returned 0x%X. Virtual camera device might not be visible in all apps.\n", ret)
				if useMJPEG {
					fmt.Println("You can still use the MJPEG URL in compatible apps (e.g. OBS or VLC).")
				}
			}
		} else {
			fmt.Println("MFCreateVirtualCamera not found (requires Windows 10 2004+).")
			if useMJPEG {
				fmt.Printf("Please use MJPEG URL: %s\n", server.URL())
			}
		}
	} else if useMJPEG && server != nil {
		fmt.Printf("Native Virtual Camera disabled. Please use MJPEG URL: %s\n", server.URL())
	}

	return cam, nil
}

func (c *WindowsVirtualCamera) WriteFrame(img *image.RGBA) error {
	if c.server != nil {
		c.server.Broadcast(img)
	}
	return nil
}

func (c *WindowsVirtualCamera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.vcam != 0 {
		// В идеале тут нужно вызвать IUnknown::Release
		// Но так как у нас Session lifetime, она должна удалиться сама при закрытии процесса
	}
	if c.server != nil {
		return c.server.Close()
	}
	return nil
}
