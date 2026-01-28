package main

import (
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	typeConnect = 0x00
	typeConnAck = 0x01
	typeData    = 0x02
)

var (
	activeVideoConn *ScreenVideoConn
	activeVideoMu   sync.RWMutex
)

// ScreenVideoConn реализует io.ReadWriter для работы через захват экрана и VCam
type ScreenVideoConn struct {
	HWND   syscall.Handle
	X, Y   int
	Margin int
}

func (s *ScreenVideoConn) Read(p []byte) (n int, err error) {
	// Ожидаем, что p имеет размер captureWidth * captureHeight * 4
	if len(p) < captureWidth*captureHeight*4 {
		return 0, io.ErrShortBuffer
	}

	// Ограничиваем частоту захвата, чтобы не перегружать CPU
	time.Sleep(30 * time.Millisecond)

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
func runTunnelWithPrefix(dataConn io.ReadWriteCloser, videoConn io.ReadWriteCloser, margin int) {
	done := make(chan bool, 2)
	start := time.Now()
	var bytesSent, bytesReceived int64
	var lastSentSeq, lastRevSeq byte

	go func() {
		defer func() { done <- true }()
		for {
			// С новым кодеком (blockSize=8) у нас около 500 байт на кадр.
			maxData := 490
			buf := make([]byte, maxData)
			n, err := dataConn.Read(buf)
			if err != nil {
				return
			}

			lastSentSeq++
			payload := append([]byte{typeData, lastSentSeq}, buf[:n]...)
			img := Encode(payload, margin)
			writeToVCam(img)
			if _, err := videoConn.Write(img.Pix); err != nil {
				return
			}
			bytesSent += int64(n)
			if bytesSent%1000 < int64(n) {
				log.Printf("Tunnel: Sent %d bytes so far", bytesSent)
			}
		}
	}()

	go func() {
		defer func() { done <- true }()
		for {
			frameSize := captureWidth * captureHeight * 4
			buf := make([]byte, frameSize)
			if _, err := io.ReadFull(videoConn, buf); err != nil {
				return
			}

			img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}
			data := Decode(img, margin)
			if len(data) >= 2 && data[0] == typeData {
				UpdateCaptureStatus(true)
				seq := data[1]
				if seq != lastRevSeq {
					n, err := dataConn.Write(data[2:])
					if err != nil {
						return
					}
					bytesReceived += int64(n)
					lastRevSeq = seq
					if bytesReceived%1000 < int64(n) {
						log.Printf("Tunnel: Received %d bytes so far", bytesReceived)
					}
				}
			}
		}
	}()

	<-done
	duration := time.Since(start)
	log.Printf("Tunnel: Closed. Duration: %v, Sent: %d bytes, Received: %d bytes", duration, bytesSent, bytesReceived)
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
	video := &ScreenVideoConn{X: x, Y: y, Margin: margin}

	activeVideoMu.Lock()
	activeVideoConn = video
	activeVideoMu.Unlock()

	frameCount := 0
	for {
		frameSize := captureWidth * captureHeight * 4
		buf := make([]byte, frameSize)
		_, err := io.ReadFull(video, buf)
		if err != nil {
			log.Printf("Server: screen read error: %v", err)
			time.Sleep(time.Second)
			continue
		}

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
			UpdateCaptureStatus(true)
			log.Printf("Server: Decoded %d bytes from screen", len(data))
		}
		if len(data) > 0 && data[0] == typeConnect {
			targetAddr := string(data[1:])
			log.Printf("Server: Request to %s", targetAddr)

			targetConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
			status := byte(0)
			if err != nil {
				log.Printf("Server: dial failed to %s: %v", targetAddr, err)
				status = 1
			}

			// Отправляем ACK
			ackImg := Encode([]byte{typeConnAck, status}, margin)
			writeToVCam(ackImg)

			if status == 0 {
				log.Printf("Server: Tunnel established for %s", targetAddr)
				runTunnelWithPrefix(targetConn, video, margin)
				targetConn.Close()
				log.Println("Server: Connection closed, waiting for next...")
			}
		}
	}
}

// RunScreenSocksClient работает через захват экрана и VCam
func RunScreenSocksClient(localListenAddr string, x, y, margin int) {
	ln, err := net.Listen("tcp", localListenAddr)
	if err != nil {
		log.Fatalf("Client: failed to listen: %v", err)
	}
	log.Printf("Client: Listening (SOCKS5) on %s, watching screen at (%d, %d) with margin %d", localListenAddr, x, y, margin)

	video := &ScreenVideoConn{X: x, Y: y, Margin: margin}

	activeVideoMu.Lock()
	activeVideoConn = video
	activeVideoMu.Unlock()

	for {
		localConn, err := ln.Accept()
		if err != nil {
			log.Printf("Client: accept error: %v", err)
			continue
		}
		log.Printf("Client: Accepted local connection from %s", localConn.RemoteAddr())

		targetAddr, err := HandleSocksHandshake(localConn)
		if err != nil {
			log.Printf("Client: handshake error: %v", err)
			localConn.Close()
			continue
		}

		// 1. Отправляем CONNECT
		log.Printf("Client: Sending CONNECT to %s", targetAddr)
		connectImg := Encode(append([]byte{typeConnect}, []byte(targetAddr)...), margin)
		for j := 0; j < 5; j++ { // Отправляем 5 раз для надежности
			writeToVCam(connectImg)
			time.Sleep(100 * time.Millisecond)
		}

		// 2. Ждем ACK
		log.Printf("Client: Waiting for ACK for %s", targetAddr)
		success := false
		for i := 0; i < 300; i++ { // Пытаемся 300 раз (около 10 секунд)
			frameSize := captureWidth * captureHeight * 4
			buf := make([]byte, frameSize)
			_, err := io.ReadFull(video, buf)
			if err != nil {
				break
			}
			img := &image.RGBA{Pix: buf, Stride: captureWidth * 4, Rect: image.Rect(0, 0, captureWidth, captureHeight)}
			ackData := Decode(img, margin)
			if len(ackData) >= 2 && ackData[0] == typeConnAck {
				UpdateCaptureStatus(true)
				if ackData[1] == 0 {
					success = true
				}
				break
			}
			time.Sleep(30 * time.Millisecond)
		}

		if !success {
			log.Printf("Client: Failed to establish tunnel to %s", targetAddr)
			_ = SendSocksResponse(localConn, fmt.Errorf("failed"))
			localConn.Close()
			continue
		}

		_ = SendSocksResponse(localConn, nil)
		log.Printf("Client: Tunnel established to %s", targetAddr)
		runTunnelWithPrefix(localConn, video, margin)
		localConn.Close()
		log.Println("Client: Connection closed")
	}
}
