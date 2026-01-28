package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"sync"
)

type MJPEGServer struct {
	listener net.Listener
	port     int
	current  *image.RGBA
	mu       sync.RWMutex
	clients  map[chan []byte]bool
	clientMu sync.Mutex
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

	ch := make(chan []byte, 1)
	s.clientMu.Lock()
	s.clients[ch] = true
	s.clientMu.Unlock()

	defer func() {
		fmt.Printf("MJPEG: Client disconnected from %s\n", r.RemoteAddr)
		s.clientMu.Lock()
		delete(s.clients, ch)
		s.clientMu.Unlock()
	}()

	for {
		imgData, ok := <-ch
		if !ok {
			return
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
	s.mu.Lock()
	s.current = img
	s.mu.Unlock()

	// Кодируем в JPEG
	// Используем простой буфер для начала
	var b []byte
	w := &bufferWriter{b: b}
	err := jpeg.Encode(w, img, &jpeg.Options{Quality: 80})
	if err != nil {
		return
	}

	s.clientMu.Lock()
	for ch := range s.clients {
		select {
		case ch <- w.b:
		default:
			// Пропускаем кадр для медленных клиентов
		}
	}
	s.clientMu.Unlock()
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
