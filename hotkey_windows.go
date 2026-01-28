//go:build windows
// +build windows

package main

import (
	"fmt"
	"unsafe"
)

var (
	procRegisterHotKey   = moduser32.NewProc("RegisterHotKey")
	procUnregisterHotKey = moduser32.NewProc("UnregisterHotKey")
)

const (
	MOD_ALT     = 0x0001
	MOD_CONTROL = 0x0002
	WM_HOTKEY   = 0x0312
)

func StartHotkeyHandler(mode string, onHotkey func()) {
	go func() {
		vk := uint32(0x53) // Default 'S' for server
		keyName := "Ctrl+Alt+S"
		if mode == "client" {
			vk = 0x43 // 'C' for client
			keyName = "Ctrl+Alt+C"
		}

		// Регистрация горячей клавиши
		// ID = 1
		ret, _, _ := procRegisterHotKey.Call(0, 1, MOD_CONTROL|MOD_ALT, uintptr(vk))
		if ret == 0 {
			fmt.Printf("Warning: Failed to register hotkey %s\n", keyName)
			return
		}
		defer procUnregisterHotKey.Call(0, 1)

		fmt.Printf("Hotkey %s registered to change capture area.\n", keyName)

		var msg MSG
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			if msg.Message == WM_HOTKEY {
				onHotkey()
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
}
