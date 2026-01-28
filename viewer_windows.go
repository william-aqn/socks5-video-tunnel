//go:build windows
// +build windows

package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	procSetDIBitsToDevice = modgdi32.NewProc("SetDIBitsToDevice")
	procInvalidateRect    = moduser32.NewProc("InvalidateRect")
	procGetClientRect     = moduser32.NewProc("GetClientRect")
	procGetWindowTextW    = moduser32.NewProc("GetWindowTextW")
	procBeginPaint        = moduser32.NewProc("BeginPaint")
	procEndPaint          = moduser32.NewProc("EndPaint")
)

const (
	WM_PAINT   = 0x000F
	WM_COMMAND = 0x0111
	WS_CHILD   = 0x40000000
	WS_BORDER  = 0x00800000
)

type ViewerState struct {
	url         string
	onURLChange func(string)
	mu          sync.RWMutex
	hwnd        syscall.Handle
	hwndEdit    syscall.Handle
	hwndStatus  syscall.Handle
	currentBody io.Closer
	gdiBuf      []byte
	gdiW, gdiH  int32
	frameCount  int
	lastCapture time.Time
}

var viewer ViewerState

func UpdateCaptureStatus(success bool) {
	if success {
		viewer.mu.Lock()
		viewer.lastCapture = time.Now()
		viewer.mu.Unlock()
	}
}

func runStatusUpdateLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	for range ticker.C {
		viewer.mu.RLock()
		hwndStatus := viewer.hwndStatus
		frameCount := viewer.frameCount
		lastCapture := viewer.lastCapture
		viewer.mu.RUnlock()

		if hwndStatus == 0 {
			continue
		}

		target := "Remote"
		if CurrentMode == "server" {
			target = "Client"
		} else if CurrentMode == "client" {
			target = "Server"
		}

		captureStatus := "SEARCHING"
		if !lastCapture.IsZero() && time.Since(lastCapture) < 5*time.Second {
			captureStatus = "OK"
		}

		status := fmt.Sprintf("Live: %s | Frames: %d | Capture %s: %s",
			time.Now().Format("15:04:05"), frameCount, target, captureStatus)

		statusPtr, _ := syscall.UTF16PtrFromString(status)
		procSetWindowTextW.Call(uintptr(hwndStatus), uintptr(unsafe.Pointer(statusPtr)))
	}
}

func viewerWndProc(hwnd syscall.Handle, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case WM_DESTROY:
		// Мы не хотим закрывать всё приложение при закрытии окна дебага,
		// но StartDebugUI запускается в горутине, так что выход из цикла сообщений достаточен.
		procPostQuitMessage.Call(0)
		return 0
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
			viewer.mu.RLock()
			if viewer.gdiBuf != nil {
				w, h := viewer.gdiW, viewer.gdiH
				var bi BITMAPINFO
				bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
				bi.Header.Width = w
				bi.Header.Height = -h // top-down
				bi.Header.Planes = 1
				bi.Header.BitCount = 32
				bi.Header.Compression = BI_RGB

				procSetDIBitsToDevice.Call(
					hdc,
					0, 25, uintptr(w), uintptr(h), // Смещение 25 для UI
					0, 0, 0, uintptr(h),
					uintptr(unsafe.Pointer(&viewer.gdiBuf[0])),
					uintptr(unsafe.Pointer(&bi)),
					DIB_RGB_COLORS,
				)
			}
			viewer.mu.RUnlock()
			procEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case WM_COMMAND:
		if wparam == 1001 { // Connect button
			var buf [1024]uint16
			procGetWindowTextW.Call(uintptr(viewer.hwndEdit), uintptr(unsafe.Pointer(&buf[0])), 1024)
			newURL := syscall.UTF16ToString(buf[:])
			viewer.mu.Lock()
			if viewer.currentBody != nil {
				viewer.currentBody.Close()
			}
			viewer.url = newURL
			viewer.mu.Unlock()
			fmt.Printf("Viewer: Changing URL to %s\n", newURL)
			if viewer.onURLChange != nil {
				viewer.onURLChange(newURL)
			}
		}
	}
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return ret
}

func StartDebugUI(mode, initialURL, localURL string, x, y int, onURLChange func(string)) {
	log.Printf("StartDebugUI: Starting for mode %s with URL %s", mode, initialURL)
	runtime.LockOSThread()
	viewer.url = initialURL
	viewer.onURLChange = onURLChange

	className, _ := syscall.UTF16PtrFromString("VideoGoViewerClass")
	title := fmt.Sprintf("[VGO-%s] VideoGo Debug Viewer", strings.ToUpper(mode))
	if localURL != "" {
		title = fmt.Sprintf("[VGO-%s] VideoGo Debug Viewer (My MJPEG: %s)", strings.ToUpper(mode), localURL)
	}
	windowName, _ := syscall.UTF16PtrFromString(title)

	instance, _, _ := procGetModuleHandleW.Call(0)
	cursor, _, _ := procLoadCursorW.Call(0, uintptr(IDC_ARROW))

	wc := WNDCLASSEXW{
		WndProc:   syscall.NewCallback(viewerWndProc),
		Instance:  syscall.Handle(instance),
		ClassName: className,
		Cursor:    syscall.Handle(cursor),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// Рассчитываем размер окна для нужной клиентской области
	// 640x(480 + 25 сверху для URL + 25 снизу для статуса)
	rect := RECT{0, 0, int32(width), int32(height + 50)}
	procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&rect)), WS_OVERLAPPEDWINDOW, 0, WS_EX_TOPMOST)
	winW := rect.Right - rect.Left
	winH := rect.Bottom - rect.Top

	hwnd, _, _ := procCreateWindowExW.Call(
		WS_EX_TOPMOST,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		WS_OVERLAPPEDWINDOW|WS_VISIBLE,
		uintptr(x), uintptr(y), uintptr(winW), uintptr(winH),
		0, 0, instance, 0,
	)

	if hwnd == 0 {
		fmt.Println("Failed to create viewer window")
		return
	}
	viewer.hwnd = syscall.Handle(hwnd)

	// Добавим поле ввода URL
	editClassName, _ := syscall.UTF16PtrFromString("EDIT")
	urlPtr, _ := syscall.UTF16PtrFromString(initialURL)
	hwndEdit, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(editClassName)),
		uintptr(unsafe.Pointer(urlPtr)),
		WS_CHILD|WS_VISIBLE|WS_BORDER,
		0, 0, uintptr(width-100), 25,
		hwnd, 0, instance, 0,
	)
	viewer.hwndEdit = syscall.Handle(hwndEdit)

	// Кнопка Connect
	btnClassName, _ := syscall.UTF16PtrFromString("BUTTON")
	btnName, _ := syscall.UTF16PtrFromString("Connect")
	procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(btnClassName)),
		uintptr(unsafe.Pointer(btnName)),
		WS_CHILD|WS_VISIBLE,
		uintptr(width-100), 0, 100, 25,
		hwnd, 1001, instance, 0,
	)

	// Поле статуса (Heartbeat)
	statusClassName, _ := syscall.UTF16PtrFromString("STATIC")
	hwndStatus, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(statusClassName)),
		0,
		WS_CHILD|WS_VISIBLE,
		0, uintptr(height+25), uintptr(width), 25,
		hwnd, 0, instance, 0,
	)
	viewer.hwndStatus = syscall.Handle(hwndStatus)

	procShowWindow.Call(uintptr(viewer.hwnd), 1) // SW_SHOWNORMAL
	procUpdateWindow.Call(uintptr(viewer.hwnd))

	go runStatusUpdateLoop()
	go runMJPEGClient()

	var msg MSG
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func runMJPEGClient() {
	log.Printf("Viewer: MJPEG client goroutine started")
	client := &http.Client{} // Нет таймаута для бесконечного потока
	for {
		viewer.mu.RLock()
		url := viewer.url
		viewer.mu.RUnlock()

		if url == "" {
			time.Sleep(time.Second)
			continue
		}

		log.Printf("Viewer: Attempting to connect to %s", url)
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("Viewer: Connect error to %s: %v", url, err)
			time.Sleep(2 * time.Second)
			continue
		}
		log.Printf("Viewer: Connected to %s", url)

		viewer.mu.Lock()
		viewer.currentBody = resp.Body
		viewer.mu.Unlock()

		_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil {
			resp.Body.Close()
			time.Sleep(time.Second)
			continue
		}
		boundary := params["boundary"]
		if boundary == "" {
			resp.Body.Close()
			time.Sleep(time.Second)
			continue
		}

		mr := multipart.NewReader(resp.Body, boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				log.Printf("Viewer: NextPart error: %v", err)
				break
			}
			img, err := jpeg.Decode(part)
			if err != nil {
				log.Printf("Viewer: JPEG decode error: %v", err)
				continue
			}

			viewer.mu.Lock()
			viewer.frameCount++
			if viewer.frameCount%100 == 0 {
				log.Printf("Viewer: Processed %d frames from MJPEG stream", viewer.frameCount)
			}
			viewer.mu.Unlock()

			rgba, ok := img.(*image.RGBA)
			if !ok {
				b := img.Bounds()
				rgba = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
				for y := b.Min.Y; y < b.Max.Y; y++ {
					for x := b.Min.X; x < b.Max.X; x++ {
						rgba.Set(x, y, img.At(x, y))
					}
				}
			}

			// Подготавливаем буфер для GDI (BGR)
			w, h := int32(rgba.Bounds().Dx()), int32(rgba.Bounds().Dy())
			pix := make([]byte, len(rgba.Pix))
			copy(pix, rgba.Pix)
			for i := 0; i < len(pix); i += 4 {
				pix[i], pix[i+2] = pix[i+2], pix[i]
			}

			viewer.mu.Lock()
			viewer.gdiBuf = pix
			viewer.gdiW = w
			viewer.gdiH = h
			viewer.mu.Unlock()

			procInvalidateRect.Call(uintptr(viewer.hwnd), 0, 0)
		}
		resp.Body.Close()
		fmt.Printf("Viewer: Connection to %s closed\n", url)
		viewer.mu.Lock()
		viewer.currentBody = nil
		viewer.mu.Unlock()
	}
}
