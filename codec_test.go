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
	img := Encode(data, margin)

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
			oldBlockSize := blockSize
			blockSize = size
			defer func() { blockSize = oldBlockSize }()

			CurrentMode = "client"
			data := []byte(fmt.Sprintf("Test message for block size %d. This should work correctly with flexible sizes.", size))
			margin := 10
			img := Encode(data, margin)

			CurrentMode = "server"
			decoded := Decode(img, margin)

			if !bytes.Equal(data, decoded) {
				t.Errorf("Decoded data does not match original for blockSize=%d. Got %q, want %q", size, string(decoded), string(data))
			}
		})
	}
}

func TestMarkers(t *testing.T) {
	CurrentMode = "client"
	data := []byte("test")
	margin := 0
	img := Encode(data, margin)

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
