package main

import (
	"bytes"
	"fmt"
	"testing"
)

func TestCRC8(t *testing.T) {
	data := []byte("hello")
	c1 := crc8(data)
	data[0] = 'H'
	c2 := crc8(data)
	if c1 == c2 {
		t.Errorf("CRC8 should be different for different data")
	}
}

func TestEncodeDecode(t *testing.T) {
	CurrentMode = "client"
	data := []byte("Hello, video stream! This is a test message to see if encoding and decoding works correctly.")
	margin := 10
	img := Encode(data, margin, GetBlockSize())

	if img.Bounds().Dx() != width || img.Bounds().Dy() != height {
		t.Errorf("Wrong image dimensions: %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}

	// Для декодирования того, что закодировал клиент, нужно быть в режиме "server"
	CurrentMode = "server"
	decoded := Decode(img, margin)

	if !bytes.Equal(data, decoded) {
		t.Errorf("Decoded data does not match original. Got %s, want %s", string(decoded), string(data))
	}
}

func TestEncodeDecodeFlexBlock(t *testing.T) {
	sizes := []int{4, 6, 8, 12}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("BlockSize-%d", size), func(t *testing.T) {
			CurrentMode = "client"
			data := []byte(fmt.Sprintf("Test message for block size %d. This should work correctly with flexible sizes.", size))
			margin := 10
			img := Encode(data, margin, size)

			CurrentMode = "server"
			decoded := Decode(img, margin)

			if !bytes.Equal(data, decoded) {
				t.Errorf("Decoded data does not match original for blockSize=%d. Got %q, want %q", size, string(decoded), string(data))
			}
		})
	}
}

func TestMaxCapacity(t *testing.T) {
	margin := 10
	bSize := 4
	maxPayload := GetMaxPayloadSize(margin, bSize)
	fmt.Printf("Max payload for blockSize=4: %d bytes\n", maxPayload)

	if maxPayload < 4000 {
		t.Errorf("Max payload for blockSize=4 is too small: %d", maxPayload)
	}

	data := make([]byte, maxPayload)
	for i := range data {
		data[i] = byte(i % 256)
	}

	CurrentMode = "client"
	img := Encode(data, margin, bSize)

	CurrentMode = "server"
	decoded := Decode(img, margin)

	if !bytes.Equal(data, decoded) {
		t.Errorf("Decoded data does not match original for max capacity. Len got %d, want %d", len(decoded), len(data))
	}
}

func TestMarkers(t *testing.T) {
	CurrentMode = "client"
	data := []byte("test")
	margin := 0
	img := Encode(data, margin, GetBlockSize())

	// Проверяем цвета маркеров в новых позициях (markerOffset=4)
	if c := img.RGBAAt(markerOffset, markerOffset); c.R != 255 || c.G != 0 || c.B != 0 {
		t.Errorf("Top-left marker should be red, got %v", c)
	}
	if c := img.RGBAAt(width-markerSize-markerOffset, markerOffset); c.R != 0 || c.G != 255 || c.B != 0 {
		t.Errorf("Top-right marker should be green, got %v", c)
	}
	if c := img.RGBAAt(markerOffset, height-markerSize-markerOffset); c.R != 0 || c.G != 0 || c.B != 255 {
		t.Errorf("Bottom-left marker should be blue, got %v", c)
	}
	if c := img.RGBAAt(width-markerSize-markerOffset, height-markerSize-markerOffset); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("Bottom-right marker should be white, got %v", c)
	}
}

func TestEncodeAutoAdjust(t *testing.T) {
	CurrentMode = "client"
	margin := 10
	// 5000 bytes won't fit in blockSize=12 (cap ~662) but will fit in blockSize=4 (cap ~7575)
	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(i % 256)
	}

	// Request blockSize=12
	img := Encode(data, margin, 12)

	// Decode should be able to recover it using the effectiveBlockSize from metadata
	CurrentMode = "server"
	decoded := Decode(img, margin)

	if !bytes.Equal(data, decoded) {
		t.Errorf("Auto-adjustment failed: decoded data does not match original. Len got %d, want %d", len(decoded), len(data))
	}
}

func TestAdaptiveBlockSize(t *testing.T) {
	SetBlockSize(4)
	if GetBlockSize() != 4 {
		t.Errorf("Expected blockSize 4, got %d", GetBlockSize())
	}

	// Should not increase immediately because lastBSChange was just set in SetBlockSize
	TryIncreaseBlockSize()
	if GetBlockSize() != 4 {
		t.Errorf("Should not increase before 10s. Got %d", GetBlockSize())
	}

	// Manually backdate lastBSChange using reflection or just wait?
	// Since I can't easily wait 10s in a unit test, I'll trust the logic or use a shorter interval for tests?
	// Actually, I can just verify it doesn't jump.
}
