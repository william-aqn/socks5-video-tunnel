package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"
)

const (
	socks5Ver          = 0x05
	socks5MethodNoAuth = 0x00
	socks5MethodNone   = 0xFF
	socks5CmdConnect   = 0x01
	socks5AtypIPv4     = 0x01
	socks5AtypDomain   = 0x03
	socks5AtypIPv6     = 0x04

	socks5RespSuccess     = 0x00
	socks5RespFailure     = 0x01
	socks5RespNotAllowed  = 0x02
	socks5RespNetUnreach  = 0x03
	socks5RespHostUnreach = 0x04
	socks5RespConnRefused = 0x05
	socks5RespTTLExpired  = 0x06
	socks5RespCmdNotSupp  = 0x07
	socks5RespAddrNotSupp = 0x08
)

// HandleSocksHandshake выполняет рукопожатие SOCKS5 и возвращает адрес назначения.
func HandleSocksHandshake(conn net.Conn) (string, error) {
	log.Printf("SOCKS5: Start handshake from %s", conn.RemoteAddr())

	// Устанавливаем таймаут на рукопожатие (10 секунд)
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})

	// 1. Выбор метода
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", fmt.Errorf("read handshake header: %v", err)
	}
	if header[0] != socks5Ver {
		return "", fmt.Errorf("invalid SOCKS version: %d", header[0])
	}
	nmethods := int(header[1])
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", fmt.Errorf("read methods: %v", err)
	}

	foundNoAuth := false
	for _, m := range methods {
		if m == socks5MethodNoAuth {
			foundNoAuth = true
			break
		}
	}

	if !foundNoAuth {
		conn.Write([]byte{socks5Ver, socks5MethodNone})
		return "", fmt.Errorf("no acceptable authentication methods")
	}

	// Отвечаем, что используем No Auth
	if _, err := conn.Write([]byte{socks5Ver, socks5MethodNoAuth}); err != nil {
		return "", fmt.Errorf("write auth response: %v", err)
	}

	// 2. Чтение запроса
	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHeader); err != nil {
		return "", fmt.Errorf("read request header: %v", err)
	}
	if reqHeader[0] != socks5Ver {
		return "", fmt.Errorf("invalid SOCKS version in request: %d", reqHeader[0])
	}
	if reqHeader[1] != socks5CmdConnect {
		return "", fmt.Errorf("unsupported command: %d", reqHeader[1])
	}

	var addr string
	switch reqHeader[3] {
	case socks5AtypIPv4:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", fmt.Errorf("read ipv4: %v", err)
		}
		addr = net.IP(ip).String()
	case socks5AtypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", fmt.Errorf("read domain length: %v", err)
		}
		domainLen := int(lenBuf[0])
		domain := make([]byte, domainLen)
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", fmt.Errorf("read domain: %v", err)
		}
		addr = string(domain)
	case socks5AtypIPv6:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", fmt.Errorf("read ipv6: %v", err)
		}
		addr = fmt.Sprintf("[%s]", net.IP(ip).String())
	default:
		return "", fmt.Errorf("unsupported address type: %d", reqHeader[3])
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", fmt.Errorf("read port: %v", err)
	}
	port := binary.BigEndian.Uint16(portBuf)
	target := fmt.Sprintf("%s:%d", addr, port)
	log.Printf("SOCKS5: Handshake successful for %s", target)
	return target, nil
}

// SendSocksResponse отправляет ответ SOCKS5 клиенту.
// Если addr != nil, используется его адрес и порт.
func SendSocksResponse(conn net.Conn, err error, boundAddr net.Addr) error {
	rep := byte(socks5RespSuccess)
	if err != nil {
		rep = socks5RespFailure
		// Пытаемся сопоставить ошибку со стандартными кодами SOCKS5
		errStr := err.Error()
		if strings.Contains(errStr, "refused") {
			rep = socks5RespConnRefused
		} else if strings.Contains(errStr, "unreachable") {
			rep = socks5RespHostUnreach
		} else if strings.Contains(errStr, "timeout") {
			rep = socks5RespTTLExpired // Или Failure
		}
		log.Printf("SOCKS5: Sending error response (rep: 0x%02x): %v", rep, err)
	} else {
		log.Printf("SOCKS5: Sending success response")
	}

	atyp := byte(socks5AtypIPv4)
	addr := make([]byte, 4)
	port := uint16(0)

	if boundAddr != nil {
		if tcpAddr, ok := boundAddr.(*net.TCPAddr); ok {
			if ip4 := tcpAddr.IP.To4(); ip4 != nil {
				atyp = socks5AtypIPv4
				addr = ip4
			} else {
				atyp = socks5AtypIPv6
				addr = tcpAddr.IP
			}
			port = uint16(tcpAddr.Port)
		}
	}

	// Ответ: VER, REP, RSV, ATYP, BND.ADDR, BND.PORT
	resp := make([]byte, 4+len(addr)+2)
	resp[0] = socks5Ver
	resp[1] = rep
	resp[2] = 0x00
	resp[3] = atyp
	copy(resp[4:], addr)
	binary.BigEndian.PutUint16(resp[4+len(addr):], port)

	_, werr := conn.Write(resp)
	return werr
}
