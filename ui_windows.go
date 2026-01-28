//go:build windows
// +build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	procCreateWindowExW            = moduser32.NewProc("CreateWindowExW")
	procRegisterClassExW           = moduser32.NewProc("RegisterClassExW")
	procDefWindowProcW             = moduser32.NewProc("DefWindowProcW")
	procGetMessageW                = moduser32.NewProc("GetMessageW")
	procTranslateMessage           = moduser32.NewProc("TranslateMessage")
	procDispatchMessageW           = moduser32.NewProc("DispatchMessageW")
	procShowWindow                 = moduser32.NewProc("ShowWindow")
	procUpdateWindow               = moduser32.NewProc("UpdateWindow")
	procGetModuleHandleW           = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procPostQuitMessage            = moduser32.NewProc("PostQuitMessage")
	procDestroyWindow              = moduser32.NewProc("DestroyWindow")
	procSetLayeredWindowAttributes = moduser32.NewProc("SetLayeredWindowAttributes")
	procGetWindowRect              = moduser32.NewProc("GetWindowRect")
	procSetWindowTextW             = moduser32.NewProc("SetWindowTextW")
	procLoadCursorW                = moduser32.NewProc("LoadCursorW")
	procEnumWindows                = moduser32.NewProc("EnumWindows")
	procIsWindowVisible            = moduser32.NewProc("IsWindowVisible")
	procAdjustWindowRectEx         = moduser32.NewProc("AdjustWindowRectEx")
)

const (
	WS_EX_LAYERED       = 0x00080000
	WS_EX_TOPMOST       = 0x00000008
	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_VISIBLE          = 0x10000000
	SW_SHOW             = 5
	WM_DESTROY          = 0x0002
	WM_KEYDOWN          = 0x0100
	WM_LBUTTONDOWN      = 0x0201
	WM_NCHITTEST        = 0x0084
	HTCAPTION           = 2
	LWA_ALPHA           = 0x00000002
	VK_RETURN           = 0x0D
	VK_ESCAPE           = 0x1B
	IDC_ARROW           = 32512
)

type WNDCLASSEXW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     syscall.Handle
}

type RECT struct {
	Left, Top, Right, Bottom int32
}

type MSG struct {
	HWnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   struct{ X, Y int32 }
}

var (
	selectedX, selectedY int
	selectionConfirmed   bool
)

func wndProc(hwnd syscall.Handle, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case WM_DESTROY:
		procPostQuitMessage.Call(0)
		return 0
	case WM_NCHITTEST:
		// Позволяет перетаскивать окно за любую часть
		ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
		if ret == 1 { // HTCLIENT
			return HTCAPTION
		}
		return ret
	case WM_KEYDOWN:
		if wparam == VK_RETURN {
			var rect RECT
			procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
			selectedX = int(rect.Left)
			selectedY = int(rect.Top)
			selectionConfirmed = true
			procDestroyWindow.Call(uintptr(hwnd))
		} else if wparam == VK_ESCAPE {
			procDestroyWindow.Call(uintptr(hwnd))
		}
	}
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return ret
}

func SelectCaptureArea() (int, int, error) {
	selectionConfirmed = false
	className, _ := syscall.UTF16PtrFromString("SelectAreaWindowClass")
	windowName, _ := syscall.UTF16PtrFromString("Перетащите это окно на область видео и нажмите ENTER")

	instance, _, _ := procGetModuleHandleW.Call(0)
	cursor, _, _ := procLoadCursorW.Call(0, uintptr(IDC_ARROW))

	wc := WNDCLASSEXW{
		WndProc:   syscall.NewCallback(wndProc),
		Instance:  syscall.Handle(instance),
		ClassName: className,
		Cursor:    syscall.Handle(cursor),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))

	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// Рассчитываем размер окна для клиентской области width x height
	rect := RECT{0, 0, int32(width), int32(height)}
	procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&rect)), WS_OVERLAPPEDWINDOW, 0, WS_EX_LAYERED|WS_EX_TOPMOST)
	winW := rect.Right - rect.Left
	winH := rect.Bottom - rect.Top

	hwnd, _, _ := procCreateWindowExW.Call(
		WS_EX_LAYERED|WS_EX_TOPMOST,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		WS_OVERLAPPEDWINDOW,
		100, 100, uintptr(winW), uintptr(winH),
		0, 0, instance, 0,
	)

	if hwnd == 0 {
		return 0, 0, fmt.Errorf("failed to create window")
	}

	// Делаем окно полупрозрачным (128 из 255)
	procSetLayeredWindowAttributes.Call(hwnd, 0, 128, LWA_ALPHA)

	procShowWindow.Call(hwnd, SW_SHOW)
	procUpdateWindow.Call(hwnd)

	var msg MSG
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	if !selectionConfirmed {
		return 0, 0, fmt.Errorf("selection cancelled")
	}

	// Учитываем толщину рамки Windows (приблизительно)
	// В идеале нужно использовать AdjustWindowRect, но для начала попробуем так.
	// Окно 640x480 внутри.
	// При WS_OVERLAPPEDWINDOW рамки обычно 8 пикселей сбоку и заголовок около 30.

	// На самом деле GetWindowRect возвращает координаты всего окна включая рамки.
	// Нам нужны координаты КЛИЕНТСКОЙ области, если мы хотим именно её захватывать.
	// Или мы можем сказать пользователю "наведите рамку".

	// Для простоты: захватываем то, что внутри рамки.
	// Обычно ClientToScreen(hwnd, &point{0,0}) дает координаты верхнего левого угла клиентской области.

	return selectedX - int(rect.Left), selectedY - int(rect.Top), nil
}

func FindCaptureWindow(titlePrefix string) (int, int, error) {
	var foundX, foundY int
	var found bool

	callback := syscall.NewCallback(func(hwnd syscall.Handle, lparam uintptr) uintptr {
		visible, _, _ := procIsWindowVisible.Call(uintptr(hwnd))
		if visible != 0 {
			var buf [256]uint16
			procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), 256)
			title := syscall.UTF16ToString(buf[:])
			if title != "" && len(title) >= len(titlePrefix) && title[:len(titlePrefix)] == titlePrefix {
				var rect RECT
				procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
				// Окно VideoGo Debug Viewer имеет заголовок и рамки.
				// Нам нужны координаты клиентской области, где отрисовывается видео.
				// Используем AdjustWindowRectEx для определения размеров рамок.
				adjRect := RECT{0, 0, 100, 100}
				procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&adjRect)), WS_OVERLAPPEDWINDOW, 0, WS_EX_TOPMOST)

				borderLeft := -adjRect.Left
				borderTop := -adjRect.Top

				foundX = int(rect.Left) + int(borderLeft)
				foundY = int(rect.Top) + int(borderTop) + 25 // 25 пикселей для поля ввода URL
				found = true
				return 0 // Остановить перечисление
			}
		}
		return 1
	})

	procEnumWindows.Call(callback, 0)

	if !found {
		return 0, 0, fmt.Errorf("window not found with prefix: %s", titlePrefix)
	}

	return foundX, foundY, nil
}
