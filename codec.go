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
	markerSize    = 8
	markerOffset  = 4
)

type ColorRange struct {
	rMin, rMax, gMin, gMax, bMin, bMax int
}

type MarkerRanges struct {
	TL, TR, BL, BR ColorRange
}

type MarkerColors struct {
	TL, TR, BL, BR color.RGBA
}

var (
	ClientMarkers = MarkerColors{
		TL: color.RGBA{255, 0, 0, 255},     // Red
		TR: color.RGBA{0, 255, 0, 255},     // Green
		BL: color.RGBA{0, 0, 255, 255},     // Blue
		BR: color.RGBA{255, 255, 255, 255}, // White
	}
	ServerMarkers = MarkerColors{
		TL: color.RGBA{0, 255, 255, 255}, // Cyan
		TR: color.RGBA{255, 0, 255, 255}, // Magenta
		BL: color.RGBA{255, 255, 0, 255}, // Yellow
		BR: color.RGBA{255, 165, 0, 255}, // Orange
	}
)

var (
	ClientRanges = MarkerRanges{
		TL: ColorRange{160, 255, 0, 120, 0, 120},     // Red
		TR: ColorRange{0, 120, 160, 255, 0, 120},     // Green
		BL: ColorRange{0, 120, 0, 120, 160, 255},     // Blue
		BR: ColorRange{160, 255, 160, 255, 160, 255}, // White
	}
	ServerRanges = MarkerRanges{
		TL: ColorRange{0, 120, 160, 255, 160, 255}, // Cyan
		TR: ColorRange{160, 255, 0, 120, 160, 255}, // Magenta
		BL: ColorRange{160, 255, 160, 255, 0, 120}, // Yellow
		BR: ColorRange{160, 255, 120, 220, 0, 120}, // Orange
	}
)

var CurrentMode string // "client" or "server"

func matchRange(r, g, b uint8, cr ColorRange) bool {
	return int(r) >= cr.rMin && int(r) <= cr.rMax &&
		int(g) >= cr.gMin && int(g) <= cr.gMax &&
		int(b) >= cr.bMin && int(b) <= cr.bMax
}

// FindMarkers ищет контрольные точки в изображении и возвращает координаты левого верхнего угла области захвата
func FindMarkers(img *image.RGBA, mode string) (int, int, bool) {
	var ranges MarkerRanges
	if mode == "server" {
		ranges = ClientRanges
	} else {
		ranges = ServerRanges
	}

	distX := width - markerSize - 2*markerOffset
	distY := height - markerSize - 2*markerOffset

	// Сканируем изображение в поисках TL маркера
	// Используем шаг 2 для надежности при поиске маленького маркера (8x8)
	for y := 0; y < img.Rect.Dy()-height+markerSize; y += 2 {
		for x := 0; x < img.Rect.Dx()-width+markerSize; x += 2 {
			c := img.RGBAAt(x, y)
			if matchRange(c.R, c.G, c.B, ranges.TL) {
				// Проверяем остальные маркеры на ожидаемых расстояниях
				if x+distX+markerSize < img.Rect.Dx() && y+distY+markerSize < img.Rect.Dy() {
					cTR := img.RGBAAt(x+distX, y)
					cBL := img.RGBAAt(x, y+distY)
					cBR := img.RGBAAt(x+distX, y+distY)

					if matchRange(cTR.R, cTR.G, cTR.B, ranges.TR) &&
						matchRange(cBL.R, cBL.G, cBL.B, ranges.BL) &&
						matchRange(cBR.R, cBR.G, cBR.B, ranges.BR) {
						return x - markerOffset, y - markerOffset, true
					}
				}
			}
		}
	}
	return 0, 0, false
}

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

	// Рисуем контрольные точки в углах (8x8 пикселя) с отступом
	drawMarker := func(x, y int, c color.RGBA) {
		for dy := 0; dy < markerSize; dy++ {
			for dx := 0; dx < markerSize; dx++ {
				if x+dx < width && y+dy < height {
					img.SetRGBA(x+dx, y+dy, c)
				}
			}
		}
	}

	markers := ClientMarkers
	if CurrentMode == "server" {
		markers = ServerMarkers
	}
	log.Printf("Codec: Encoding %d bytes in %s mode (TL Color: %+v)", len(data), CurrentMode, markers.TL)

	drawMarker(markerOffset, markerOffset, markers.TL)
	drawMarker(width-markerSize-markerOffset, markerOffset, markers.TR)
	drawMarker(markerOffset, height-markerSize-markerOffset, markers.BL)
	drawMarker(width-markerSize-markerOffset, height-markerSize-markerOffset, markers.BR)

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
	findCenter := func(cr ColorRange, sx, sy, sw, sh int) (float64, float64, bool) {
		var sumX, sumY float64
		var count float64
		for y := sy; y < sy+sh; y++ {
			for x := sx; x < sx+sw; x++ {
				if x < 0 || x >= img.Bounds().Dx() || y < 0 || y >= img.Bounds().Dy() {
					continue
				}
				c := img.RGBAAt(x, y)
				if int(c.R) >= cr.rMin && int(c.R) <= cr.rMax &&
					int(c.G) >= cr.gMin && int(c.G) <= cr.gMax &&
					int(c.B) >= cr.bMin && int(c.B) <= cr.bMax {
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

	ranges := ServerRanges
	if CurrentMode == "server" {
		ranges = ClientRanges
	}

	// TL (левый верхний)
	rx, ry, okR := findCenter(ranges.TL, 0, 0, 150, 150)
	if !okR {
		return nil
	}

	// TR (правый верхний) - ищем в правой половине
	gx, _, okG := findCenter(ranges.TR, 300, 0, img.Bounds().Dx()-300, 150)
	if !okG {
		// log.Printf("Codec: TR marker not found")
	}

	// BL (левый нижний) - ищем в нижней половине
	_, by, okB := findCenter(ranges.BL, 0, 300, 150, img.Bounds().Dy()-300)
	if !okB {
		// log.Printf("Codec: BL marker not found")
	}

	// BR (правый нижний) - ищем в правой нижней четверти
	_, _, okW := findCenter(ranges.BR, 300, 300, img.Bounds().Dx()-300, img.Bounds().Dy()-300)
	if !okW {
		// log.Printf("Codec: BR marker not found")
	}

	scaleX := 1.0
	scaleY := 1.0
	if okG {
		scaleX = (gx - rx) / 624.0 // width - markerSize - 2*markerOffset
	}
	if okB {
		scaleY = (by - ry) / 464.0 // height - markerSize - 2*markerOffset
	}

	offsetX := rx - 8.0*scaleX // markerOffset + markerSize/2
	offsetY := ry - 8.0*scaleY

	if okG && okB && okW {
		// log.Printf("Codec: Calibration: ScaleX=%.3f, ScaleY=%.3f, Offset=(%.1f, %.1f)", scaleX, scaleY, offsetX, offsetY)
	}

	var bits []bool
	for y := margin; y <= height-margin-blockSize; y += blockSize {
		for x := margin; x <= width-margin-blockSize; x += blockSize {
			// Пропускаем контрольные точки
			if (x < 16 && y < 16) || (x >= width-16 && y < 16) || (x < 16 && y >= height-16) || (x >= width-16 && y >= height-16) {
				continue
			}

			// Сэмплируем блок 3x3 в центре для устойчивости к шуму
			var sumBrightness uint32
			points := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					px := int(offsetX + (float64(x)+float64(blockSize)/2.0+float64(dx))*scaleX)
					py := int(offsetY + (float64(y)+float64(blockSize)/2.0+float64(dy))*scaleY)

					if px >= 0 && px < img.Bounds().Dx() && py >= 0 && py < img.Bounds().Dy() {
						c := img.RGBAAt(px, py)
						sumBrightness += (uint32(c.R) + uint32(c.G) + uint32(c.B)) / 3
						points++
					}
				}
			}

			if points > 0 {
				bits = append(bits, (sumBrightness/uint32(points)) > 128)
			} else {
				bits = append(bits, false)
			}
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
		if dataLen != 0 && dataLen < 5000 {
			log.Printf("Codec: potential data detected but dataLen %d invalid. First bytes: %02x %02x", dataLen, fullData[0], fullData[1])
		}
		return nil
	}
	if dataLen > len(fullData)-2 {
		// log.Printf("Codec: dataLen %d > available %d", dataLen, len(fullData)-2)
		return nil
	}

	log.Printf("Codec: Successfully decoded %d bytes: %v", dataLen, fullData[2:2+dataLen])
	return fullData[2 : 2+dataLen]
}
