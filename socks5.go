package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
)

const (
	socks5Ver          = 0x05
	socks5MethodNoAuth = 0x00
	socks5CmdConnect   = 0x01
	socks5AtypIPv4     = 0x01
	socks5AtypDomain   = 0x03
	socks5AtypIPv6     = 0x04
)

// HandleSocksHandshake выполняет рукопожатие SOCKS5 и возвращает адрес назначения.
func HandleSocksHandshake(conn net.Conn) (string, error) {
	log.Printf("SOCKS5: Start handshake from %s", conn.RemoteAddr())
	// 1. Выбор метода
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", err
	}
	if buf[0] != socks5Ver {
		return "", fmt.Errorf("invalid SOCKS version: %d", buf[0])
	}
	nmethods := int(buf[1])
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", err
	}

	// Отвечаем, что используем No Auth
	if _, err := conn.Write([]byte{socks5Ver, socks5MethodNoAuth}); err != nil {
		return "", err
	}

	// 2. Чтение запроса
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", err
	}
	if header[0] != socks5Ver {
		return "", fmt.Errorf("invalid SOCKS version in request: %d", header[0])
	}
	if header[1] != socks5CmdConnect {
		return "", fmt.Errorf("unsupported command: %d", header[1])
	}

	var addr string
	switch header[3] {
	case socks5AtypIPv4:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", err
		}
		addr = net.IP(ip).String()
	case socks5AtypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", err
		}
		domainLen := int(lenBuf[0])
		domain := make([]byte, domainLen)
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", err
		}
		addr = string(domain)
	case socks5AtypIPv6:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", err
		}
		addr = fmt.Sprintf("[%s]", net.IP(ip).String())
	default:
		return "", fmt.Errorf("unsupported address type: %d", header[3])
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)
	target := fmt.Sprintf("%s:%d", addr, port)
	log.Printf("SOCKS5: Handshake successful for %s", target)
	return target, nil
}

// SendSocksResponse отправляет ответ SOCKS5 клиенту.
func SendSocksResponse(conn net.Conn, err error) error {
	rep := byte(0x00) // Success
	if err != nil {
		rep = 0x01 // General SOCKS server failure
		log.Printf("SOCKS5: Sending error response: %v", err)
	} else {
		log.Printf("SOCKS5: Sending success response")
	}

	// Ответ: VER, REP, RSV, ATYP, BND.ADDR (4 bytes 0), BND.PORT (2 bytes 0)
	resp := []byte{socks5Ver, rep, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	_, err = conn.Write(resp)
	return err
}
