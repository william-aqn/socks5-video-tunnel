package main

import (
	"encoding/binary"
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
	typeConnect      = 0x00
	typeConnAck      = 0x01
	typeData         = 0x02
	typeHeartbeat    = 0x03
	typeDisconnect   = 0x04
	typeSync         = 0x05
	typeSyncComplete = 0x06
)

type HeartbeatData struct {
	FPS          float32 `json:"fps"`
	ProcessingMS int     `json:"ms"`
	Timestamp    int64   `json:"ts"`
	TargetFPS    int     `json:"target_fps"`   // Скорость, с которой я отправляю
	ReceivedFPS  int     `json:"received_fps"` // Скорость, которую я успешно принимаю от тебя
	Ready        bool    `json:"ready"`        // Готовность к передаче данных
	SessionID    int64   `json:"sid"`          // Идентификатор сессии
	Seq          uint32  `json:"seq"`          // Порядковый номер
	Phase        int     `json:"phase"`        // 0: Normal, 1: Client -> Server test, 2: Server -> Client test
}

type SyncData struct {
	SessionID   int64  `json:"sid"`
	Random      string `json:"rnd"`
	MeasuredFPS int    `json:"fps,omitempty"`
}

type SyncCompleteData struct {
	SessionID int64 `json:"sid"`
	FPS       int   `json:"fps"`
}

func generateRandomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
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

var (
	sentMu    sync.Mutex
	sentStats = make(map[byte]int)
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 2048)
	},
}

func getBuffer() []byte {
	return bufferPool.Get().([]byte)
}

func putBuffer(b []byte) {
	bufferPool.Put(b)
}

var (
	trafficMu          sync.Mutex
	bytesSentTotal     int64
	bytesReceivedTotal int64
	lastTrafficCheck   time.Time
	lastSentKBs        float64
	lastRecvKBs        float64
)

func recordTrafficSent(n int) {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	bytesSentTotal += int64(n)
}

func recordTrafficRecv(n int) {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	bytesReceivedTotal += int64(n)
}

func getTrafficStats() (sentKBs, recvKBs float64) {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	now := time.Now()
	if lastTrafficCheck.IsZero() {
		lastTrafficCheck = now
		return 0, 0
	}
	dur := now.Sub(lastTrafficCheck).Seconds()
	if dur >= 1.0 {
		lastSentKBs = float64(bytesSentTotal) / 1024.0 / dur
		lastRecvKBs = float64(bytesReceivedTotal) / 1024.0 / dur
		bytesSentTotal = 0
		bytesReceivedTotal = 0
		lastTrafficCheck = now
	}
	return lastSentKBs, lastRecvKBs
}

func sendEncodedPacket(payload []byte, margin int) {
	recordTrafficSent(len(payload))
	writeToVCam(Encode(payload, margin), margin)
}

func recordSentPacket(t byte) {
	sentMu.Lock()
	defer sentMu.Unlock()
	sentStats[t]++
}

func getSentStatsAndReset() string {
	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sentStats) == 0 {
		return "none"
	}
	res := ""
	for t, count := range sentStats {
		typeName := "unknown"
		switch t {
		case typeConnect:
			typeName = "CONNECT"
		case typeConnAck:
			typeName = "ACK"
		case typeData:
			typeName = "DATA"
		case typeHeartbeat:
			typeName = "HB"
		case typeSync:
			typeName = "SYNC"
		case typeSyncComplete:
			typeName = "SYNC_DONE"
		case typeDisconnect:
			typeName = "DISCONNECT"
		}
		res += fmt.Sprintf("%s:%d ", typeName, count)
		sentStats[t] = 0
	}
	return res
}

type PacketDispatcher struct {
	mu           sync.RWMutex
	connChannels map[uint16]chan []byte
	heartbeatCh  chan []byte
	connectCh    chan []byte
	syncCh       chan []byte
	syncCompCh   chan []byte
	margin       int
}

func NewPacketDispatcher(margin int) *PacketDispatcher {
	return &PacketDispatcher{
		connChannels: make(map[uint16]chan []byte),
		heartbeatCh:  make(chan []byte, 32),
		connectCh:    make(chan []byte, 32),
		syncCh:       make(chan []byte, 32),
		syncCompCh:   make(chan []byte, 32),
		margin:       margin,
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
	case typeSync:
		select {
		case pd.syncCh <- data:
		default:
		}
	case typeSyncComplete:
		select {
		case pd.syncCompCh <- data:
		default:
		}
	case typeData, typeConnAck, typeDisconnect:
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
					log.Printf("Dispatcher: Data for unknown connID %d (len: %d), NOT sending DISCONNECT back (disabled)", id, len(data))
					/*
						payload := []byte{typeDisconnect, data[1], data[2]}
						sendEncodedPacket(payload, pd.margin)
						recordSentPacket(typeDisconnect)
					*/
				} else if data[0] == typeDisconnect {
					log.Printf("Dispatcher: Disconnect for already unknown connID %d", id)
				}
			}
		}
	default:
		log.Printf("Dispatcher: Unknown packet type 0x%02x (len: %d)", data[0], len(data))
	}
}

func (pd *PacketDispatcher) Run(video *ScreenVideoConn, margin int) {
	frameSize := captureWidth * captureHeight * 4
	buf := make([]byte, frameSize)
	for {
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
			recordTrafficRecv(len(data))
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
	//log.Printf("DEBUG: Frame received at %v", time.Now())
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
	if recvLastCheck.IsZero() {
		return 0
	}
	dur := now.Sub(recvLastCheck).Seconds()
	// Обновляем если прошло больше секунды ИЛИ если накопилось хотя бы 1 кадр (ускоряет калибровку)
	if dur >= 1.0 || (dur > 0.05 && recvFrames >= 1) {
		lastRecvFPS = int(float32(recvFrames) / float32(dur))
		if lastRecvFPS < 1 && recvFrames > 0 {
			lastRecvFPS = 1
		}
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
	writeToVCam(img, s.Margin)
	return len(p), nil
}

func (s *ScreenVideoConn) Close() error {
	return nil
}

var (
	vcamMu           sync.Mutex
	vcamLastWrite    time.Time
	vcamCleared      bool
	vcamGlobalMargin int
	vcamIdleOnce     sync.Once
)

func writeToVCam(img *image.RGBA, margin int) {
	vcamIdleOnce.Do(func() {
		go vcamIdleHandler()
	})
	if vcam != nil {
		vcamMu.Lock()
		defer vcamMu.Unlock()
		vcam.WriteFrame(img)
		vcamLastWrite = time.Now()
		vcamCleared = false
		vcamGlobalMargin = margin
	}
}

func vcamIdleHandler() {
	for {
		time.Sleep(100 * time.Millisecond)
		vcamMu.Lock()
		if !vcamCleared && !vcamLastWrite.IsZero() && time.Since(vcamLastWrite) > 500*time.Millisecond {
			if vcam != nil {
				// Кодируем пустой кадр для очистки экрана
				img := Encode(nil, vcamGlobalMargin)
				vcam.WriteFrame(img)
			}
			vcamCleared = true
		}
		vcamMu.Unlock()
	}
}

// runTunnelWithPrefix читает данные из dataConn, упаковывает их в видеокадры с префиксом типа и пишет в VCam.
// Также получает пакеты из incoming канала и пишет в dataConn.
func runTunnelWithPrefix(dataConn io.ReadWriteCloser, video *ScreenVideoConn, margin int, connID uint16, initialFPS int, incoming chan []byte) {
	var wg sync.WaitGroup
	wg.Add(2)
	var closeOnce sync.Once
	sendDisconnect := func() {
		closeOnce.Do(func() {
			payload := []byte{typeDisconnect, byte(connID >> 8), byte(connID)}
			sendEncodedPacket(payload, margin)
			recordSentPacket(typeDisconnect)
		})
	}

	var bytesSent, bytesReceived int64
	var lastSentSeq, lastRevSeq byte
	lastSentSeq = 0
	lastRevSeq = 0

	var myHBSeq uint32

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

	var activityMu sync.Mutex
	lastActivity := time.Now()

	type speedState struct {
		mu                sync.Mutex
		lastRemoteFPS     int
		remoteReceivedFPS int
	}
	ss := &speedState{}

	go func() {
		defer func() {
			log.Printf("Tunnel: Exit Data->Video goroutine (ID: %d)", connID)
			sendDisconnect()
			wg.Done()
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
				myHBSeq++
				hb := HeartbeatData{
					FPS:          fpsMetrics,
					ProcessingMS: ms,
					Timestamp:    time.Now().Unix(),
					TargetFPS:    fps,
					ReceivedFPS:  getRecvFPS(),
					Ready:        true,
					SessionID:    mySID,
					Seq:          myHBSeq,
				}
				hbBytes, _ := json.Marshal(hb)
				payload := append([]byte{typeHeartbeat}, hbBytes...)
				sendEncodedPacket(payload, margin)
				recordSentPacket(typeHeartbeat)
				lastHeartbeat = time.Now()

				activityMu.Lock()
				lastActivity = time.Now()
				activityMu.Unlock()

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

			maxData := 450
			buf := getBuffer()
			if tc, ok := dataConn.(interface{ SetReadDeadline(time.Time) error }); ok {
				tc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			}
			n, err := dataConn.Read(buf[:maxData])

			if err == nil && n > 0 {
				activityMu.Lock()
				lastActivity = time.Now()
				activityMu.Unlock()

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
				sendEncodedPacket(payload, margin)
				recordSentPacket(typeData)
				bytesSent += int64(n)
			}
			putBuffer(buf)

			if err != nil {
				if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
					if err != io.EOF {
						log.Printf("Tunnel: dataConn read error (ID: %d): %v", connID, err)
					}
					return
				}
			}

			elapsed := time.Since(loopStart)
			if elapsed < sendInterval {
				time.Sleep(sendInterval - elapsed)
			}

			activityMu.Lock()
			inactive := time.Since(lastActivity) > 60*time.Second
			activityMu.Unlock()

			if inactive {
				log.Printf("Tunnel: Inactive for 60s, closing (ID: %d)", connID)
				return
			}
		}
	}()

	go func() {
		defer func() {
			log.Printf("Tunnel: Exit Video->Data goroutine (ID: %d)", connID)
			sendDisconnect()
			wg.Done()
		}()
		var remoteSID int64
		var lastRemoteHBSeq uint32
		for data := range incoming {
			if len(data) < 1 {
				continue
			}
			switch data[0] {
			case typeDisconnect:
				log.Printf("Tunnel: Received DISCONNECT (ID: %d)", connID)
				closeOnce.Do(func() {}) // Mark as closed without sending back
				return
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
					activityMu.Lock()
					lastActivity = time.Now()
					activityMu.Unlock()

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
					if hb.Seq != 0 && hb.Seq <= lastRemoteHBSeq && hb.SessionID == remoteSID {
						continue // Пропускаем дубликаты
					}
					remoteSID = hb.SessionID
					lastRemoteHBSeq = hb.Seq
					ss.mu.Lock()
					ss.lastRemoteFPS = hb.TargetFPS
					ss.remoteReceivedFPS = hb.ReceivedFPS
					ss.mu.Unlock()
				}
			}
		}
	}()

	wg.Wait()
	log.Printf("Tunnel: Closed. Sent: %d bytes, Received: %d bytes", bytesSent, bytesReceived)
	// Очищаем VCam, чтобы не висел старый кадр
	for i := 0; i < 3; i++ {
		sendEncodedPacket(nil, margin)
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

	pd := NewPacketDispatcher(margin)
	go pd.Run(video, margin)

	var lastHeartbeatRecv time.Time
	var lastLog time.Time
	var lastHBSeq uint32
	var remoteSID int64
	var syncPhase int // 0: Idle, 1: CalibratingClient, 2: SendingToClient, 3: Done
	var syncStartTime time.Time
	var syncCount int
	var stopServerSync chan struct{}

	for {
		select {
		case data := <-pd.syncCh:
			var sd SyncData
			if err := json.Unmarshal(data[1:], &sd); err == nil {
				if remoteSID != 0 && sd.SessionID != remoteSID {
					log.Printf("Server: Remote session ID changed (%d -> %d), restarting sync", remoteSID, sd.SessionID)
					remoteSID = sd.SessionID
					syncPhase = 0
					if stopServerSync != nil {
						close(stopServerSync)
						stopServerSync = nil
					}
				}

				if syncPhase == 0 {
					log.Printf("Server: New sync session detected (SID=%d). Phase 1: Calibrating client for 10s...", sd.SessionID)
					remoteSID = sd.SessionID
					syncPhase = 1
					video.ReadDelay = 0 // Max speed for calibration
					syncStartTime = time.Now()
					syncCount = 0
				}

				if syncPhase == 1 {
					syncCount++
					if time.Since(syncStartTime) >= 10*time.Second {
						dur := time.Since(syncStartTime).Seconds()
						calculatedFPS := int(float64(syncCount) / dur)
						if calculatedFPS < 1 {
							calculatedFPS = 1
						}
						if calculatedFPS > 30 {
							calculatedFPS = 30
						}
						log.Printf("Server: Phase 1 done. Client FPS=%d (dur=%.2fs). Transitioning to Phase 2...", calculatedFPS, dur)

						syncPhase = 2
						stopServerSync = make(chan struct{})
						// Начинаем отправлять свои синхропакеты
						go func(sid int64, fps int, stop chan struct{}) {
							log.Printf("Server: Phase 2: Sending SYNC to client...")
							for {
								select {
								case <-stop:
									return
								default:
									resp := SyncData{SessionID: video.SessionID, Random: generateRandomString(32), MeasuredFPS: fps}
									respBytes, _ := json.Marshal(resp)
									sendEncodedPacket(append([]byte{typeSync}, respBytes...), margin)
									recordSentPacket(typeSync)
									time.Sleep(10 * time.Millisecond) // Max rate 100 FPS
								}
							}
						}(video.SessionID, calculatedFPS, stopServerSync)
					}
				}
			}

		case data := <-pd.syncCompCh:
			var scd SyncCompleteData
			if err := json.Unmarshal(data[1:], &scd); err == nil {
				if scd.SessionID == remoteSID && syncPhase == 2 {
					log.Printf("Server: Received SYNC_COMPLETE. Final FPS: %d", scd.FPS)
					if stopServerSync != nil {
						close(stopServerSync)
						stopServerSync = nil
					}
					video.ReadDelay = time.Second / time.Duration(scd.FPS)
					syncPhase = 3
				}
			}

		case data := <-pd.heartbeatCh:
			if syncPhase != 3 {
				continue
			}
			var hb HeartbeatData
			if err := json.Unmarshal(data[1:], &hb); err == nil {
				if hb.Seq != 0 && hb.Seq <= lastHBSeq && hb.SessionID == remoteSID && hb.Phase == 0 {
					continue
				}
				lastHBSeq = hb.Seq

				if time.Since(lastLog) > 5*time.Second {
					log.Printf("Server: Quality: SID=%d, Phase=%d, RemoteFPS=%.1f, RemoteTarget=%d, Sent:[%s], RecvFPS=%d",
						hb.SessionID, hb.Phase, hb.FPS, hb.TargetFPS, getSentStatsAndReset(), getRecvFPS())
					lastLog = time.Now()
				}
				lastHeartbeatRecv = time.Now()

				if hb.TargetFPS > 0 {
					newDelay := time.Second / time.Duration(hb.TargetFPS)
					if newDelay < 10*time.Millisecond {
						newDelay = 10 * time.Millisecond
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
					TargetFPS:    int(time.Second / video.ReadDelay),
					ReceivedFPS:  getRecvFPS(),
					Ready:        true,
					SessionID:    video.SessionID,
					Seq:          hb.Seq,
					Phase:        0,
				}
				hbBytes, _ := json.Marshal(resp)
				sendEncodedPacket(append([]byte{typeHeartbeat}, hbBytes...), margin)
				recordSentPacket(typeHeartbeat)
			}

		case data := <-pd.connectCh:
			if syncPhase != 3 {
				continue
			}
			if len(data) < 4 { // Тип(1) + ID(2) + Seq(1) + Адрес
				continue
			}
			connID := uint16(data[1])<<8 | uint16(data[2])
			seq := data[3]
			targetAddr := string(data[4:])
			targetAddr = strings.TrimRight(targetAddr, "\x00")

			// Проверка на дубликаты CONNECT
			activeVideoMu.RLock()
			pd_local := pd
			activeVideoMu.RUnlock()
			pd_local.mu.RLock()
			_, alreadyActive := pd_local.connChannels[connID]
			pd_local.mu.RUnlock()

			if alreadyActive {
				// Просто подтверждаем еще раз, если это повтор
				// Для повтора отправляем пустой адрес, так как клиент уже должен иметь его или он ему не важен
				payload := make([]byte, 4+1+4+2)
				payload[0] = typeConnAck
				payload[1] = byte(connID >> 8)
				payload[2] = byte(connID)
				payload[3] = socks5RespSuccess
				payload[4] = socks5AtypIPv4
				// остальное нули
				sendEncodedPacket(payload, margin)
				recordSentPacket(typeConnAck)
				continue
			}

			log.Printf("Server: Decoded CONNECT to %s (ID: %d, seq: %d)", targetAddr, connID, seq)

			targetConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
			status := byte(socks5RespSuccess)
			var boundAddr []byte
			atyp := byte(socks5AtypIPv4)
			port := uint16(0)

			if err != nil {
				log.Printf("Server: dial failed to %s: %v", targetAddr, err)
				status = socks5RespFailure
				errStr := err.Error()
				if strings.Contains(errStr, "refused") {
					status = socks5RespConnRefused
				} else if strings.Contains(errStr, "unreachable") {
					status = socks5RespHostUnreach
				} else if strings.Contains(errStr, "timeout") {
					status = socks5RespTTLExpired
				}
			} else {
				if tcpAddr, ok := targetConn.LocalAddr().(*net.TCPAddr); ok {
					if ip4 := tcpAddr.IP.To4(); ip4 != nil {
						atyp = socks5AtypIPv4
						boundAddr = ip4
					} else {
						atyp = socks5AtypIPv6
						boundAddr = tcpAddr.IP
					}
					port = uint16(tcpAddr.Port)
				}
			}

			if boundAddr == nil {
				boundAddr = make([]byte, 4)
			}

			payload := make([]byte, 4+1+len(boundAddr)+2)
			payload[0] = typeConnAck
			payload[1] = byte(connID >> 8)
			payload[2] = byte(connID)
			payload[3] = status
			payload[4] = atyp
			copy(payload[5:], boundAddr)
			binary.BigEndian.PutUint16(payload[5+len(boundAddr):], port)
			sendEncodedPacket(payload, margin)
			recordSentPacket(typeConnAck)

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

	pd := NewPacketDispatcher(margin)
	go pd.Run(video, margin)

	var hbSeq uint32

	for {
		log.Printf("Client: Starting synchronization...")
		var serverSID int64
		var syncStartTime time.Time
		var syncCount int
		var clientSyncPhase int // 0: Initiating, 1: CalibratingServer, 2: Done
		var stopInitiating chan struct{} = make(chan struct{})

		// Phase 0: Отправляем свои синхропакеты на максимально доступной скорости
		go func(sid int64, stop chan struct{}) {
			for {
				select {
				case <-stop:
					return
				default:
					syncPayload, _ := json.Marshal(SyncData{SessionID: sid, Random: generateRandomString(32)})
					sendEncodedPacket(append([]byte{typeSync}, syncPayload...), margin)
					recordSentPacket(typeSync)
					time.Sleep(10 * time.Millisecond)
				}
			}
		}(video.SessionID, stopInitiating)

	WaitSync:
		for {
			select {
			case data := <-pd.syncCh:
				var sd SyncData
				if err := json.Unmarshal(data[1:], &sd); err == nil {
					if clientSyncPhase == 0 {
						log.Printf("Client: Server SYNC detected (SID=%d). Phase 1: Calibrating server for 10s...", sd.SessionID)
						// Останавливаем свою отправку
						close(stopInitiating)
						serverSID = sd.SessionID
						clientSyncPhase = 1
						video.ReadDelay = 0 // Max speed for calibration
						syncStartTime = time.Now()
						syncCount = 0
					}
					if clientSyncPhase == 1 && sd.SessionID == serverSID {
						syncCount++
						if time.Since(syncStartTime) >= 10*time.Second {
							dur := time.Since(syncStartTime).Seconds()
							calculatedFPS := int(float64(syncCount) / dur)
							if calculatedFPS < 1 {
								calculatedFPS = 1
							}
							if calculatedFPS > 30 {
								calculatedFPS = 30
							}
							log.Printf("Client: Phase 1 done. Server FPS=%d (dur=%.2fs). Sending SYNC_COMPLETE...", calculatedFPS, dur)

							// Phase 2: Отправляем SYNC_COMPLETE
							scd := SyncCompleteData{SessionID: video.SessionID, FPS: calculatedFPS}
							scdBytes, _ := json.Marshal(scd)
							for i := 0; i < 5; i++ { // Отправляем несколько раз для надежности
								sendEncodedPacket(append([]byte{typeSyncComplete}, scdBytes...), margin)
								recordSentPacket(typeSyncComplete)
								time.Sleep(50 * time.Millisecond)
							}

							video.ReadDelay = time.Second / time.Duration(calculatedFPS)
							clientSyncPhase = 2
							break WaitSync
						}
					}
				}
			case <-time.After(60 * time.Second):
				log.Printf("Client: Sync timeout, retrying...")
				if clientSyncPhase == 0 {
					close(stopInitiating)
				}
				break WaitSync
			}
		}

		if clientSyncPhase != 2 {
			continue
		}

		bestFPS := int(time.Second / video.ReadDelay)

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
			lastClientLog := time.Now()
			var lastRemoteHBSeq uint32

			for {
				select {
				case <-stopSession:
					return
				case data := <-pd.heartbeatCh:
					var hb HeartbeatData
					if err := json.Unmarshal(data[1:], &hb); err == nil {
						if hb.Seq != 0 && hb.Seq <= lastRemoteHBSeq {
							continue
						}
						lastRemoteHBSeq = hb.Seq

						// Периодический лог качества на клиенте
						if time.Since(lastClientLog) > 5*time.Second {
							log.Printf("Client: Quality: SID=%d, RemoteFPS=%.1f, RemoteTarget=%d, Sent:[%s], RecvFPS=%d",
								hb.SessionID, hb.FPS, hb.TargetFPS, getSentStatsAndReset(), getRecvFPS())
							lastClientLog = time.Now()
						}
					}
				case <-ticker.C:
					fpsMetrics, ms := getPerfMetrics()
					hbSeq++
					hb := HeartbeatData{
						FPS:          fpsMetrics,
						ProcessingMS: ms,
						Timestamp:    time.Now().Unix(),
						TargetFPS:    bestFPS,
						ReceivedFPS:  getRecvFPS(),
						Ready:        true,
						SessionID:    video.SessionID,
						Seq:          hbSeq,
					}
					hbBytes, _ := json.Marshal(hb)
					sendEncodedPacket(append([]byte{typeHeartbeat}, hbBytes...), margin)
					recordSentPacket(typeHeartbeat)
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

			payload := make([]byte, 4+len(targetAddr))
			payload[0] = typeConnect
			payload[1] = byte(connID >> 8)
			payload[2] = byte(connID)
			payload[3] = 1 // Initial seq for connect
			copy(payload[4:], targetAddr)
			sendEncodedPacket(payload, margin)
			recordSentPacket(typeConnect)

			ch := pd.Register(connID)
			success := false
			var remoteBoundAddr net.Addr
			var lastStatus byte = socks5RespFailure
			timer := time.After(5 * time.Second)
		WaitAck:
			for {
				select {
				case data := <-ch:
					if data[0] == typeConnAck && len(data) >= 4 {
						lastStatus = data[3]
						success = (lastStatus == socks5RespSuccess)
						if success && len(data) >= 7 {
							atyp := data[4]
							var ip net.IP
							var port uint16
							if atyp == socks5AtypIPv4 && len(data) >= 11 {
								ip = net.IP(data[5:9])
								port = binary.BigEndian.Uint16(data[9:11])
							} else if atyp == socks5AtypIPv6 && len(data) >= 23 {
								ip = net.IP(data[5:21])
								port = binary.BigEndian.Uint16(data[21:23])
							}
							if ip != nil {
								remoteBoundAddr = &net.TCPAddr{IP: ip, Port: int(port)}
							}
						}
						break WaitAck
					}
				case <-timer:
					// Повторная отправка CONNECT если нет ответа 2 секунды
					sendEncodedPacket(payload, margin)
					recordSentPacket(typeConnect)
					timer = time.After(3 * time.Second) // Ждем еще до общего таймаута 5с
				}
			}

			if !success {
				log.Printf("Client: Failed to establish tunnel to %s (ID: %d), status: 0x%02x", targetAddr, connID, lastStatus)
				pd.Unregister(connID)
				var err error
				if lastStatus != socks5RespSuccess {
					err = fmt.Errorf("socks5 error: 0x%02x", lastStatus)
				} else {
					err = fmt.Errorf("failed")
				}
				_ = SendSocksResponse(localConn, err, nil)
				localConn.Close()
				continue
			}

			_ = SendSocksResponse(localConn, nil, remoteBoundAddr)
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
