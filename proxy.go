package main

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"math/rand"
	"net"
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
)

type PacketDispatcher struct {
	mu           sync.RWMutex
	connChannels map[uint16]chan []byte
	heartbeatCh  chan []byte
	connectCh    chan []byte
}

func NewPacketDispatcher() *PacketDispatcher {
	return &PacketDispatcher{
		connChannels: make(map[uint16]chan []byte),
		heartbeatCh:  make(chan []byte, 32),
		connectCh:    make(chan []byte, 32),
	}
}

func (pd *PacketDispatcher) Register(id uint16) chan []byte {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	ch := make(chan []byte, 128)
	pd.connChannels[id] = ch
	return ch
}

func (pd *PacketDispatcher) Unregister(id uint16) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	if ch, ok := pd.connChannels[id]; ok {
		close(ch)
		delete(pd.connChannels, id)
	}
}

func (pd *PacketDispatcher) Dispatch(data []byte) {
	if len(data) == 0 {
		return
	}
	switch data[0] {
	case typeHeartbeat:
		select {
		case pd.heartbeatCh <- data:
		default:
		}
	case typeConnect:
		select {
		case pd.connectCh <- data:
		default:
		}
	case typeData, typeConnAck:
		if len(data) >= 3 {
			id := uint16(data[1])<<8 | uint16(data[2])
			pd.mu.RLock()
			ch, ok := pd.connChannels[id]
			pd.mu.RUnlock()
			if ok {
				select {
				case ch <- data:
				default:
				}
			} else {
				// Пакет для неизвестного или уже закрытого соединения
				if data[0] == typeData {
					log.Printf("Dispatcher: Data for unknown connID %d (len: %d)", id, len(data))
				}
			}
		}
	default:
		log.Printf("Dispatcher: Unknown packet type 0x%02x (len: %d)", data[0], len(data))
	}
}

func (pd *PacketDispatcher) Run(video *ScreenVideoConn, margin int) {
	for {
		frameSize := captureWidth * captureHeight * 4
		buf := make([]byte, frameSize)
		startTime := time.Now()
		_, err := io.ReadFull(video, buf)
		if err != nil {
			log.Printf("Dispatcher: screen read error: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		recordFrameProcess(time.Since(startTime))
		img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}
		data := Decode(img, margin)
		if data != nil && len(data) > 0 {
			recordRecvFrame()
			UpdateCaptureStatus(true)
			pd.Dispatch(data)
		} else {
			UpdateCaptureStatus(false)
		}
	}
}

var (
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
	// Обновляем если прошло больше секунды ИЛИ если накопилось хотя бы 5 кадров (ускоряет калибровку)
	if dur >= 1.0 || (dur > 0.1 && recvFrames >= 5) {
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
	lastRead  time.Time
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

	// Точное соблюдение интервала захвата
	if !s.lastRead.IsZero() {
		elapsed := time.Since(s.lastRead)
		if elapsed < delay {
			time.Sleep(delay - elapsed)
		}
	}

	activeVideoMu.RLock()
	curX, curY, hwnd := s.X, s.Y, s.HWND
	activeVideoMu.RUnlock()

	// Захватываем чуть больше, чтобы компенсировать смещения рамок и DPI
	img, err := CaptureScreenEx(hwnd, curX, curY, captureWidth, captureHeight)
	if err != nil {
		log.Printf("ScreenVideoConn: CaptureScreen error: %v", err)
		return 0, err
	}
	s.lastRead = time.Now()

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

// runTunnelWithPrefix читает данные из dataConn, упаковывает их в видеокадры с префиксом типа и пишет в VCam.
// Также получает пакеты из incoming канала и пишет в dataConn.
func runTunnelWithPrefix(dataConn io.ReadWriteCloser, video *ScreenVideoConn, margin int, connID uint16, initialFPS int, incoming chan []byte) {
	done := make(chan bool, 2)
	var bytesSent, bytesReceived int64
	var lastSentSeq, lastRevSeq byte
	lastSentSeq = 0
	lastRevSeq = 0
	var lastDataAt = time.Now()

	var mySID int64
	if video != nil {
		mySID = video.SessionID
	}

	// Настройка скорости
	fpsIdx := 0
	// Если передана начальная скорость, находим подходящий индекс
	if initialFPS > 0 {
		for i, f := range fpsLevels {
			if f <= initialFPS {
				fpsIdx = i
			} else {
				break
			}
		}
	} else {
		fpsIdx = 1
	}

	lastFPSIncrease := time.Now()
	lastHeartbeat := time.Now()

	type speedState struct {
		mu                sync.Mutex
		lastRemoteFPS     int
		remoteReceivedFPS int
	}
	ss := &speedState{}

	go func() {
		defer func() {
			log.Printf("Tunnel: Exit Data->Video goroutine (ID: %d)", connID)
			done <- true
		}()
		for {
			fps := fpsLevels[fpsIdx]
			sendInterval := time.Second / time.Duration(fps)
			loopStart := time.Now()

			ss.mu.Lock()
			remRecv := ss.remoteReceivedFPS
			ss.mu.Unlock()

			hbInterval := 30 * time.Second
			if currentCfg != nil && currentCfg.HeartbeatInterval > 0 {
				hbInterval = time.Duration(currentCfg.HeartbeatInterval) * time.Second
			}

			needHB := time.Since(lastHeartbeat) > hbInterval
			// Если удаленная сторона не успевает, посылаем Heartbeat чуть чаще для коррекции (но не чаще чем раз в 5 сек)
			if !needHB && (remRecv < fps && remRecv > 0) && time.Since(lastHeartbeat) > 5*time.Second {
				needHB = true
			}

			if needHB {
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

				if remRecv >= fps && fpsIdx < len(fpsLevels)-1 && time.Since(lastFPSIncrease) > 5*time.Second {
					fpsIdx++
					lastFPSIncrease = time.Now()
					log.Printf("Tunnel: Ramping up to %d FPS (remote confirmed %d FPS)", fpsLevels[fpsIdx], remRecv)
				} else if remRecv < fps-2 && remRecv > 0 && fpsIdx > 0 && time.Since(lastFPSIncrease) > 5*time.Second {
					fpsIdx--
					lastFPSIncrease = time.Now()
					log.Printf("Tunnel: Ramping down to %d FPS (remote confirmed only %d FPS)", fpsLevels[fpsIdx], remRecv)
				}
			}

			maxData := 490
			buf := make([]byte, maxData)
			if tc, ok := dataConn.(interface{ SetReadDeadline(time.Time) error }); ok {
				tc.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			}
			n, err := dataConn.Read(buf)

			if err == nil && n > 0 {
				lastDataAt = time.Now()
				lastSentSeq++
				if lastSentSeq == 0 {
					lastSentSeq = 1
				}
				payload := make([]byte, 4+n)
				payload[0] = typeData
				payload[1] = byte(connID >> 8)
				payload[2] = byte(connID)
				payload[3] = lastSentSeq
				copy(payload[4:], buf[:n])
				img := Encode(payload, margin)
				writeToVCam(img)
				bytesSent += int64(n)
				// log.Printf("Tunnel: Sent pkt seq=%d, len=%d (total sent: %d)", lastSentSeq, n, bytesSent)
				time.Sleep(sendInterval / 2)
			} else if err != nil {
				if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
					log.Printf("Tunnel: dataConn read error (ID: %d): %v", connID, err)
					return
				}
			}

			elapsed := time.Since(loopStart)
			if elapsed < sendInterval {
				time.Sleep(sendInterval - elapsed)
			}

			if time.Since(lastDataAt) > 30*time.Second {
				log.Printf("Tunnel: Inactive for 30s, closing (ID: %d)", connID)
				return
			}
		}
	}()

	go func() {
		defer func() {
			log.Printf("Tunnel: Exit Video->Data goroutine (ID: %d)", connID)
			done <- true
		}()
		var remoteSID int64
		for data := range incoming {
			if len(data) < 1 {
				continue
			}
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
					lastDataAt = time.Now()
					n, err := dataConn.Write(data[4:])
					if err != nil {
						log.Printf("Tunnel: dataConn write error (ID: %d): %v", connID, err)
						return
					}
					bytesReceived += int64(n)
					lastRevSeq = seq
					// log.Printf("Tunnel: Received pkt seq=%d, len=%d (total recv: %d)", seq, n, bytesReceived)
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
					ss.lastRemoteFPS = hb.TargetFPS
					ss.remoteReceivedFPS = hb.ReceivedFPS
					ss.mu.Unlock()
				}
			}
		}
	}()

	<-done
	log.Printf("Tunnel: Closed. Sent: %d bytes, Received: %d bytes", bytesSent, bytesReceived)
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
	video := &ScreenVideoConn{X: x, Y: y, Margin: margin, ReadDelay: 100 * time.Millisecond, SessionID: rand.Int63()}

	activeVideoMu.Lock()
	activeVideoConn = video
	activeVideoMu.Unlock()

	pd := NewPacketDispatcher()
	go pd.Run(video, margin)

	var lastHeartbeatRecv time.Time
	var lastLog time.Time
	for {
		select {
		case data := <-pd.heartbeatCh:
			var hb HeartbeatData
			if err := json.Unmarshal(data[1:], &hb); err == nil {
				if time.Since(lastLog) > 5*time.Second {
					log.Printf("Server: Remote quality: SID=%d, FPS=%.1f, TargetFPS=%d", hb.SessionID, hb.FPS, hb.TargetFPS)
					lastLog = time.Now()
				}
				lastHeartbeatRecv = time.Now()

				if hb.TargetFPS > 0 {
					newDelay := time.Second / time.Duration(hb.TargetFPS*2)
					if newDelay < 20*time.Millisecond {
						newDelay = 20 * time.Millisecond
					}
					if video.ReadDelay != newDelay {
						log.Printf("Server: Adapting capture delay to %v (Target FPS: %d)", newDelay, hb.TargetFPS)
						video.ReadDelay = newDelay
					}
				}

				fps, ms := getPerfMetrics()
				resp := HeartbeatData{
					FPS:          fps,
					ProcessingMS: ms,
					Timestamp:    time.Now().Unix(),
					TargetFPS:    int(1000 / video.ReadDelay.Milliseconds()),
					ReceivedFPS:  getRecvFPS(),
					Ready:        true,
					SessionID:    video.SessionID,
				}
				hbBytes, _ := json.Marshal(resp)
				writeToVCam(Encode(append([]byte{typeHeartbeat}, hbBytes...), margin))
			}

		case data := <-pd.connectCh:
			if len(data) < 3 {
				continue
			}
			connID := uint16(data[1])<<8 | uint16(data[2])
			targetAddr := string(data[3:])
			targetAddr = strings.TrimRight(targetAddr, "\x00")
			log.Printf("Server: Decoded CONNECT to %s (ID: %d)", targetAddr, connID)

			targetConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
			status := byte(0)
			if err != nil {
				log.Printf("Server: dial failed to %s: %v", targetAddr, err)
				status = 1
			}

			payload := make([]byte, 4)
			payload[0] = typeConnAck
			payload[1] = byte(connID >> 8)
			payload[2] = byte(connID)
			payload[3] = status
			writeToVCam(Encode(payload, margin))

			if err == nil {
				ch := pd.Register(connID)
				go func() {
					runTunnelWithPrefix(targetConn, video, margin, connID, 0, ch)
					pd.Unregister(connID)
					targetConn.Close()
				}()
			}

		case <-time.After(5 * time.Second):
			hbTimeout := 60 * time.Second
			if currentCfg != nil && currentCfg.HeartbeatInterval > 0 {
				hbTimeout = time.Duration(currentCfg.HeartbeatInterval) * 2 * time.Second
			}
			if !lastHeartbeatRecv.IsZero() && time.Since(lastHeartbeatRecv) > hbTimeout {
				if video.ReadDelay < 100*time.Millisecond {
					video.ReadDelay = 100 * time.Millisecond
					log.Printf("Server: No heartbeats received for %v, slowing down capture...", hbTimeout)
					lastHeartbeatRecv = time.Time{}
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

	pd := NewPacketDispatcher()
	go pd.Run(video, margin)

	for {
		log.Printf("Client: Starting handshake/calibration phase...")
		bestFPS := 1

		// 1. Ждем хотя бы минимальной стабильности на 1 FPS
		for {
			log.Printf("Client: Testing base FPS level: 1...")
			video.ReadDelay = 500 * time.Millisecond

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

			timer := time.After(2 * time.Second)
			connected := false
		CalibrationLoop1:
			for {
				select {
				case data := <-pd.heartbeatCh:
					var remoteHb HeartbeatData
					if err := json.Unmarshal(data[1:], &remoteHb); err == nil && remoteHb.Ready {
						connected = true
						break CalibrationLoop1
					}
				case <-timer:
					break CalibrationLoop1
				}
			}

			if connected {
				log.Printf("Client: Connection established at 1 FPS")
				break
			}
			log.Printf("Client: Server not responding, retrying...")
			time.Sleep(1 * time.Second)
		}

		// 2. Пробуем повысить FPS
		for _, testFPS := range fpsLevels {
			if testFPS <= 1 {
				continue
			}
			log.Printf("Client: Testing FPS level: %d...", testFPS)
			video.ReadDelay = time.Duration(1000/(testFPS*2)) * time.Millisecond

			success := false
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

			timer := time.After(2 * time.Second)
		CalibrationLoop2:
			for {
				select {
				case data := <-pd.heartbeatCh:
					var remoteHb HeartbeatData
					if err := json.Unmarshal(data[1:], &remoteHb); err == nil && remoteHb.Ready {
						if remoteHb.ReceivedFPS >= testFPS-2 && remoteHb.ReceivedFPS > 0 {
							success = true
							break CalibrationLoop2
						}
					}
				case <-timer:
					break CalibrationLoop2
				}
			}

			if success {
				log.Printf("Client: FPS level %d is stable", testFPS)
				bestFPS = testFPS
			} else {
				log.Printf("Client: FPS level %d failed, staying at %d", testFPS, bestFPS)
				break
			}
		}

		log.Printf("Client: Calibration finished. Best FPS: %d", bestFPS)
		video.ReadDelay = time.Duration(1000/(bestFPS*2)) * time.Millisecond

		ln, err := net.Listen("tcp", localListenAddr)
		if err != nil {
			log.Printf("Client: Failed to listen on %s: %v", localListenAddr, err)
			return
		}
		log.Printf("Client: SOCKS5 server listening on %s", localListenAddr)

		stopSession := make(chan struct{})
		go func() {
			hbInterval := 30 * time.Second
			if currentCfg != nil && currentCfg.HeartbeatInterval > 0 {
				hbInterval = time.Duration(currentCfg.HeartbeatInterval) * time.Second
			}
			ticker := time.NewTicker(hbInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopSession:
					return
				case <-pd.heartbeatCh:
					// Just consume
				case <-ticker.C:
					fpsMetrics, ms := getPerfMetrics()
					hb := HeartbeatData{
						FPS:          fpsMetrics,
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

		for {
			localConn, err := ln.Accept()
			if err != nil {
				break
			}

			targetAddr, err := HandleSocksHandshake(localConn)
			if err != nil {
				log.Printf("Client: SOCKS5 handshake failed: %v", err)
				localConn.Close()
				continue
			}

			connID := uint16(rand.Intn(65535) + 1)
			log.Printf("Client: New connection to %s (ID: %d)", targetAddr, connID)

			payload := make([]byte, 3+len(targetAddr))
			payload[0] = typeConnect
			payload[1] = byte(connID >> 8)
			payload[2] = byte(connID)
			copy(payload[3:], targetAddr)
			writeToVCam(Encode(payload, margin))

			ch := pd.Register(connID)
			success := false
			timer := time.After(5 * time.Second)
		WaitAck:
			for {
				select {
				case data := <-ch:
					if data[0] == typeConnAck && len(data) >= 4 {
						success = (data[3] == 0)
						break WaitAck
					}
				case <-timer:
					break WaitAck
				}
			}

			if !success {
				log.Printf("Client: Failed to establish tunnel to %s (ID: %d)", targetAddr, connID)
				pd.Unregister(connID)
				_ = SendSocksResponse(localConn, fmt.Errorf("failed"))
				localConn.Close()
				continue
			}

			_ = SendSocksResponse(localConn, nil)
			log.Printf("Client: Tunnel established to %s (ID: %d)", targetAddr, connID)

			go func() {
				runTunnelWithPrefix(localConn, video, margin, connID, bestFPS, ch)
				pd.Unregister(connID)
				localConn.Close()
			}()
		}
		close(stopSession)
		ln.Close()
		time.Sleep(1 * time.Second)
	}
}
