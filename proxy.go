package main

import (
	"fmt"
	"image"
	"io"
	"log"
	"net"
	"sync"
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
	X, Y   int
	Margin int
}

func (s *ScreenVideoConn) Read(p []byte) (n int, err error) {
	// Ожидаем, что p имеет размер width * height * 4
	if len(p) < width*height*4 {
		return 0, io.ErrShortBuffer
	}

	// Ограничиваем частоту захвата, чтобы не перегружать CPU
	time.Sleep(30 * time.Millisecond)

	activeVideoMu.RLock()
	curX, curY := s.X, s.Y
	activeVideoMu.RUnlock()

	img, err := CaptureScreen(curX, curY, width, height)
	if err != nil {
		return 0, err
	}

	copy(p, img.Pix)
	return width * height * 4, nil
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

	go func() {
		defer func() { done <- true }()
		for {
			// Оставляем 1 байт под префикс typeData
			// Учитываем margin и маркеры
			// Доступные пиксели: (width - 2*margin) * (height - 2*margin) - 4*4 (маркеры 2x2)
			// На самом деле Encode учитывает маркеры внутри цикла.
			// Посчитаем точно сколько байт влезает.
			// Для простоты используем консервативную оценку или вынесем расчет в codec.go
			maxData := (width * height * 3) - 4 - 1
			buf := make([]byte, maxData)
			n, err := dataConn.Read(buf)
			if err != nil {
				return
			}

			payload := append([]byte{typeData}, buf[:n]...)
			img := Encode(payload, margin)
			writeToVCam(img)
			if _, err := videoConn.Write(img.Pix); err != nil {
				return
			}
		}
	}()

	go func() {
		defer func() { done <- true }()
		for {
			frameSize := width * height * 4
			buf := make([]byte, frameSize)
			if _, err := io.ReadFull(videoConn, buf); err != nil {
				return
			}

			img := &image.RGBA{Pix: buf, Stride: width * 4, Rect: image.Rect(0, 0, width, height)}
			// Мы не вызываем writeToVCam здесь для входящих кадров, так как это может создать бесконечную петлю или шум в камере,
			// если камера используется и для вывода, и для ввода (но в данной схеме VCam - только выход).
			data := Decode(img, margin)
			if len(data) > 0 && data[0] == typeData {
				if _, err := dataConn.Write(data[1:]); err != nil {
					return
				}
			}
		}
	}()

	<-done
}

func UpdateActiveCaptureArea(x, y int) {
	activeVideoMu.Lock()
	defer activeVideoMu.Unlock()
	if activeVideoConn != nil {
		activeVideoConn.X = x
		activeVideoConn.Y = y
	}
}

// RunScreenSocksServer работает через захват экрана и VCam с динамическим выбором цели
func RunScreenSocksServer(x, y, margin int) {
	fmt.Printf("Server: Watching screen at (%d, %d) with margin %d\n", x, y, margin)
	video := &ScreenVideoConn{X: x, Y: y, Margin: margin}

	activeVideoMu.Lock()
	activeVideoConn = video
	activeVideoMu.Unlock()

	for {
		frameSize := width * height * 4
		buf := make([]byte, frameSize)
		_, err := io.ReadFull(video, buf)
		if err != nil {
			log.Printf("Server: screen read error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		img := &image.RGBA{Pix: buf, Stride: width * 4, Rect: image.Rect(0, 0, width, height)}
		data := Decode(img, margin)
		if len(data) > 0 && data[0] == typeConnect {
			targetAddr := string(data[1:])
			fmt.Printf("Server: Request to %s\n", targetAddr)

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
				fmt.Printf("Server: Tunnel established for %s\n", targetAddr)
				runTunnelWithPrefix(targetConn, video, margin)
				targetConn.Close()
				fmt.Println("Server: Connection closed, waiting for next...")
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
	fmt.Printf("Client: Listening (SOCKS5) on %s, watching screen at (%d, %d) with margin %d\n", localListenAddr, x, y, margin)

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

		targetAddr, err := HandleSocksHandshake(localConn)
		if err != nil {
			log.Printf("Client: handshake error: %v", err)
			localConn.Close()
			continue
		}

		// 1. Отправляем CONNECT
		connectImg := Encode(append([]byte{typeConnect}, []byte(targetAddr)...), margin)
		writeToVCam(connectImg)

		// 2. Ждем ACK
		fmt.Printf("Client: Waiting for ACK for %s\n", targetAddr)
		success := false
		for i := 0; i < 100; i++ { // Пытаемся 100 раз (около 3 секунд)
			frameSize := width * height * 4
			buf := make([]byte, frameSize)
			_, err := io.ReadFull(video, buf)
			if err != nil {
				break
			}
			img := &image.RGBA{Pix: buf, Stride: width * 4, Rect: image.Rect(0, 0, width, height)}
			ackData := Decode(img, margin)
			if len(ackData) >= 2 && ackData[0] == typeConnAck {
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
		fmt.Printf("Client: Tunnel established to %s\n", targetAddr)
		runTunnelWithPrefix(localConn, video, margin)
		localConn.Close()
		fmt.Println("Client: Connection closed")
	}
}
