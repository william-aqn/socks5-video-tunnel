package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
)

const (
	width     = 640
	height    = 480
	blockSize = 4
)

// Encode записывает данные в пиксели изображения.
// Используем 1 бит на блок blockSize x blockSize пикселей для максимальной надежности.
func Encode(data []byte, margin int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Заполняем фон серым, чтобы лучше видеть границы (опционально)
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = 128
		img.Pix[i+1] = 128
		img.Pix[i+2] = 128
		img.Pix[i+3] = 255
	}

	// Рисуем контрольные точки в углах (4x4 пикселя)
	drawMarker := func(x, y int, c color.RGBA) {
		for dy := 0; dy < 4; dy++ {
			for dx := 0; dx < 4; dx++ {
				if x+dx < width && y+dy < height {
					img.SetRGBA(x+dx, y+dy, c)
				}
			}
		}
	}

	drawMarker(0, 0, color.RGBA{255, 0, 0, 255})                  // Красный - левый верхний
	drawMarker(width-4, 0, color.RGBA{0, 255, 0, 255})            // Зеленый - правый верхний
	drawMarker(0, height-4, color.RGBA{0, 0, 255, 255})           // Синий - левый нижний
	drawMarker(width-4, height-4, color.RGBA{255, 255, 255, 255}) // Белый - правый нижний

	// Длина данных (2 байта достаточно для такого метода, макс ~2400 байт)
	dataLen := len(data)
	header := []byte{
		byte(dataLen >> 8),
		byte(dataLen),
	}

	fullData := append(header, data...)

	// Превращаем данные в поток бит
	bits := make([]bool, 0, len(fullData)*8)
	for _, b := range fullData {
		for i := 7; i >= 0; i-- {
			bits = append(bits, (b>>uint(i))&1 == 1)
		}
	}

	bitIdx := 0
	for y := margin; y <= height-margin-blockSize; y += blockSize {
		for x := margin; x <= width-margin-blockSize; x += blockSize {
			// Пропускаем контрольные точки
			if (x < 8 && y < 8) || (x >= width-8 && y < 8) || (x < 8 && y >= height-8) || (x >= width-8 && y >= height-8) {
				continue
			}

			if bitIdx < len(bits) {
				c := color.RGBA{0, 0, 0, 255} // 0 = Black
				if bits[bitIdx] {
					c = color.RGBA{255, 255, 255, 255} // 1 = White
				}

				// Рисуем блок
				for dy := 0; dy < blockSize; dy++ {
					for dx := 0; dx < blockSize; dx++ {
						img.SetRGBA(x+dx, y+dy, c)
					}
				}
				bitIdx++
			} else {
				goto Done
			}
		}
	}

Done:
	if bitIdx < len(bits) {
		fmt.Printf("Codec Warning: Data truncated! Only %d bits of %d encoded.\n", bitIdx, len(bits))
	}
	return img
}

// Decode извлекает данные из изображения.
func Decode(img *image.RGBA, margin int) []byte {
	// Попробуем найти красный маркер в левом верхнем углу, чтобы компенсировать смещение
	offsetX, offsetY := 0, 0
	foundMarker := false

	// Ищем красный цвет (255, 0, 0)
	for sy := 0; sy < 20; sy++ {
		for sx := 0; sx < 20; sx++ {
			c := img.RGBAAt(sx, sy)
			if c.R > 200 && c.G < 50 && c.B < 50 {
				offsetX = sx
				offsetY = sy
				foundMarker = true
				break
			}
		}
		if foundMarker {
			break
		}
	}

	if foundMarker {
		if offsetX != 0 || offsetY != 0 {
			log.Printf("Codec: Found marker at (%d, %d)", offsetX, offsetY)
		}
	}

	var bits []bool

	for y := margin; y <= height-margin-blockSize; y += blockSize {
		for x := margin; x <= width-margin-blockSize; x += blockSize {
			// Пропускаем контрольные точки
			if (x < 8 && y < 8) || (x >= width-8 && y < 8) || (x < 8 && y >= height-8) || (x >= width-8 && y >= height-8) {
				continue
			}

			// Сэмплируем центр блока с учетом смещения
			c := img.RGBAAt(offsetX+x+blockSize/2, offsetY+y+blockSize/2)
			// Используем порог яркости
			brightness := (uint32(c.R) + uint32(c.G) + uint32(c.B)) / 3
			bits = append(bits, brightness > 128)
		}
	}

	if len(bits) < 16 {
		return nil
	}

	// Собираем байты
	var fullData []byte
	for i := 0; i+8 <= len(bits); i += 8 {
		var b byte
		for j := 0; j < 8; j++ {
			if bits[i+j] {
				b |= 1 << uint(7-j)
			}
		}
		fullData = append(fullData, b)
	}

	if len(fullData) < 2 {
		return nil
	}

	dataLen := int(fullData[0])<<8 | int(fullData[1])
	if dataLen <= 0 || dataLen > 2000 {
		return nil
	}
	if dataLen > len(fullData)-2 {
		log.Printf("Codec: dataLen %d > available %d", dataLen, len(fullData)-2)
		return nil
	}

	log.Printf("Codec: Successfully decoded %d bytes", dataLen)
	return fullData[2 : 2+dataLen]
}
