package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"sync"
	"time"
)

type MJPEGServer struct {
	listener       net.Listener
	port           int
	current        *image.RGBA
	currentEncoded []byte
	mu             sync.RWMutex
	clients        map[chan []byte]bool
	clientMu       sync.Mutex
}

func NewMJPEGServer(port int) (*MJPEGServer, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}

	s := &MJPEGServer{
		listener: ln,
		port:     ln.Addr().(*net.TCPAddr).Port,
		clients:  make(map[chan []byte]bool),
	}

	go s.run()
	return s, nil
}

func (s *MJPEGServer) run() {
	http.Serve(s.listener, http.HandlerFunc(s.handler))
}

func (s *MJPEGServer) handler(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("MJPEG: Client connected from %s\n", r.RemoteAddr)
	m := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+m.Boundary())

	defer func() {
		fmt.Printf("MJPEG: Client disconnected from %s\n", r.RemoteAddr)
	}()

	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.RLock()
		imgData := s.currentEncoded
		s.mu.RUnlock()

		if imgData == nil {
			continue
		}

		partHeader := make(textproto.MIMEHeader)
		partHeader.Set("Content-Type", "image/jpeg")
		partHeader.Set("Content-Length", fmt.Sprint(len(imgData)))
		part, err := m.CreatePart(partHeader)
		if err != nil {
			return
		}
		if _, err := part.Write(imgData); err != nil {
			return
		}
	}
}

func (s *MJPEGServer) Broadcast(img *image.RGBA) {
	// Кодируем в JPEG
	var b []byte
	w := &bufferWriter{b: b}
	err := jpeg.Encode(w, img, &jpeg.Options{Quality: 100})
	if err != nil {
		return
	}

	s.mu.Lock()
	s.current = img
	s.currentEncoded = w.b
	s.mu.Unlock()
	log.Printf("MJPEG: Broadcasted new frame (%d bytes)", len(w.b))
}

type bufferWriter struct {
	b []byte
}

func (w *bufferWriter) Write(p []byte) (n int, err error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

func (s *MJPEGServer) Close() error {
	return s.listener.Close()
}

func (s *MJPEGServer) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}
