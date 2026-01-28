package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	typeConnect   = 0x00
	typeConnAck   = 0x01
	typeData      = 0x02
	typeHeartbeat = 0x03
)

type HeartbeatData struct {
	FPS          float32 `json:"fps"`
	ProcessingMS int     `json:"ms"`
	Timestamp    int64   `json:"ts"`
	TargetFPS    int     `json:"target_fps"`   // Скорость, с которой я отправляю
	ReceivedFPS  int     `json:"received_fps"` // Скорость, которую я успешно принимаю от тебя
	Ready        bool    `json:"ready"`        // Готовность к передаче данных
	SessionID    int64   `json:"sid"`          // Идентификатор сессии
}

var fpsLevels = []int{1, 5, 10, 20, 25}

var (
	perfMu         sync.Mutex
	perfFrames     int
	perfLastCheck  time.Time
	perfProcessSum time.Duration
	lastFPS        float32
	lastAvgMs      int

	recvMu        sync.Mutex
	recvFrames    int
	recvLastCheck time.Time
	lastRecvFPS   int
)

func recordRecvFrame() {
	recvMu.Lock()
	defer recvMu.Unlock()
	recvFrames++
	if recvLastCheck.IsZero() {
		recvLastCheck = time.Now()
	}
}

func getRecvFPS() int {
	recvMu.Lock()
	defer recvMu.Unlock()
	now := time.Now()
	dur := now.Sub(recvLastCheck).Seconds()
	if dur >= 1.0 {
		lastRecvFPS = int(float32(recvFrames) / float32(dur))
		recvFrames = 0
		recvLastCheck = now
	}
	return lastRecvFPS
}

func recordFrameProcess(dur time.Duration) {
	perfMu.Lock()
	defer perfMu.Unlock()
	perfFrames++
	perfProcessSum += dur
	if perfLastCheck.IsZero() {
		perfLastCheck = time.Now()
	}
}

func getPerfMetrics() (fps float32, avgMs int) {
	perfMu.Lock()
	defer perfMu.Unlock()
	now := time.Now()
	dur := now.Sub(perfLastCheck).Seconds()
	if dur >= 1.0 {
		lastFPS = float32(perfFrames) / float32(dur)
		if perfFrames > 0 {
			lastAvgMs = int(perfProcessSum.Milliseconds()) / perfFrames
		} else {
			lastAvgMs = 0
		}
		// Сброс для следующего периода
		perfFrames = 0
		perfProcessSum = 0
		perfLastCheck = now
	}
	return lastFPS, lastAvgMs
}

var (
	activeVideoConn *ScreenVideoConn
	activeVideoMu   sync.RWMutex
)

// ScreenVideoConn реализует io.ReadWriter для работы через захват экрана и VCam
type ScreenVideoConn struct {
	HWND      syscall.Handle
	X, Y      int
	Margin    int
	ReadDelay time.Duration
	SessionID int64
}

func (s *ScreenVideoConn) Read(p []byte) (n int, err error) {
	// Ожидаем, что p имеет размер captureWidth * captureHeight * 4
	if len(p) < captureWidth*captureHeight*4 {
		return 0, io.ErrShortBuffer
	}

	// Ограничиваем частоту захвата, чтобы не перегружать CPU
	delay := s.ReadDelay
	if delay == 0 {
		delay = 500 * time.Millisecond // 2 FPS по умолчанию
	}
	time.Sleep(delay)

	activeVideoMu.RLock()
	curX, curY, hwnd := s.X, s.Y, s.HWND
	activeVideoMu.RUnlock()

	// Захватываем чуть больше, чтобы компенсировать смещения рамок и DPI
	img, err := CaptureScreenEx(hwnd, curX, curY, captureWidth, captureHeight)
	if err != nil {
		log.Printf("ScreenVideoConn: CaptureScreen error: %v", err)
		return 0, err
	}

	copy(p, img.Pix)
	return captureWidth * captureHeight * 4, nil
}

func (s *ScreenVideoConn) Write(p []byte) (n int, err error) {
	if len(p) < width*height*4 {
		return 0, io.ErrShortWrite
	}

	img := &image.RGBA{
		Pix:    p,
		Stride: width * 4,
		Rect:   image.Rect(0, 0, width, height),
	}
	writeToVCam(img)
	return len(p), nil
}

func (s *ScreenVideoConn) Close() error {
	return nil
}

func writeToVCam(img *image.RGBA) {
	if vcam != nil {
		vcam.WriteFrame(img)
	}
}

// runTunnelWithPrefix читает данные из dataConn, упаковывает их в видеокадры с префиксом типа и пишет в videoConn (VCam).
// Также читает видеокадры из videoConn (Screen), распаковывает и пишет в dataConn.
func runTunnelWithPrefix(dataConn io.ReadWriteCloser, videoConn io.ReadWriteCloser, margin int, connID uint16) {
	done := make(chan bool, 2)
	start := time.Now()
	var bytesSent, bytesReceived int64
	var lastSentSeq, lastRevSeq byte

	var mySID int64
	if svc, ok := videoConn.(*ScreenVideoConn); ok {
		mySID = svc.SessionID
	}

	// Настройка скорости
	fpsIdx := 0
	lastFPSIncrease := time.Now()
	lastHeartbeat := time.Now()

	type speedState struct {
		mu                sync.Mutex
		lastRemoteFPS     int
		remoteReceivedFPS int
	}
	ss := &speedState{}

	// Принудительно ставим 2 FPS в начале (500ms)
	if svc, ok := videoConn.(*ScreenVideoConn); ok {
		svc.ReadDelay = 500 * time.Millisecond
	}

	go func() {
		defer func() { done <- true }()
		for {
			fps := fpsLevels[fpsIdx]
			sendInterval := time.Second / time.Duration(fps)
			loopStart := time.Now()

			// 1. Проверяем нужно ли отправить Heartbeat для согласования скорости
			ss.mu.Lock()
			remRecv := ss.remoteReceivedFPS
			ss.mu.Unlock()

			if time.Since(lastHeartbeat) > 2*time.Second || remRecv < fps {
				fpsMetrics, ms := getPerfMetrics()
				hb := HeartbeatData{
					FPS:          fpsMetrics,
					ProcessingMS: ms,
					Timestamp:    time.Now().Unix(),
					TargetFPS:    fps,
					ReceivedFPS:  getRecvFPS(),
					Ready:        true,
					SessionID:    mySID,
				}
				hbBytes, _ := json.Marshal(hb)
				payload := append([]byte{typeHeartbeat}, hbBytes...)
				writeToVCam(Encode(payload, margin))
				lastHeartbeat = time.Now()

				// Если удаленная сторона подтвердила наш уровень, пробуем расти
				if remRecv >= fps && fpsIdx < len(fpsLevels)-1 && time.Since(lastFPSIncrease) > 3*time.Second {
					fpsIdx++
					lastFPSIncrease = time.Now()
					log.Printf("Tunnel: Ramping up to %d FPS (remote confirmed %d FPS)", fpsLevels[fpsIdx], fps)
					// Увеличиваем частоту чтения тоже
					if svc, ok := videoConn.(*ScreenVideoConn); ok {
						svc.ReadDelay = time.Second / time.Duration(fpsLevels[fpsIdx]*2)
						if svc.ReadDelay < 30*time.Millisecond {
							svc.ReadDelay = 30 * time.Millisecond
						}
					}
				}
			}

			// 2. Читаем данные из сокета
			maxData := 490
			buf := make([]byte, maxData)
			if tc, ok := dataConn.(interface{ SetReadDeadline(time.Time) error }); ok {
				tc.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			}
			n, err := dataConn.Read(buf)

			if err == nil && n > 0 {
				lastSentSeq++
				payload := make([]byte, 4+n)
				payload[0] = typeData
				payload[1] = byte(connID >> 8)
				payload[2] = byte(connID)
				payload[3] = lastSentSeq
				copy(payload[4:], buf[:n])
				img := Encode(payload, margin)
				writeToVCam(img)
				if _, err := videoConn.Write(img.Pix); err != nil {
					return
				}
				bytesSent += int64(n)
				if bytesSent%1000 < int64(n) || bytesSent < 1000 {
					log.Printf("Tunnel: Sent %d bytes, current FPS: %d", bytesSent, fps)
				}
			} else if err != nil {
				if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
					return
				}
			}

			// 3. Соблюдаем FPS
			elapsed := time.Since(loopStart)
			if elapsed < sendInterval {
				time.Sleep(sendInterval - elapsed)
			}
		}
	}()

	go func() {
		defer func() { done <- true }()
		var remoteSID int64
		for {
			frameSize := captureWidth * captureHeight * 4
			buf := make([]byte, frameSize)
			frameStart := time.Now()
			if _, err := io.ReadFull(videoConn, buf); err != nil {
				return
			}

			img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}
			data := Decode(img, margin)
			recordFrameProcess(time.Since(frameStart))

			if data != nil && len(data) >= 1 {
				recordRecvFrame()
				UpdateCaptureStatus(true)
				switch data[0] {
				case typeData:
					if len(data) < 4 {
						continue
					}
					id := uint16(data[1])<<8 | uint16(data[2])
					if id != connID {
						continue
					}
					seq := data[3]
					if seq != lastRevSeq {
						n, err := dataConn.Write(data[4:])
						if err != nil {
							return
						}
						bytesReceived += int64(n)
						lastRevSeq = seq
						if bytesReceived%1000 < int64(n) || bytesReceived < 1000 {
							log.Printf("Tunnel: Received %d bytes", bytesReceived)
						}
					}
				case typeHeartbeat:
					var hb HeartbeatData
					if err := json.Unmarshal(data[1:], &hb); err == nil {
						if remoteSID != 0 && hb.SessionID != remoteSID {
							log.Printf("Tunnel: Remote session ID changed (%d -> %d), closing tunnel", remoteSID, hb.SessionID)
							return
						}
						remoteSID = hb.SessionID

						ss.mu.Lock()
						if hb.TargetFPS > 0 {
							if hb.TargetFPS != ss.lastRemoteFPS {
								log.Printf("Tunnel: Remote is sending at %d FPS", hb.TargetFPS)
							}
							ss.lastRemoteFPS = hb.TargetFPS
						}
						if hb.ReceivedFPS > 0 {
							if hb.ReceivedFPS != ss.remoteReceivedFPS {
								log.Printf("Tunnel: Remote confirmed they receive %d FPS", hb.ReceivedFPS)
							}
							ss.remoteReceivedFPS = hb.ReceivedFPS
						}
						ss.mu.Unlock()
					}
				}
			}
		}
	}()

	<-done
	duration := time.Since(start)
	log.Printf("Tunnel: Closed. Duration: %v, Sent: %d bytes, Received: %d bytes", duration, bytesSent, bytesReceived)
	// Очищаем VCam, чтобы не висел старый кадр
	for i := 0; i < 3; i++ {
		writeToVCam(Encode(nil, margin))
		time.Sleep(50 * time.Millisecond)
	}
}

func UpdateActiveCaptureArea(hwnd syscall.Handle, x, y int) {
	activeVideoMu.Lock()
	defer activeVideoMu.Unlock()
	if activeVideoConn != nil {
		activeVideoConn.HWND = hwnd
		activeVideoConn.X = x
		activeVideoConn.Y = y
	}
}

// RunScreenSocksServer работает через захват экрана и VCam с динамическим выбором цели
func RunScreenSocksServer(x, y, margin int) {
	log.Printf("Server: Watching screen at (%d, %d) with margin %d", x, y, margin)
	video := &ScreenVideoConn{X: x, Y: y, Margin: margin, ReadDelay: 500 * time.Millisecond, SessionID: rand.Int63()}

	activeVideoMu.Lock()
	activeVideoConn = video
	activeVideoMu.Unlock()

	go func() {
		lastHb := time.Now()
		for {
			if time.Since(lastHb) > 5*time.Second {
				fps, ms := getPerfMetrics()
				hb := HeartbeatData{
					FPS:          fps,
					ProcessingMS: ms,
					Timestamp:    time.Now().Unix(),
					TargetFPS:    int(1000 / video.ReadDelay.Milliseconds()),
					ReceivedFPS:  getRecvFPS(),
					Ready:        true,
					SessionID:    video.SessionID,
				}
				hbBytes, _ := json.Marshal(hb)
				writeToVCam(Encode(append([]byte{typeHeartbeat}, hbBytes...), margin))
				lastHb = time.Now()
			}
			time.Sleep(1 * time.Second)
		}
	}()

	frameCount := 0
	var lastData []byte
	for {
		loopStart := time.Now()
		frameSize := captureWidth * captureHeight * 4
		buf := make([]byte, frameSize)
		_, err := io.ReadFull(video, buf)
		if err != nil {
			log.Printf("Server: screen read error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		recordFrameProcess(time.Since(loopStart))
		img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}

		if frameCount == 10 { // Сохраняем 10-й кадр для проверки
			f, err := os.Create("debug_server_capture.png")
			if err == nil {
				png.Encode(f, img)
				f.Close()
				log.Printf("Server: Saved debug_server_capture.png")
			}
		}

		frameCount++
		if frameCount%100 == 0 {
			log.Printf("Server: Heartbeat - Processed %d frames from screen...", frameCount)
		}

		data := Decode(img, margin)
		if data != nil {
			recordRecvFrame()
			UpdateCaptureStatus(true)
			if !bytes.Equal(data, lastData) {
				if len(data) >= 1 && data[0] == typeHeartbeat {
					var hb HeartbeatData
					if err := json.Unmarshal(data[1:], &hb); err == nil {
						log.Printf("Server: Remote quality: SID=%d, FPS=%.1f, ProcMS=%d", hb.SessionID, hb.FPS, hb.ProcessingMS)
					}
					lastData = data
					continue
				}

				log.Printf("Server: Decoded %d bytes from screen", len(data))
				lastData = data

				if len(data) > 0 && data[0] == typeConnect {
					if len(data) < 3 {
						continue
					}
					connID := uint16(data[1])<<8 | uint16(data[2])
					targetAddr := string(data[3:])
					targetAddr = strings.TrimRight(targetAddr, "\x00")
					log.Printf("Server: Request to %s (ID: %d, raw len: %d)", targetAddr, connID, len(data)-3)

					targetConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
					status := byte(0)
					if err != nil {
						log.Printf("Server: dial failed to %s: %v", targetAddr, err)
						status = 1
					}

					// Отправляем ACK (несколько раз для надежности при низкой частоте кадров)
					ackPayload := []byte{typeConnAck, byte(connID >> 8), byte(connID), status}
					ackImg := Encode(ackPayload, margin)
					for j := 0; j < 30; j++ {
						writeToVCam(ackImg)
						time.Sleep(100 * time.Millisecond)
					}

					if status == 0 {
						log.Printf("Server: Tunnel established for %s (ID: %d)", targetAddr, connID)
						runTunnelWithPrefix(targetConn, video, margin, connID)
						targetConn.Close()
						log.Println("Server: Connection closed, waiting for next...")
						lastData = nil // Сбрасываем после туннеля
					}
				}
			}
		}
	}
}

// RunScreenSocksClient работает через захват экрана и VCam
func RunScreenSocksClient(localListenAddr string, x, y, margin int) {
	video := &ScreenVideoConn{X: x, Y: y, Margin: margin, ReadDelay: 500 * time.Millisecond, SessionID: rand.Int63()}

	activeVideoMu.Lock()
	activeVideoConn = video
	activeVideoMu.Unlock()

	for {
		log.Printf("Client: Starting handshake/calibration phase...")
		bestFPS := 1

		// 1. Ждем хотя бы минимальной стабильности на 1 FPS
		for {
			log.Printf("Client: Testing base FPS level: 1...")
			video.ReadDelay = 1000 * time.Millisecond
			successCount := 0
			for i := 0; i < 5; i++ {
				loopStart := time.Now()
				fpsMetrics, ms := getPerfMetrics()
				hb := HeartbeatData{
					FPS:          fpsMetrics,
					ProcessingMS: ms,
					Timestamp:    time.Now().Unix(),
					TargetFPS:    1,
					ReceivedFPS:  getRecvFPS(),
					Ready:        true,
					SessionID:    video.SessionID,
				}
				hbBytes, _ := json.Marshal(hb)
				writeToVCam(Encode(append([]byte{typeHeartbeat}, hbBytes...), margin))

				buf := make([]byte, captureWidth*captureHeight*4)
				if _, err := io.ReadFull(video, buf); err == nil {
					recordFrameProcess(time.Since(loopStart))
					img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}
					if data := Decode(img, margin); data != nil && len(data) > 0 {
						recordRecvFrame()
						if data[0] == typeHeartbeat {
							var remoteHb HeartbeatData
							if err := json.Unmarshal(data[1:], &remoteHb); err == nil && remoteHb.Ready {
								successCount++
							}
						}
					}
				}
			}
			if successCount >= 2 {
				log.Printf("Client: Connection established at 1 FPS")
				break
			}
			log.Printf("Client: Server not responding or connection unstable, retrying...")
			time.Sleep(1 * time.Second)
		}

		// 2. Пробуем повысить FPS
		for _, testFPS := range fpsLevels {
			if testFPS <= 1 {
				continue
			}
			log.Printf("Client: Testing FPS level: %d...", testFPS)
			video.ReadDelay = time.Duration(1000/testFPS) * time.Millisecond

			successCount := 0
			for i := 0; i < 10; i++ {
				loopStart := time.Now()
				fpsMetrics, ms := getPerfMetrics()
				hb := HeartbeatData{
					FPS:          fpsMetrics,
					ProcessingMS: ms,
					Timestamp:    time.Now().Unix(),
					TargetFPS:    testFPS,
					ReceivedFPS:  getRecvFPS(),
					Ready:        true,
					SessionID:    video.SessionID,
				}
				hbBytes, _ := json.Marshal(hb)
				writeToVCam(Encode(append([]byte{typeHeartbeat}, hbBytes...), margin))

				buf := make([]byte, captureWidth*captureHeight*4)
				if _, err := io.ReadFull(video, buf); err == nil {
					recordFrameProcess(time.Since(loopStart))
					img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}
					if data := Decode(img, margin); data != nil && len(data) > 0 {
						recordRecvFrame()
						if data[0] == typeHeartbeat {
							var remoteHb HeartbeatData
							if err := json.Unmarshal(data[1:], &remoteHb); err == nil && remoteHb.Ready {
								if remoteHb.ReceivedFPS >= testFPS-2 && remoteHb.ReceivedFPS > 0 {
									successCount++
								}
							}
						}
					}
				}
			}

			if successCount >= 4 {
				log.Printf("Client: FPS level %d is stable (%d/10)", testFPS, successCount)
				bestFPS = testFPS
			} else {
				log.Printf("Client: FPS level %d is NOT stable (%d/10), stopping calibration", testFPS, successCount)
				break
			}
		}

		video.ReadDelay = time.Duration(1000/bestFPS) * time.Millisecond
		log.Printf("Client: Calibration finished. Optimal FPS: %d", bestFPS)

		ln, err := net.Listen("tcp", localListenAddr)
		if err != nil {
			log.Printf("Client: failed to listen on %s: %v, retrying in 5s...", localListenAddr, err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("Client: Listening (SOCKS5) on %s, watching screen at (%d, %d) with margin %d", localListenAddr, x, y, margin)

		stopSession := make(chan struct{})

		// Фоновые хартбиты
		go func() {
			for {
				select {
				case <-stopSession:
					return
				case <-time.After(5 * time.Second):
					fps, ms := getPerfMetrics()
					hb := HeartbeatData{
						FPS:          fps,
						ProcessingMS: ms,
						Timestamp:    time.Now().Unix(),
						TargetFPS:    bestFPS,
						ReceivedFPS:  getRecvFPS(),
						Ready:        true,
						SessionID:    video.SessionID,
					}
					hbBytes, _ := json.Marshal(hb)
					writeToVCam(Encode(append([]byte{typeHeartbeat}, hbBytes...), margin))
				}
			}
		}()

		// Мониторинг SID сервера
		go func() {
			var lastRemoteSID int64
			for {
				select {
				case <-stopSession:
					return
				case <-time.After(2 * time.Second):
					buf := make([]byte, captureWidth*captureHeight*4)
					if _, err := io.ReadFull(video, buf); err == nil {
						img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}
						if data := Decode(img, margin); data != nil && len(data) > 0 {
							if data[0] == typeHeartbeat {
								var remoteHb HeartbeatData
								if err := json.Unmarshal(data[1:], &remoteHb); err == nil {
									if lastRemoteSID != 0 && remoteHb.SessionID != lastRemoteSID {
										log.Printf("Client: Server session changed (%d -> %d), re-calibrating...", lastRemoteSID, remoteHb.SessionID)
										ln.Close()
										return
									}
									lastRemoteSID = remoteHb.SessionID
								}
							}
						}
					}
				}
			}
		}()

		// Accept loop
		shouldRestart := false
		for {
			localConn, err := ln.Accept()
			if err != nil {
				log.Printf("Client: Accept error (closing listener): %v", err)
				shouldRestart = true
				break
			}
			log.Printf("Client: Accepted local connection from %s", localConn.RemoteAddr())

			targetAddr, err := HandleSocksHandshake(localConn)
			if err != nil {
				log.Printf("Client: handshake error: %v", err)
				localConn.Close()
				continue
			}

			// 1. Отправляем CONNECT
			connID := uint16(rand.Intn(65535) + 1)
			log.Printf("Client: Sending CONNECT to %s (ID: %d)", targetAddr, connID)
			payload := make([]byte, 3+len(targetAddr))
			payload[0] = typeConnect
			payload[1] = byte(connID >> 8)
			payload[2] = byte(connID)
			copy(payload[3:], targetAddr)
			connectImg := Encode(payload, margin)
			for j := 0; j < 30; j++ {
				writeToVCam(connectImg)
				time.Sleep(100 * time.Millisecond)
			}

			// 2. Ждем ACK
			log.Printf("Client: Waiting for ACK for %s (ID: %d)", targetAddr, connID)
			success := false
			for i := 0; i < 40; i++ {
				loopStart := time.Now()
				frameSize := captureWidth * captureHeight * 4
				buf := make([]byte, frameSize)
				if _, err := io.ReadFull(video, buf); err != nil {
					break
				}
				recordFrameProcess(time.Since(loopStart))
				img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}
				ackData := Decode(img, margin)
				if ackData != nil && len(ackData) > 0 {
					recordRecvFrame()
					if ackData[0] == typeConnAck && len(ackData) >= 4 {
						id := uint16(ackData[1])<<8 | uint16(ackData[2])
						if id == connID {
							UpdateCaptureStatus(true)
							if ackData[3] == 0 {
								success = true
							}
							break
						}
					}
				}
			}

			if !success {
				log.Printf("Client: Failed to establish tunnel to %s (ID: %d)", targetAddr, connID)
				_ = SendSocksResponse(localConn, fmt.Errorf("failed"))
				localConn.Close()
				continue
			}

			_ = SendSocksResponse(localConn, nil)
			log.Printf("Client: Tunnel established to %s (ID: %d)", targetAddr, connID)
			time.Sleep(2 * time.Second)
			runTunnelWithPrefix(localConn, video, margin, connID)
			localConn.Close()
			log.Println("Client: Tunnel finished")
		}

		close(stopSession)
		if !shouldRestart {
			// If we broke out of loop not because of error, maybe we should stop?
			// But for now always restart.
		}
		log.Printf("Client: Restarting calibration...")
		time.Sleep(1 * time.Second)
	}
}
