package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"syscall"
	"time"
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
)

type Config struct {
	CaptureX  int    `json:"capture_x"`
	CaptureY  int    `json:"capture_y"`
	Margin    int    `json:"margin"`
	UseMJPEG  bool   `json:"use_mjpeg"`
	UseNative bool   `json:"use_native"`
	VCamName  string `json:"vcam_name"`
	DebugURL  string `json:"debug_url"`
	VCamPort  int    `json:"vcam_port"`
	DebugX    int    `json:"debug_x"`
	DebugY    int    `json:"debug_y"`
}

func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	cfg := Config{
		UseMJPEG:  true,
		UseNative: true,
		DebugURL:  "http://127.0.0.1:8080", // Default guess
		VCamPort:  0,
		DebugX:    200,
		DebugY:    200,
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(filename string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

var (
	vcam       VirtualCamera
	currentCfg *Config
	cfgFile    string
)

func main() {
	procSetProcessDPIAware.Call()
	mode := flag.String("mode", "", "Mode: server or client")
	localAddr := flag.String("local", ":1080", "Local SOCKS5 listen address (for client mode)")
	captureX := flag.Int("capture-x", -1, "X coordinate for screen capture")
	captureY := flag.Int("capture-y", -1, "Y coordinate for screen capture")
	margin := flag.Int("margin", -1, "Margin from edges for video generation/decoding")
	useUI := flag.Bool("ui", false, "Use UI to select capture area")
	useMJPEG := flag.Bool("vcam-mjpeg", true, "Enable MJPEG server")
	useNative := flag.Bool("vcam-native", true, "Enable native Virtual Camera registration (Windows only)")
	vcamName := flag.String("vcam-name", "", "Name of the virtual camera")
	vcamPort := flag.Int("vcam-port", -1, "MJPEG server port (0 for random)")
	debugUI := flag.Bool("debug-ui", false, "Open debug UI to view video stream")
	debugURL := flag.String("debug-url", "", "MJPEG URL to view in debug UI")
	debugX := flag.Int("debug-x", -1, "X position for debug UI window")
	debugY := flag.Int("debug-y", -1, "Y position for debug UI window")

	flag.Parse()

	if *mode == "" {
		fmt.Println("Please specify mode: -mode=server or -mode=client")
		os.Exit(1)
	}

	// Настройка логирования в файл
	logFile, err := os.OpenFile(fmt.Sprintf("%s_vgo.log", *mode), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
		log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
		log.Printf("--- Starting %s session ---", *mode)
	}

	cfgFile = fmt.Sprintf("config_%s.json", *mode)
	loadedCfg, _ := loadConfig(cfgFile)

	finalX, finalY := *captureX, *captureY
	finalMargin := *margin
	finalUseMJPEG := *useMJPEG
	finalUseNative := *useNative
	finalVCamName := *vcamName
	finalDebugURL := *debugURL
	finalVCamPort := *vcamPort
	finalDebugX := *debugX
	finalDebugY := *debugY

	isMJPEGSet := false
	isNativeSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "vcam-mjpeg" {
			isMJPEGSet = true
		}
		if f.Name == "vcam-native" {
			isNativeSet = true
		}
	})

	// Если в флагах пусто, пробуем из конфига
	if finalX == -1 && finalY == -1 && loadedCfg != nil {
		finalX = loadedCfg.CaptureX
		finalY = loadedCfg.CaptureY
		fmt.Printf("Loaded coordinates from %s: (%d, %d)\n", cfgFile, finalX, finalY)
	}
	if finalMargin == -1 {
		if loadedCfg != nil {
			finalMargin = loadedCfg.Margin
			fmt.Printf("Loaded margin from %s: %d\n", cfgFile, finalMargin)
		} else {
			finalMargin = 10
		}
	}
	if !isMJPEGSet && loadedCfg != nil {
		finalUseMJPEG = loadedCfg.UseMJPEG
		fmt.Printf("Loaded MJPEG setting from %s: %v\n", cfgFile, finalUseMJPEG)
	}
	if !isNativeSet && loadedCfg != nil {
		finalUseNative = loadedCfg.UseNative
		fmt.Printf("Loaded Native VCam setting from %s: %v\n", cfgFile, finalUseNative)
	}
	if finalVCamName == "" && loadedCfg != nil {
		finalVCamName = loadedCfg.VCamName
		if finalVCamName != "" {
			fmt.Printf("Loaded VCam Name from %s: %s\n", cfgFile, finalVCamName)
		}
	}
	if finalVCamName == "" {
		if *mode == "server" {
			finalVCamName = "VideoGo Server Camera"
		} else {
			finalVCamName = "VideoGo Client Camera"
		}
	}
	if finalDebugURL == "" && loadedCfg != nil {
		finalDebugURL = loadedCfg.DebugURL
		if finalDebugURL != "" {
			fmt.Printf("Loaded Debug URL from %s: %s\n", cfgFile, finalDebugURL)
		}
	}
	if finalVCamPort == -1 {
		if loadedCfg != nil {
			finalVCamPort = loadedCfg.VCamPort
		} else {
			finalVCamPort = 0
		}
	}
	if finalDebugX == -1 {
		if loadedCfg != nil {
			finalDebugX = loadedCfg.DebugX
		} else {
			finalDebugX = 200
		}
	}
	if finalDebugY == -1 {
		if loadedCfg != nil {
			finalDebugY = loadedCfg.DebugY
		} else {
			finalDebugY = 200
		}
	}

	CurrentMode = *mode

	if *useUI {
		fmt.Println("Please select capture area using the window...")
		x, y, err := SelectCaptureArea()
		if err != nil {
			fmt.Printf("UI Selection failed: %v\n", err)
			if finalX == -1 || finalY == -1 {
				fmt.Println("No coordinates provided, using (0,0) as default.")
				finalX, finalY = 0, 0
			} else {
				fmt.Println("Using existing coordinates.")
			}
		} else {
			finalX, finalY = x, y
			fmt.Printf("Selected area: (%d, %d)\n", finalX, finalY)
		}
	} else if finalX == -1 && finalY == -1 {
		// Default to 0,0 if nothing specified and UI not requested
		finalX, finalY = 0, 0
		fmt.Println("Using default coordinates (0, 0). Use Hotkey or -ui to change.")
	}

	currentCfg = &Config{
		CaptureX:  finalX,
		CaptureY:  finalY,
		Margin:    finalMargin,
		UseMJPEG:  finalUseMJPEG,
		UseNative: finalUseNative,
		VCamName:  finalVCamName,
		DebugURL:  finalDebugURL,
		VCamPort:  finalVCamPort,
		DebugX:    finalDebugX,
		DebugY:    finalDebugY,
	}

	// Сохраняем конфиг, если он изменился или не существовал
	if loadedCfg == nil || loadedCfg.CaptureX != finalX || loadedCfg.CaptureY != finalY ||
		loadedCfg.Margin != finalMargin || loadedCfg.UseMJPEG != finalUseMJPEG || loadedCfg.UseNative != finalUseNative ||
		loadedCfg.VCamName != finalVCamName || loadedCfg.DebugURL != finalDebugURL ||
		loadedCfg.VCamPort != finalVCamPort || loadedCfg.DebugX != finalDebugX || loadedCfg.DebugY != finalDebugY {
		err := saveConfig(cfgFile, currentCfg)
		if err != nil {
			fmt.Printf("Warning: failed to save config: %v\n", err)
		} else {
			fmt.Printf("Saved settings to %s\n", cfgFile)
		}
	}

	// Запускаем обработчик горячих клавиш
	StartHotkeyHandler(*mode, func(id int) {
		if id == HK_SELECT {
			fmt.Println("\nHotkey pressed! Changing capture area...")
			x, y, err := SelectCaptureArea()
			if err != nil {
				fmt.Printf("Selection failed: %v\n", err)
				return
			}
			fmt.Printf("New area selected: (%d, %d)\n", x, y)
			UpdateActiveCaptureArea(0, x, y)

			currentCfg.CaptureX = x
			currentCfg.CaptureY = y
			saveConfig(cfgFile, currentCfg)
			fmt.Printf("Coordinates updated and saved to %s\n", cfgFile)
		} else {
			// Тонкая настройка стрелками
			newX, newY := currentCfg.CaptureX, currentCfg.CaptureY
			switch id {
			case HK_LEFT:
				newX--
			case HK_RIGHT:
				newX++
			case HK_UP:
				newY--
			case HK_DOWN:
				newY++
			}

			if newX != currentCfg.CaptureX || newY != currentCfg.CaptureY {
				currentCfg.CaptureX = newX
				currentCfg.CaptureY = newY
				UpdateActiveCaptureArea(0, newX, newY)
				saveConfig(cfgFile, currentCfg)
			}
		}
	})

	// Инициализируем виртуальную камеру, она нужна в обоих режимах
	cam, err := NewVirtualCamera(width, height, finalUseMJPEG, finalUseNative, finalVCamName, finalVCamPort)
	if err != nil {
		fmt.Printf("Warning: Failed to initialize virtual camera system: %v\n", err)
	} else {
		fmt.Println("Virtual camera system initialized.")
		vcam = cam
		// Отправим пустой кадр для инициализации MJPEG сервера
		vcam.WriteFrame(Encode(nil, finalMargin))
		defer cam.Close()
	}

	// ShowCaptureOverlay(*mode, finalX, finalY)

	if *debugUI {
		localURL := ""
		if vcam != nil {
			localURL = vcam.GetURL()
		}
		go StartDebugUI(*mode, finalDebugURL, localURL, finalDebugX, finalDebugY, func(newURL string) {
			currentCfg.DebugURL = newURL
			saveConfig(cfgFile, currentCfg)
		})
	}

	// Запускаем фоновый трекинг по маркерам
	go func() {
		log.Printf("%s: Starting continuous tracking via control points...", *mode)
		for {
			activeVideoMu.RLock()
			conn := activeVideoConn
			activeVideoMu.RUnlock()

			if conn != nil {
				found := false
				// 1. Сначала пробуем найти маркеры в текущей области (с запасом 200px)
				searchMargin := 200
				localX := conn.X - searchMargin/2
				localY := conn.Y - searchMargin/2
				if localX < 0 {
					localX = 0
				}
				if localY < 0 {
					localY = 0
				}
				localW := width + searchMargin
				localH := height + searchMargin

				img, err := CaptureScreenEx(0, localX, localY, localW, localH)
				if err == nil {
					dx, dy, ok := FindMarkers(img, *mode)
					if ok {
						newX := localX + dx
						newY := localY + dy
						if newX != currentCfg.CaptureX || newY != currentCfg.CaptureY {
							log.Printf("%s: Markers tracked at (%d, %d)", *mode, newX, newY)
							currentCfg.CaptureX = newX
							currentCfg.CaptureY = newY
							UpdateActiveCaptureArea(0, newX, newY)
							saveConfig(cfgFile, currentCfg)
						}
						UpdateCaptureStatus(true)
						found = true
					}
				}

				// 2. Если в локальной области не нашли, сканируем весь экран
				if !found {
					sw, sh := GetScreenSize()
					// log.Printf("%s: Markers lost. Scanning whole screen %dx%d...", *mode, sw, sh)
					img, err := CaptureScreenEx(0, 0, 0, sw, sh)
					if err == nil {
						nx, ny, ok := FindMarkers(img, *mode)
						if ok {
							log.Printf("%s: Markers found on screen at (%d, %d)", *mode, nx, ny)
							currentCfg.CaptureX = nx
							currentCfg.CaptureY = ny
							UpdateActiveCaptureArea(0, nx, ny)
							saveConfig(cfgFile, currentCfg)
							UpdateCaptureStatus(true)
						}
					}
				}
			}
			time.Sleep(2 * time.Second)
		}
	}()

	switch *mode {
	case "server":
		fmt.Println("Starting Server mode (SOCKS5 via Screen/VCam)...")
		RunScreenSocksServer(finalX, finalY, finalMargin)
	case "client":
		fmt.Println("Starting Client mode (SOCKS5 via Screen/VCam)...")
		RunScreenSocksClient(*localAddr, finalX, finalY, finalMargin)
	default:
		fmt.Println("Please specify mode: -mode=server or -mode=client")
		os.Exit(1)
	}
}
