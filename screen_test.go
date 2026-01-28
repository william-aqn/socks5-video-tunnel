//go:build windows
// +build windows

package main

import (
	"testing"
)

func TestCaptureScreen(t *testing.T) {
	img, err := CaptureScreen(0, 0, 100, 100)
	if err != nil {
		t.Fatalf("CaptureScreen failed: %v", err)
	}

	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 100 {
		t.Errorf("Wrong image dimensions: %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}

	// Проверим, что хотя бы альфа-канал установлен в 255 (как в нашем кодеке)
	// Хотя GetDIBits может вернуть что угодно, но мы ожидаем 32-бит
	foundAlpha := false
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] == 255 {
			foundAlpha = true
			break
		}
	}

	if !foundAlpha {
		t.Log("Warning: No pixels with Alpha=255 found. This might be normal for some screen areas, but check if GetDIBits works correctly.")
	}
}
