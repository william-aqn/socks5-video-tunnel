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

	VK_LEFT  = 0x25
	VK_UP    = 0x26
	VK_RIGHT = 0x27
	VK_DOWN  = 0x28

	HK_SELECT = 1
	HK_LEFT   = 2
	HK_UP     = 3
	HK_RIGHT  = 4
	HK_DOWN   = 5
)

func StartHotkeyHandler(mode string, onHotkey func(id int)) {
	go func() {
		vkSelect := uint32(0x53) // Default 'S' for server
		keyName := "Ctrl+Alt+S"
		if mode == "client" {
			vkSelect = 0x43 // 'C' for client
			keyName = "Ctrl+Alt+C"
		}

		// Регистрация горячих клавиш
		procRegisterHotKey.Call(0, HK_SELECT, MOD_CONTROL|MOD_ALT, uintptr(vkSelect))
		procRegisterHotKey.Call(0, HK_LEFT, MOD_CONTROL|MOD_ALT, uintptr(VK_LEFT))
		procRegisterHotKey.Call(0, HK_UP, MOD_CONTROL|MOD_ALT, uintptr(VK_UP))
		procRegisterHotKey.Call(0, HK_RIGHT, MOD_CONTROL|MOD_ALT, uintptr(VK_RIGHT))
		procRegisterHotKey.Call(0, HK_DOWN, MOD_CONTROL|MOD_ALT, uintptr(VK_DOWN))

		defer func() {
			procUnregisterHotKey.Call(0, HK_SELECT)
			procUnregisterHotKey.Call(0, HK_LEFT)
			procUnregisterHotKey.Call(0, HK_UP)
			procUnregisterHotKey.Call(0, HK_RIGHT)
			procUnregisterHotKey.Call(0, HK_DOWN)
		}()

		fmt.Printf("Hotkeys registered:\n")
		fmt.Printf("  %s: Change capture area (select window)\n", keyName)
		fmt.Printf("  Ctrl+Alt+Arrows: Fine-tune capture area position\n")

		var msg MSG
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			if msg.Message == WM_HOTKEY {
				onHotkey(int(msg.WParam))
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
}
