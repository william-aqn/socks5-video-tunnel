//go:build windows
// +build windows

package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
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
	currentImg  *image.RGBA
	mu          sync.RWMutex
	hwnd        syscall.Handle
	hwndEdit    syscall.Handle
	currentBody io.Closer
}

var viewer ViewerState

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
			img := viewer.currentImg
			var displayPix []byte
			var w, h int32
			if img != nil {
				w, h = int32(img.Bounds().Dx()), int32(img.Bounds().Dy())
				displayPix = make([]byte, len(img.Pix))
				copy(displayPix, img.Pix)
			}
			viewer.mu.RUnlock()

			if displayPix != nil {
				// Нам нужно подготовить данные для SetDIBitsToDevice.
				for i := 0; i < len(displayPix); i += 4 {
					displayPix[i], displayPix[i+2] = displayPix[i+2], displayPix[i]
				}

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
					uintptr(unsafe.Pointer(&displayPix[0])),
					uintptr(unsafe.Pointer(&bi)),
					DIB_RGB_COLORS,
				)
			}
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
	viewer.url = initialURL
	viewer.onURLChange = onURLChange

	className, _ := syscall.UTF16PtrFromString("VideoGoViewerClass")
	title := fmt.Sprintf("VideoGo Debug Viewer [%s]", mode)
	if localURL != "" {
		title = fmt.Sprintf("VideoGo Debug Viewer [%s] (My MJPEG: %s)", mode, localURL)
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

	hwnd, _, _ := procCreateWindowExW.Call(
		WS_EX_TOPMOST,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		WS_OVERLAPPEDWINDOW|WS_VISIBLE,
		uintptr(x), uintptr(y), uintptr(width+16), uintptr(height+60), // Чуть больше для UI элементов
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
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	for {
		viewer.mu.RLock()
		url := viewer.url
		viewer.mu.RUnlock()

		if url == "" {
			time.Sleep(time.Second)
			continue
		}

		resp, err := client.Get(url)
		if err != nil {
			fmt.Printf("Viewer: Connect error to %s: %v\n", url, err)
			time.Sleep(2 * time.Second)
			continue
		}
		fmt.Printf("Viewer: Connected to %s\n", url)

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
				break
			}
			img, err := jpeg.Decode(part)
			if err != nil {
				continue
			}

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

			viewer.mu.Lock()
			viewer.currentImg = rgba
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
