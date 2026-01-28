package main

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestSocks5Handshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addrChan := make(chan string, 1)
	errChan := make(chan error, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errChan <- err
			return
		}
		defer conn.Close()

		target, err := HandleSocksHandshake(conn)
		if err != nil {
			errChan <- err
			return
		}
		addrChan <- target
		_ = SendSocksResponse(conn, nil, nil)
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// 1. Method selection: VER=5, NMETHODS=1, METHOD=0 (No Auth)
	client.Write([]byte{5, 1, 0})
	methodResp := make([]byte, 2)
	client.Read(methodResp)
	if !bytes.Equal(methodResp, []byte{5, 0}) {
		t.Errorf("Unexpected method response: %v", methodResp)
	}

	// 2. Request: VER=5, CMD=1, RSV=0, ATYP=3 (Domain), LEN=10, "google.com", PORT=80
	req := []byte{5, 1, 0, 3, 10}
	req = append(req, []byte("google.com")...)
	req = append(req, 0, 80)
	client.Write(req)

	resp := make([]byte, 10)
	client.Read(resp)
	if resp[1] != 0 {
		t.Errorf("SOCKS request failed: %d", resp[1])
	}

	select {
	case target := <-addrChan:
		if target != "google.com:80" {
			t.Errorf("Expected google.com:80, got %s", target)
		}
	case err := <-errChan:
		t.Errorf("Server error: %v", err)
	case <-time.After(time.Second):
		t.Error("Timeout waiting for handshake")
	}
}
