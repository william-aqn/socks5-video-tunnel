package main

import (
	"bytes"
	"testing"
)

func TestEncodeDecode(t *testing.T) {
	data := []byte("Hello, video stream! This is a test message to see if encoding and decoding works correctly.")
	margin := 10
	img := Encode(data, margin)

	if img.Bounds().Dx() != width || img.Bounds().Dy() != height {
		t.Errorf("Wrong image dimensions: %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}

	decoded := Decode(img, margin)

	if !bytes.Equal(data, decoded) {
		t.Errorf("Decoded data does not match original. Got %s, want %s", string(decoded), string(data))
	}
}

func TestLargeData(t *testing.T) {
	margin := 0
	// 4 маркера по 2x2 пикселя = 16 пикселей.
	size := (width*height-16)*3 - 4
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}

	img := Encode(data, margin)
	decoded := Decode(img, margin)

	if !bytes.Equal(data, decoded) {
		t.Errorf("Large data decode failed")
	}
}

func TestMarkers(t *testing.T) {
	data := []byte("test")
	margin := 0
	img := Encode(data, margin)

	// Проверяем цвета маркеров
	if c := img.RGBAAt(0, 0); c.R != 255 || c.G != 0 || c.B != 0 {
		t.Errorf("Top-left marker should be red, got %v", c)
	}
	if c := img.RGBAAt(width-1, 0); c.R != 0 || c.G != 255 || c.B != 0 {
		t.Errorf("Top-right marker should be green, got %v", c)
	}
	if c := img.RGBAAt(0, height-1); c.R != 0 || c.G != 0 || c.B != 255 {
		t.Errorf("Bottom-left marker should be blue, got %v", c)
	}
	if c := img.RGBAAt(width-1, height-1); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("Bottom-right marker should be white, got %v", c)
	}
}
