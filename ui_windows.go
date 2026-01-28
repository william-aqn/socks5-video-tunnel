//go:build windows
// +build windows

package main

import (
	"fmt"
	"image/color"
	"log"
	"strings"
	"sync"
	"syscall"
	"time"
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
	procSetWindowPos               = moduser32.NewProc("SetWindowPos")
	procFillRect                   = moduser32.NewProc("FillRect")
	procGetClassNameW              = moduser32.NewProc("GetClassNameW")
	procGetWindowLongW             = moduser32.NewProc("GetWindowLongW")
	procSetWindowLongW             = moduser32.NewProc("SetWindowLongW")

	procCreatePen        = modgdi32.NewProc("CreatePen")
	procRectangle        = modgdi32.NewProc("Rectangle")
	procSetBkMode        = modgdi32.NewProc("SetBkMode")
	procSetTextColor     = modgdi32.NewProc("SetTextColor")
	procTextOutW         = modgdi32.NewProc("TextOutW")
	procCreateSolidBrush = modgdi32.NewProc("CreateSolidBrush")
)

const (
	WS_EX_LAYERED       = 0x00080000
	WS_EX_TOPMOST       = 0x00000008
	WS_EX_TRANSPARENT   = 0x00000020
	WS_EX_NOACTIVATE    = 0x08000000
	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_POPUP            = 0x80000000
	WS_VISIBLE          = 0x10000000
	SW_SHOW             = 5
	WM_DESTROY          = 0x0002
	WM_KEYDOWN          = 0x0100
	WM_LBUTTONDOWN      = 0x0201
	WM_NCHITTEST        = 0x0084
	HTCAPTION           = 2
	LWA_ALPHA           = 0x00000002
	LWA_COLORKEY        = 0x00000001
	VK_RETURN           = 0x0D
	VK_ESCAPE           = 0x1B
	IDC_ARROW           = 32512
	PS_SOLID            = 0
	TRANSPARENT         = 1
	HTTRANSPARENT       = ^uintptr(0)
	GWL_EXSTYLE         = ^uintptr(19)

	HWND_TOPMOST   = ^uintptr(0)
	SWP_NOSIZE     = 0x0001
	SWP_NOMOVE     = 0x0002
	SWP_NOZORDER   = 0x0004
	SWP_NOACTIVATE = 0x0010
)

var (
	procGetStockObject = modgdi32.NewProc("GetStockObject")
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

func SelectCaptureArea() (int, int, error) {
	if selectionDone == nil {
		selectionDone = make(chan bool)
	}

	overlayMu.Lock()
	hwnd := overlayHwnd
	mode := CurrentMode
	ox, oy := overlayX, overlayY
	overlayMu.Unlock()

	if hwnd == 0 {
		// Если оверлей еще не создан, создаем его
		ShowCaptureOverlay(mode, ox, oy)
		// Ждем появления окна
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			overlayMu.Lock()
			hwnd = overlayHwnd
			overlayMu.Unlock()
			if hwnd != 0 {
				break
			}
		}
	}

	if hwnd == 0 {
		return 0, 0, fmt.Errorf("failed to initialize overlay")
	}

	// Переключаем в режим выбора
	setOverlayInteractive(true)

	// Ждем подтверждения или отмены
	confirmed := <-selectionDone

	// Возвращаем в режим захвата
	setOverlayInteractive(false)

	if !confirmed {
		return 0, 0, fmt.Errorf("selection cancelled")
	}

	overlayMu.Lock()
	finalX, finalY := overlayX, overlayY
	overlayMu.Unlock()

	return finalX, finalY, nil
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

			if title != "" && strings.HasPrefix(title, titlePrefix) {
				// Проверяем класс окна, чтобы не захватить лишнего
				var classBuf [256]uint16
				procGetClassNameW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&classBuf[0])), 256)
				className := syscall.UTF16ToString(classBuf[:])

				if className == "VideoGoViewerClass" {
					log.Printf("FindCaptureWindow: Found target window '%s' (Class: %s, HWND: %v)", title, className, hwnd)

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
					foundY = int(rect.Top) + int(borderTop) + 25 // 31 для заголовка, 25 для URL
					found = true
					return 0 // Остановить перечисление
				}
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

var (
	overlayHwnd        syscall.Handle
	overlayMode        string
	overlayX, overlayY int
	overlayMu          sync.Mutex
	overlaySelecting   bool
	selectionDone      chan bool
)

func overlayWndProc(hwnd syscall.Handle, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case WM_NCHITTEST:
		overlayMu.Lock()
		selecting := overlaySelecting
		overlayMu.Unlock()
		if selecting {
			ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
			if ret == 1 { // HTCLIENT
				return HTCAPTION
			}
			return ret
		}
		return HTTRANSPARENT

	case WM_KEYDOWN:
		overlayMu.Lock()
		selecting := overlaySelecting
		overlayMu.Unlock()
		if selecting {
			switch wparam {
			case VK_RETURN:
				var rect RECT
				procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
				overlayMu.Lock()
				overlayX = int(rect.Left) + 2 // Учитываем рамку (2, 22)
				overlayY = int(rect.Top) + 22
				overlayMu.Unlock()
				if selectionDone != nil {
					selectionDone <- true
				}
			case VK_ESCAPE:
				if selectionDone != nil {
					selectionDone <- false
				}
			case VK_LEFT, VK_RIGHT, VK_UP, VK_DOWN:
				var rect RECT
				procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
				x, y := rect.Left, rect.Top
				switch wparam {
				case VK_LEFT:
					x--
				case VK_RIGHT:
					x++
				case VK_UP:
					y--
				case VK_DOWN:
					y++
				}
				procSetWindowPos.Call(uintptr(hwnd), 0, uintptr(x), uintptr(y), 0, 0, SWP_NOSIZE|SWP_NOZORDER|SWP_NOACTIVATE)
			}
		}

	case WM_PAINT:
		var ps struct {
			Hdc         syscall.Handle
			FErase      int32
			RcPaint     RECT
			FRestore    int32
			FIncUpdate  int32
			RgbReserved [32]byte
		}
		hdc, _, _ := procBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			overlayMu.Lock()
			mode := overlayMode
			selecting := overlaySelecting
			overlayMu.Unlock()

			// Заливаем фон Magenta (BGR: 0xFF00FF) для прозрачности
			magentaBrush, _, _ := procCreateSolidBrush.Call(0xFF00FF)
			procFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&ps.RcPaint)), magentaBrush)
			procDeleteObject.Call(magentaBrush)

			// Рисуем рамку
			// Используем ярко-зеленый (0x00FF00)
			pen, _, _ := procCreatePen.Call(uintptr(PS_SOLID), 2, 0x00FF00)
			oldPen, _, _ := procSelectObject.Call(hdc, pen)

			brush, _, _ := procGetStockObject.Call(5) // HOLLOW_BRUSH
			oldBrush, _, _ := procSelectObject.Call(hdc, brush)

			// Рисуем рамку вокруг области захвата (640x480)
			// Окно имеет размер width+4 x height+24
			// Область захвата внутри окна: (2, 22) до (width+2, height+22)
			// Рисуем прямоугольник от (0, 20) до (width+4, height+24), при толщине пера 2
			// его внутренняя граница будет как раз (2, 22) - (width+2, height+22)
			procRectangle.Call(hdc, 0, 20, uintptr(width+4), uintptr(height+24))

			// Текст над рамкой
			procSetBkMode.Call(hdc, uintptr(TRANSPARENT))
			procSetTextColor.Call(hdc, 0x00FF00)

			var text string
			if selecting {
				text = "Selecting: ENTER confirm, ESC cancel, ARROWS move"
			} else {
				text = fmt.Sprintf("Capture: %s", mode)
			}
			text16, _ := syscall.UTF16FromString(text)
			procTextOutW.Call(hdc, 7, 2, uintptr(unsafe.Pointer(&text16[0])), uintptr(len(text)))

			// Рисуем контрольные точки для визуализации
			markers := ClientMarkers
			if mode == "server" {
				markers = ServerMarkers
			}

			drawVisualPoint := func(x, y int, c color.RGBA) {
				colorRef := uint32(c.R) | (uint32(c.G) << 8) | (uint32(c.B) << 16)
				br, _, _ := procCreateSolidBrush.Call(uintptr(colorRef))
				r := RECT{int32(x), int32(y), int32(x + markerSize), int32(y + markerSize)}
				procFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&r)), br)
				procDeleteObject.Call(br)
			}

			// Смещение области захвата в окне: (2, 22)
			drawVisualPoint(2+markerOffset, 22+markerOffset, markers.TL)
			drawVisualPoint(2+width-markerSize-markerOffset, 22+markerOffset, markers.TR)
			drawVisualPoint(2+markerOffset, 22+height-markerSize-markerOffset, markers.BL)
			drawVisualPoint(2+width-markerSize-markerOffset, 22+height-markerSize-markerOffset, markers.BR)

			procSelectObject.Call(hdc, oldPen)
			procSelectObject.Call(hdc, oldBrush)
			procDeleteObject.Call(pen)

			procEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return ret
}

func ShowCaptureOverlay(mode string, x, y int) {
	overlayMu.Lock()
	if overlayHwnd != 0 {
		overlayMode = mode
		overlayX = x
		overlayY = y
		hwnd := overlayHwnd
		overlayMu.Unlock()
		UpdateCaptureOverlay(x, y)
		// Форсируем перерисовку
		procInvalidateRect.Call(uintptr(hwnd), 0, 1)
		return
	}
	overlayMode = mode
	overlayX = x
	overlayY = y
	overlayMu.Unlock()

	go func() {
		className, _ := syscall.UTF16PtrFromString("CaptureOverlayClass")
		windowName, _ := syscall.UTF16PtrFromString("Capture Overlay")

		instance, _, _ := procGetModuleHandleW.Call(0)
		cursor, _, _ := procLoadCursorW.Call(0, uintptr(IDC_ARROW))

		wc := WNDCLASSEXW{
			WndProc:   syscall.NewCallback(overlayWndProc),
			Instance:  syscall.Handle(instance),
			ClassName: className,
			Cursor:    syscall.Handle(cursor),
		}
		wc.Size = uint32(unsafe.Sizeof(wc))
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

		hwnd, _, _ := procCreateWindowExW.Call(
			WS_EX_LAYERED|WS_EX_TOPMOST|WS_EX_TRANSPARENT|WS_EX_NOACTIVATE,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(windowName)),
			WS_POPUP|WS_VISIBLE,
			uintptr(x-2), uintptr(y-22), uintptr(width+4), uintptr(height+24),
			0, 0, instance, 0,
		)

		if hwnd == 0 {
			fmt.Printf("Overlay Error: Failed to create window for %s at (%d, %d)\n", mode, x, y)
			return
		}

		overlayMu.Lock()
		overlayHwnd = syscall.Handle(hwnd)
		overlayMu.Unlock()

		// Цвет Magenta (0xFF00FF) становится прозрачным
		procSetLayeredWindowAttributes.Call(hwnd, 0xFF00FF, 0, LWA_COLORKEY)

		// Периодически подтверждаем статус TOPMOST
		go func() {
			for {
				time.Sleep(200 * time.Millisecond)
				overlayMu.Lock()
				h := overlayHwnd
				ox, oy := overlayX, overlayY
				selecting := overlaySelecting
				overlayMu.Unlock()
				if h == 0 {
					break
				}
				if selecting {
					// В режиме выбора не форсируем позицию, так как окно перемещается
					continue
				}
				// Ре-активация TOPMOST статуса и позиции
				procSetWindowPos.Call(uintptr(h), HWND_TOPMOST, uintptr(ox-2), uintptr(oy-22), 0, 0, SWP_NOSIZE|SWP_NOACTIVATE)
			}
		}()

		var msg MSG
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
}

func setOverlayInteractive(interactive bool) {
	overlayMu.Lock()
	overlaySelecting = interactive
	hwnd := overlayHwnd
	overlayMu.Unlock()

	if hwnd == 0 {
		return
	}

	style, _, _ := procGetWindowLongW.Call(uintptr(hwnd), GWL_EXSTYLE)
	if interactive {
		// Убираем прозрачность для кликов и NOACTIVATE
		style &^= (WS_EX_TRANSPARENT | WS_EX_NOACTIVATE)
	} else {
		style |= (WS_EX_TRANSPARENT | WS_EX_NOACTIVATE)
	}
	procSetWindowLongW.Call(uintptr(hwnd), GWL_EXSTYLE, style)

	// Форсируем перерисовку и смену Z-order
	procSetWindowPos.Call(uintptr(hwnd), HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE)
	// procInvalidateRect объявлен в viewer_windows.go
	procInvalidateRect.Call(uintptr(hwnd), 0, 1)
}

func UpdateCaptureOverlay(x, y int) {
	overlayMu.Lock()
	hwnd := overlayHwnd
	overlayX = x
	overlayY = y
	overlayMu.Unlock()

	if hwnd != 0 {
		// Используем HWND_TOPMOST для принудительного вывода на передний план
		procSetWindowPos.Call(uintptr(hwnd), HWND_TOPMOST, uintptr(x-2), uintptr(y-22), 0, 0, SWP_NOSIZE|SWP_NOACTIVATE)
		// Форсируем перерисовку, чтобы рамка не "залипала"
		procInvalidateRect.Call(uintptr(hwnd), 0, 1)
	}
}
