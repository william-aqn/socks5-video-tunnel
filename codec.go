package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
)

const (
	width         = 640
	height        = 480
	blockSize     = 8
	captureWidth  = 1024
	captureHeight = 1024
)

// Encode записывает данные в пиксели изображения.
// Используем 1 бит на блок blockSize x blockSize пикселей для максимальной надежности.
func Encode(data []byte, margin int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Заполняем фон черным
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0
		img.Pix[i+1] = 0
		img.Pix[i+2] = 0
		img.Pix[i+3] = 255
	}

	// Рисуем контрольные точки в углах (8x8 пикселя)
	drawMarker := func(x, y int, c color.RGBA) {
		for dy := 0; dy < 8; dy++ {
			for dx := 0; dx < 8; dx++ {
				if x+dx < width && y+dy < height {
					img.SetRGBA(x+dx, y+dy, c)
				}
			}
		}
	}

	drawMarker(0, 0, color.RGBA{255, 0, 0, 255})                  // Красный - левый верхний
	drawMarker(width-8, 0, color.RGBA{0, 255, 0, 255})            // Зеленый - правый верхний
	drawMarker(0, height-8, color.RGBA{0, 0, 255, 255})           // Синий - левый нижний
	drawMarker(width-8, height-8, color.RGBA{255, 255, 255, 255}) // Белый - правый нижний

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
			if (x < 16 && y < 16) || (x >= width-16 && y < 16) || (x < 16 && y >= height-16) || (x >= width-16 && y >= height-16) {
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
	// Ищем центры 4-х угловых маркеров
	findCenter := func(rMin, rMax, gMin, gMax, bMin, bMax int, sx, sy, sw, sh int) (float64, float64, bool) {
		var sumX, sumY float64
		var count float64
		for y := sy; y < sy+sh; y++ {
			for x := sx; x < sx+sw; x++ {
				if x < 0 || x >= img.Bounds().Dx() || y < 0 || y >= img.Bounds().Dy() {
					continue
				}
				c := img.RGBAAt(x, y)
				if int(c.R) >= rMin && int(c.R) <= rMax &&
					int(c.G) >= gMin && int(c.G) <= gMax &&
					int(c.B) >= bMin && int(c.B) <= bMax {
					sumX += float64(x)
					sumY += float64(y)
					count++
				}
			}
		}
		if count < 4 { // Минимум 4 пикселя для маркера 8x8 (с запасом на размытие)
			return 0, 0, false
		}
		return sumX / count, sumY / count, true
	}

	// Красный (левый верхний)
	rx, ry, okR := findCenter(200, 255, 0, 100, 0, 100, 0, 0, 100, 100)
	if !okR {
		return nil
	}

	// Зеленый (правый верхний) - ищем в правой половине
	gx, gy, okG := findCenter(0, 100, 200, 255, 0, 100, 300, 0, img.Bounds().Dx()-300, 100)
	// Синий (левый нижний) - ищем в нижней половине
	bx, by, okB := findCenter(0, 100, 0, 100, 200, 255, 0, 300, 100, img.Bounds().Dy()-300)
	// Белый (правый нижний) - ищем в правой нижней четверти
	wx, wy, okW := findCenter(200, 255, 200, 255, 200, 255, 300, 300, img.Bounds().Dx()-300, img.Bounds().Dy()-300)

	scaleX := 1.0
	scaleY := 1.0
	if okG {
		scaleX = (gx - rx) / 632.0 // 632 = width - 8
	}
	if okB {
		scaleY = (by - ry) / 472.0 // 472 = height - 8
	}

	offsetX := rx - 4.0*scaleX
	offsetY := ry - 4.0*scaleY

	if okG && okB && okW {
		log.Printf("Codec: Calibration: ScaleX=%.3f, ScaleY=%.3f, Offset=(%.1f, %.1f)", scaleX, scaleY, offsetX, offsetY)
		_ = wx
		_ = wy
		_ = gy
		_ = bx
	}

	var bits []bool
	for y := margin; y <= height-margin-blockSize; y += blockSize {
		for x := margin; x <= width-margin-blockSize; x += blockSize {
			// Пропускаем контрольные точки
			if (x < 16 && y < 16) || (x >= width-16 && y < 16) || (x < 16 && y >= height-16) || (x >= width-16 && y >= height-16) {
				continue
			}

			// Сэмплируем центр блока с учетом смещения и масштаба
			px := int(offsetX + float64(x)*scaleX + float64(blockSize)/2.0*scaleX)
			py := int(offsetY + float64(y)*scaleY + float64(blockSize)/2.0*scaleY)

			if px < 0 || px >= img.Bounds().Dx() || py < 0 || py >= img.Bounds().Dy() {
				continue
			}

			c := img.RGBAAt(px, py)
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
		if dataLen != 0 {
			log.Printf("Codec: dataLen %d out of range (first bits: %08b %08b)", dataLen, fullData[0], fullData[1])
		}
		return nil
	}
	if dataLen > len(fullData)-2 {
		log.Printf("Codec: dataLen %d > available %d", dataLen, len(fullData)-2)
		return nil
	}

	log.Printf("Codec: Successfully decoded %d bytes", dataLen)
	return fullData[2 : 2+dataLen]
}
