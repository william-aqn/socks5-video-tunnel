package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

type Config struct {
	CaptureX  int    `json:"capture_x"`
	CaptureY  int    `json:"capture_y"`
	Margin    int    `json:"margin"`
	UseMJPEG  bool   `json:"use_mjpeg"`
	UseNative bool   `json:"use_native"`
	VCamName  string `json:"vcam_name"`
}

func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	cfg := Config{
		UseMJPEG:  true,
		UseNative: true,
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
	mode := flag.String("mode", "", "Mode: server or client")
	localAddr := flag.String("local", ":1080", "Local SOCKS5 listen address (for client mode)")
	captureX := flag.Int("capture-x", -1, "X coordinate for screen capture")
	captureY := flag.Int("capture-y", -1, "Y coordinate for screen capture")
	margin := flag.Int("margin", -1, "Margin from edges for video generation/decoding")
	useUI := flag.Bool("ui", false, "Use UI to select capture area")
	useMJPEG := flag.Bool("vcam-mjpeg", true, "Enable MJPEG server")
	useNative := flag.Bool("vcam-native", true, "Enable native Virtual Camera registration (Windows only)")
	vcamName := flag.String("vcam-name", "", "Name of the virtual camera")

	flag.Parse()

	if *mode == "" {
		fmt.Println("Please specify mode: -mode=server or -mode=client")
		os.Exit(1)
	}

	cfgFile = fmt.Sprintf("config_%s.json", *mode)
	loadedCfg, _ := loadConfig(cfgFile)

	finalX, finalY := *captureX, *captureY
	finalMargin := *margin
	finalUseMJPEG := *useMJPEG
	finalUseNative := *useNative
	finalVCamName := *vcamName

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

	if *useUI || (finalX == -1 && finalY == -1) {
		fmt.Println("Please select capture area using the window...")
		x, y, err := SelectCaptureArea()
		if err != nil {
			fmt.Printf("UI Selection failed: %v\n", err)
			if finalX == -1 || finalY == -1 {
				fmt.Println("Please provide coordinates via flags: -capture-x and -capture-y")
				os.Exit(1)
			}
			fmt.Println("Using existing coordinates.")
		} else {
			finalX, finalY = x, y
			fmt.Printf("Selected area: (%d, %d)\n", finalX, finalY)
		}
	}

	currentCfg = &Config{
		CaptureX:  finalX,
		CaptureY:  finalY,
		Margin:    finalMargin,
		UseMJPEG:  finalUseMJPEG,
		UseNative: finalUseNative,
		VCamName:  finalVCamName,
	}

	// Сохраняем конфиг, если он изменился или не существовал
	if loadedCfg == nil || loadedCfg.CaptureX != finalX || loadedCfg.CaptureY != finalY ||
		loadedCfg.Margin != finalMargin || loadedCfg.UseMJPEG != finalUseMJPEG || loadedCfg.UseNative != finalUseNative ||
		loadedCfg.VCamName != finalVCamName {
		err := saveConfig(cfgFile, currentCfg)
		if err != nil {
			fmt.Printf("Warning: failed to save config: %v\n", err)
		} else {
			fmt.Printf("Saved settings to %s\n", cfgFile)
		}
	}

	// Запускаем обработчик горячих клавиш
	StartHotkeyHandler(*mode, func() {
		fmt.Println("\nHotkey pressed! Changing capture area...")
		x, y, err := SelectCaptureArea()
		if err != nil {
			fmt.Printf("Selection failed: %v\n", err)
			return
		}
		fmt.Printf("New area selected: (%d, %d)\n", x, y)
		UpdateActiveCaptureArea(x, y)

		currentCfg.CaptureX = x
		currentCfg.CaptureY = y
		saveConfig(cfgFile, currentCfg)
		fmt.Printf("Coordinates updated and saved to %s\n", cfgFile)
	})

	// Инициализируем виртуальную камеру, она нужна в обоих режимах
	cam, err := NewVirtualCamera(width, height, finalUseMJPEG, finalUseNative, finalVCamName)
	if err != nil {
		fmt.Printf("Warning: Failed to initialize virtual camera system: %v\n", err)
	} else {
		fmt.Println("Virtual camera system initialized.")
		vcam = cam
		defer cam.Close()
	}

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
