package main

import (
	"io"
	"net"
	"testing"
	"time"
)

// runSimpleSocks5Proxy запускает простой SOCKS5 прокси на заданном слушателе.
func runSimpleSocks5Proxy(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()

			targetAddr, err := HandleSocksHandshake(c)
			if err != nil {
				return
			}

			targetConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
			if err != nil {
				_ = SendSocksResponse(c, err)
				return
			}
			defer targetConn.Close()

			err = SendSocksResponse(c, nil)
			if err != nil {
				return
			}

			// Обычное проксирование данных
			done := make(chan struct{}, 2)
			go func() {
				io.Copy(targetConn, c)
				done <- struct{}{}
			}()
			go func() {
				io.Copy(c, targetConn)
				done <- struct{}{}
			}()

			<-done
		}(conn)
	}
}

func TestSocks5Integration(t *testing.T) {
	// 1. Запускаем эхо-сервер
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	// 2. Запускаем SOCKS5 прокси
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()
	go runSimpleSocks5Proxy(proxyLn)

	// 3. Подключаемся через прокси вручную (как SOCKS5 клиент)
	client, err := net.Dial("tcp", proxyLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Рукопожатие SOCKS5
	// Method selection
	_, err = client.Write([]byte{socks5Ver, 1, socks5MethodNoAuth})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	_, err = io.ReadFull(client, buf)
	if err != nil {
		t.Fatal(err)
	}
	if buf[0] != socks5Ver || buf[1] != socks5MethodNoAuth {
		t.Fatalf("Invalid method response: %v", buf)
	}

	// Request CONNECT to echo server
	echoAddr := echoLn.Addr().(*net.TCPAddr)
	req := []byte{socks5Ver, socks5CmdConnect, 0, socks5AtypIPv4}
	req = append(req, echoAddr.IP.To4()...)
	req = append(req, byte(echoAddr.Port>>8), byte(echoAddr.Port))
	_, err = client.Write(req)
	if err != nil {
		t.Fatal(err)
	}

	// Read response
	resp := make([]byte, 10)
	_, err = io.ReadFull(client, resp)
	if err != nil {
		t.Fatal(err)
	}
	if resp[1] != 0 {
		t.Fatalf("SOCKS5 connect failed with code: %d", resp[1])
	}

	// 4. Проверяем передачу данных
	testMsg := "Hello, SOCKS5!"
	_, err = client.Write([]byte(testMsg))
	if err != nil {
		t.Fatal(err)
	}

	readBuf := make([]byte, len(testMsg))
	_, err = io.ReadFull(client, readBuf)
	if err != nil {
		t.Fatal(err)
	}

	if string(readBuf) != testMsg {
		t.Errorf("Expected %q, got %q", testMsg, string(readBuf))
	}
}

func TestSocks5DomainIntegration(t *testing.T) {
	// 1. Запускаем эхо-сервер
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	// 2. Запускаем SOCKS5 прокси
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()
	go runSimpleSocks5Proxy(proxyLn)

	// 3. Подключаемся через прокси
	client, err := net.Dial("tcp", proxyLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Handshake
	client.Write([]byte{5, 1, 0})
	buf := make([]byte, 2)
	io.ReadFull(client, buf)

	// Request CONNECT using domain name "localhost"
	echoAddr := echoLn.Addr().(*net.TCPAddr)
	domain := "localhost"
	req := []byte{5, 1, 0, 3, byte(len(domain))}
	req = append(req, []byte(domain)...)
	req = append(req, byte(echoAddr.Port>>8), byte(echoAddr.Port))
	client.Write(req)

	resp := make([]byte, 10)
	io.ReadFull(client, resp)
	if resp[1] != 0 {
		t.Fatalf("SOCKS5 connect failed: %d", resp[1])
	}

	// Data transfer
	msg := "Domain test"
	client.Write([]byte(msg))
	readBuf := make([]byte, len(msg))
	io.ReadFull(client, readBuf)
	if string(readBuf) != msg {
		t.Errorf("Expected %q, got %q", msg, string(readBuf))
	}
}

func TestSocks5ConnectFailure(t *testing.T) {
	// 1. Запускаем SOCKS5 прокси
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()
	go runSimpleSocks5Proxy(proxyLn)

	// 2. Подключаемся к прокси
	client, err := net.Dial("tcp", proxyLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Handshake
	client.Write([]byte{5, 1, 0})
	buf := make([]byte, 2)
	io.ReadFull(client, buf)

	// Попытка подключения к заведомо недоступному адресу
	req := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 1} // 127.0.0.1:1 (скорее всего закрыт)
	client.Write(req)

	resp := make([]byte, 10)
	io.ReadFull(client, resp)
	if resp[1] == 0 {
		t.Error("Expected SOCKS5 connect failure, but got success")
	}
}
