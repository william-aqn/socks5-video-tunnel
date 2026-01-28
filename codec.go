package main

import (
	"fmt"
	"image"
	"image/color"
)

const (
	width  = 640
	height = 480
)

// Encode записывает данные в пиксели изображения.
// Каждый пиксель RGB может хранить 3 байта.
func Encode(data []byte, margin int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Рисуем контрольные точки в углах (2x2 пикселя)
	drawMarker := func(x, y int, c color.RGBA) {
		for dy := 0; dy < 2; dy++ {
			for dx := 0; dx < 2; dx++ {
				if x+dx < width && y+dy < height {
					img.SetRGBA(x+dx, y+dy, c)
				}
			}
		}
	}

	drawMarker(0, 0, color.RGBA{255, 0, 0, 255})                  // Красный - левый верхний
	drawMarker(width-2, 0, color.RGBA{0, 255, 0, 255})            // Зеленый - правый верхний
	drawMarker(0, height-2, color.RGBA{0, 0, 255, 255})           // Синий - левый нижний
	drawMarker(width-2, height-2, color.RGBA{255, 255, 255, 255}) // Белый - правый нижний

	// Первые 4 байта зарезервируем для длины данных
	dataLen := len(data)
	header := []byte{
		byte(dataLen >> 24),
		byte(dataLen >> 16),
		byte(dataLen >> 8),
		byte(dataLen),
	}

	fullData := append(header, data...)
	fmt.Printf("Codec: Encoding %d bytes of data\n", len(data))

	idx := 0
	for y := margin; y < height-margin; y++ {
		for x := margin; x < width-margin; x++ {
			// Пропускаем контрольные точки, если они попадают в область данных
			if (x < 2 && y < 2) || (x >= width-2 && y < 2) || (x < 2 && y >= height-2) || (x >= width-2 && y >= height-2) {
				continue
			}

			var r, g, b byte
			if idx < len(fullData) {
				r = fullData[idx]
				idx++
			}
			if idx < len(fullData) {
				g = fullData[idx]
				idx++
			}
			if idx < len(fullData) {
				b = fullData[idx]
				idx++
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
			if idx >= len(fullData) {
				goto Done
			}
		}
	}

Done:
	return img
}

// Decode извлекает данные из изображения.
func Decode(img *image.RGBA, margin int) []byte {
	var fullData []byte

	for y := margin; y < height-margin; y++ {
		for x := margin; x < width-margin; x++ {
			// Пропускаем контрольные точки
			if (x < 2 && y < 2) || (x >= width-2 && y < 2) || (x < 2 && y >= height-2) || (x >= width-2 && y >= height-2) {
				continue
			}

			c := img.RGBAAt(x, y)
			fullData = append(fullData, c.R, c.G, c.B)
		}
	}

	if len(fullData) < 4 {
		return nil
	}

	dataLen := int(fullData[0])<<24 | int(fullData[1])<<16 | int(fullData[2])<<8 | int(fullData[3])
	if dataLen <= 0 || dataLen > len(fullData)-4 {
		return nil
	}

	fmt.Printf("Codec: Decoded %d bytes of data\n", dataLen)
	return fullData[4 : 4+dataLen]
}
