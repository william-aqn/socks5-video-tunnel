package main

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"log"
	"sync"
	"time"
)

const (
	width         = 640
	height        = 480
	captureWidth  = 1024
	captureHeight = 1024
	markerSize    = 8
	markerOffset  = 4
)

var (
	blockSize    = 4
	lastBSChange = time.Now()
	bsMu         sync.Mutex
)

func GetBlockSize() int {
	bsMu.Lock()
	defer bsMu.Unlock()
	return blockSize
}

func SetBlockSize(s int) {
	bsMu.Lock()
	defer bsMu.Unlock()
	blockSize = s
	lastBSChange = time.Now()
}

func TryIncreaseBlockSize() {
	bsMu.Lock()
	defer bsMu.Unlock()
	if time.Since(lastBSChange) > 10*time.Second && blockSize < 12 {
		blockSize += 2
		lastBSChange = time.Now()
		log.Printf("Adaptive: Global blockSize increased to %d", blockSize)
	}
}

func TryDecreaseBlockSize() {
	bsMu.Lock()
	defer bsMu.Unlock()
	if time.Since(lastBSChange) > 10*time.Second && blockSize > 4 {
		blockSize -= 1
		lastBSChange = time.Now()
		log.Printf("Adaptive: Global blockSize decreased to %d", blockSize)
	}
}

func calculateMaxBits(margin int, bSize int) int {
	if bSize < 1 {
		bSize = 4
	}
	totalBits := 0
	for y := margin; y <= height-margin-bSize; y += bSize {
		for x := margin; x <= width-margin-bSize; x += bSize {
			if (x < 16 && y < 16) || (x >= width-16 && y < 16) || (x < 16 && y >= height-16) || (x >= width-16 && y >= height-16) {
				continue
			}
			if x < 6 || y < 6 {
				continue
			}
			totalBits += bitsPerBlock
		}
	}
	return totalBits
}

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
		TL: ColorRange{130, 255, 0, 140, 0, 140},     // Red
		TR: ColorRange{0, 140, 130, 255, 0, 140},     // Green
		BL: ColorRange{0, 140, 0, 140, 130, 255},     // Blue
		BR: ColorRange{130, 255, 130, 255, 130, 255}, // White
	}
	ServerRanges = MarkerRanges{
		TL: ColorRange{0, 140, 130, 255, 130, 255}, // Cyan
		TR: ColorRange{130, 255, 0, 140, 130, 255}, // Magenta
		BL: ColorRange{130, 255, 130, 255, 0, 140}, // Yellow
		BR: ColorRange{130, 255, 100, 230, 0, 140}, // Orange
	}
)

var DataPalette = []color.RGBA{
	{0, 0, 0, 255},       // 0
	{255, 0, 0, 255},     // 1
	{0, 255, 0, 255},     // 2
	{0, 0, 255, 255},     // 3
	{255, 255, 0, 255},   // 4
	{255, 0, 255, 255},   // 5
	{0, 255, 255, 255},   // 6
	{255, 255, 255, 255}, // 7
	{128, 0, 0, 255},     // 8
	{0, 128, 0, 255},     // 9
	{0, 0, 128, 255},     // 10
	{128, 128, 0, 255},   // 11
	{128, 0, 128, 255},   // 12
	{0, 128, 128, 255},   // 13
	{128, 128, 128, 255}, // 14
	{255, 128, 0, 255},   // 15
}

const bitsPerBlock = 4

var CurrentMode string // "client" or "server"

func matchRange(r, g, b uint8, cr ColorRange) bool {
	return int(r) >= cr.rMin && int(r) <= cr.rMax &&
		int(g) >= cr.gMin && int(g) <= cr.gMax &&
		int(b) >= cr.bMin && int(b) <= cr.bMax
}

// GetMaxPayloadSize возвращает максимальное количество байт, которое можно закодировать в одном кадре.
func GetMaxPayloadSize(margin int, bSize int) int {
	totalBits := calculateMaxBits(margin, bSize)
	totalBytes := totalBits / 8
	// Мы используем RS(255, 223), то есть каждые 255 байт на экране содержат 223 байта данных.
	numRSBlocks := totalBytes / 255
	if numRSBlocks == 0 {
		return 0
	}
	// В каждом блоке 223 байта. Оверхед на весь пакет (header + CRC32) = 7 байт.
	maxPayload := numRSBlocks*223 - 7
	if maxPayload < 0 {
		return 0
	}
	return maxPayload
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

	checkMarker := func(x, y int, cr ColorRange) bool {
		count := 0
		for dy := 0; dy < markerSize; dy++ {
			for dx := 0; dx < markerSize; dx++ {
				if x+dx >= 0 && x+dx < img.Rect.Dx() && y+dy >= 0 && y+dy < img.Rect.Dy() {
					c := img.RGBAAt(x+dx, y+dy)
					if matchRange(c.R, c.G, c.B, cr) {
						count++
					}
				}
			}
		}
		return count > (markerSize * markerSize / 4) // Хотя бы четверть пикселей совпала (устойчивость к шуму)
	}

	// Сканируем изображение в поисках TL маркера
	for y := 0; y < img.Rect.Dy()-height+markerSize; y++ {
		for x := 0; x < img.Rect.Dx()-width+markerSize; x++ {
			c := img.RGBAAt(x, y)
			// Быстрая проверка первого пикселя (или любого в маркере)
			if matchRange(c.R, c.G, c.B, ranges.TL) {
				// Проверяем TL маркер целиком
				if checkMarker(x, y, ranges.TL) {
					// Проверяем остальные маркеры на ожидаемых расстояниях (с небольшим допуском +-2 пикселя)
					foundOthers := true
					for _, offset := range []struct {
						dx, dy int
						r      ColorRange
					}{
						{distX, 0, ranges.TR},
						{0, distY, ranges.BL},
						{distX, distY, ranges.BR},
					} {
						ok := false
						for dy := -2; dy <= 2; dy++ {
							for dx := -2; dx <= 2; dx++ {
								if checkMarker(x+offset.dx+dx, y+offset.dy+dy, offset.r) {
									ok = true
									break
								}
							}
							if ok {
								break
							}
						}
						if !ok {
							foundOthers = false
							break
						}
					}

					if foundOthers {
						return x - markerOffset, y - markerOffset, true
					}
				}
			}
		}
	}
	return 0, 0, false
}

// --- Reed-Solomon и GF(256) математика ---

var (
	gfExp [512]byte
	gfLog [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D // QR-code polynomial
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

func gfDiv(a, b byte) byte {
	if b == 0 {
		panic("gfDiv: division by zero")
	}
	if a == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])-int(gfLog[b])+255]
}

func gfPolyEval(p []byte, x byte) byte {
	res := byte(0)
	for _, c := range p {
		res = gfMul(res, x) ^ c
	}
	return res
}

func gfPolyMul(p, q []byte) []byte {
	r := make([]byte, len(p)+len(q)-1)
	for i, pv := range p {
		for j, qv := range q {
			r[i+j] ^= gfMul(pv, qv)
		}
	}
	return r
}

func rsGenerator(nsym int) []byte {
	g := []byte{1}
	for i := 0; i < nsym; i++ {
		g = gfPolyMul(g, []byte{1, gfExp[i]})
	}
	return g
}

func rsEncode(data []byte, nsym int) []byte {
	blockDataLen := 255 - nsym
	var res []byte
	gen := rsGenerator(nsym)

	for i := 0; i < len(data); i += blockDataLen {
		end := i + blockDataLen
		if end > len(data) {
			end = len(data)
		}
		chunk := make([]byte, blockDataLen)
		copy(chunk, data[i:end])

		block := make([]byte, 255)
		copy(block, chunk)

		// Систематическое кодирование: остаток от деления (data * x^nsym) на gen
		for j := 0; j < blockDataLen; j++ {
			feedback := block[j]
			if feedback != 0 {
				for k := 1; k < len(gen); k++ {
					block[j+k] ^= gfMul(gen[k], feedback)
				}
			}
		}
		copy(block, chunk)
		res = append(res, block...)
	}
	return res
}

func rsDecode(data []byte, nsym int) ([]byte, bool) {
	if len(data) < 255 {
		return nil, false
	}
	blockDataLen := 255 - nsym
	var res []byte
	allOk := true

	for i := 0; i+255 <= len(data); i += 255 {
		block := make([]byte, 255)
		copy(block, data[i:i+255])

		// 1. Синдромы S[j] = block(alpha^j)
		s := make([]byte, nsym)
		anyError := false
		for j := 0; j < nsym; j++ {
			s[nsym-1-j] = gfPolyEval(block, gfExp[j])
			if s[nsym-1-j] != 0 {
				anyError = true
			}
		}

		if !anyError {
			res = append(res, block[:blockDataLen]...)
			continue
		}

		// 2. Алгоритм Берлекампа-Мэсси
		lambda := []byte{1}
		b := []byte{1}
		for j := 0; j < nsym; j++ {
			b = append(b, 0)
			delta := s[nsym-1-j]
			for k := 1; k < len(lambda); k++ {
				delta ^= gfMul(lambda[len(lambda)-1-k], s[nsym-1-j+k])
			}

			if delta != 0 {
				if len(b) > len(lambda) {
					oldLambda := lambda
					lambda = gfPolyAdd(lambda, gfPolyScale(b, delta))
					b = gfPolyScale(oldLambda, gfDiv(1, delta))
				} else {
					lambda = gfPolyAdd(lambda, gfPolyScale(b, delta))
				}
			}
		}

		// 3. Поиск корней (Chien search)
		var errPos []int
		for j := 0; j < 255; j++ {
			if gfPolyEval(lambda, gfExp[255-j]) == 0 {
				errPos = append(errPos, 254-j)
			}
		}

		if len(errPos) != len(lambda)-1 {
			res = append(res, block[:blockDataLen]...)
			allOk = false
			continue
		}

		// 4. Алгоритм Форни
		omega := gfPolyMul(s, lambda)
		omega = omega[len(omega)-nsym:]

		lambdaDeriv := make([]byte, len(lambda)-1)
		for j := 1; j < len(lambda); j++ {
			if j%2 != 0 {
				lambdaDeriv[len(lambdaDeriv)-j] = lambda[len(lambda)-1-j]
			}
		}

		for _, pos := range errPos {
			xInv := gfExp[255-pos]
			y := gfPolyEval(omega, xInv)
			z := gfPolyEval(lambdaDeriv, xInv)
			errVal := gfDiv(y, gfMul(xInv, z))
			block[pos] ^= errVal
		}

		res = append(res, block[:blockDataLen]...)
	}
	return res, allOk
}

func gfPolyAdd(p, q []byte) []byte {
	size := len(p)
	if len(q) > size {
		size = len(q)
	}
	res := make([]byte, size)
	for i := 0; i < len(p); i++ {
		res[size-len(p)+i] ^= p[i]
	}
	for i := 0; i < len(q); i++ {
		res[size-len(q)+i] ^= q[i]
	}
	return res
}

func gfPolyScale(p []byte, s byte) []byte {
	res := make([]byte, len(p))
	for i, v := range p {
		res[i] = gfMul(v, s)
	}
	return res
}

func crc8(data []byte) byte {
	var crc byte
	for _, b := range data {
		crc ^= b
		for i := 0; i < 8; i++ {
			if (crc & 0x80) != 0 {
				crc = (crc << 1) ^ 0x07 // Polynomial 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Encode записывает данные в пиксели изображения.
func Encode(data []byte, margin int, bSize int) *image.RGBA {
	if bSize < 1 {
		bSize = 4
	}

	// Подготовка данных с Reed-Solomon (nsym=32)
	dataLen := len(data)
	// Блок: [Версия 0x04][Длина 2][Данные][CRC32 4]
	header := []byte{
		0x04,
		byte(dataLen >> 8),
		byte(dataLen),
	}
	block := append(header, data...)
	c32 := crc32.ChecksumIEEE(block)
	block = append(block, byte(c32>>24), byte(c32>>16), byte(c32>>8), byte(c32))

	// Добавляем RS-коды (32 байта)
	fullData := rsEncode(block, 32)

	// Маскирование (XOR с шахматным паттерном) для улучшения JPEG-сжатия
	for i := 0; i < len(fullData); i++ {
		fullData[i] ^= 0xAA // Простая маска
	}

	// Превращаем данные в поток бит
	bits := make([]bool, 0, len(fullData)*8)
	for _, b := range fullData {
		for i := 7; i >= 0; i-- {
			bits = append(bits, (b>>uint(i))&1 == 1)
		}
	}

	// Автоматически подбираем bSize, если данные не влезают
	originalBSize := bSize
	for bSize > 2 {
		if len(bits) <= calculateMaxBits(margin, bSize) {
			break
		}
		bSize--
	}
	if bSize != originalBSize {
		log.Printf("Encode: Auto-adjusted blockSize from %d to %d to fit %d bits", originalBSize, bSize, len(bits))
	}

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

	drawMarker(markerOffset, markerOffset, markers.TL)
	drawMarker(width-markerSize-markerOffset, markerOffset, markers.TR)
	drawMarker(markerOffset, height-markerSize-markerOffset, markers.BL)
	drawMarker(width-markerSize-markerOffset, height-markerSize-markerOffset, markers.BR)

	// Кодируем текущий bSize в метаданных (рядом с TL маркером)
	// bSize 2..15 кодируется индексом палитры 0..13
	metaColorIdx := bSize - 2
	if metaColorIdx < 0 {
		metaColorIdx = 0
	}
	if metaColorIdx > 15 {
		metaColorIdx = 15
	}
	cMeta := DataPalette[metaColorIdx]
	for dy := 0; dy < 4; dy++ {
		for dx := 0; dx < 4; dx++ {
			img.SetRGBA(16+dx, 4+dy, cMeta)
		}
	}

	// Рисуем Timing Patterns (пунктирные линии для синхронизации)
	// Горизонтальная линия сверху (y=1)
	for x := 64; x < width-64; x += 8 {
		c := color.RGBA{255, 255, 255, 255}
		if (x/8)%2 == 0 {
			c = color.RGBA{0, 0, 0, 255}
		}
		for dy := 0; dy < 2; dy++ {
			for dx := 0; dx < 4; dx++ {
				img.SetRGBA(x+dx, 1+dy, c)
			}
		}
	}
	// Вертикальная линия слева (x=1)
	for y := 64; y < height-64; y += 8 {
		c := color.RGBA{255, 255, 255, 255}
		if (y/8)%2 == 0 {
			c = color.RGBA{0, 0, 0, 255}
		}
		for dy := 0; dy < 4; dy++ {
			for dx := 0; dx < 2; dx++ {
				img.SetRGBA(1+dx, y+dy, c)
			}
		}
	}

	bitIdx := 0
	for y := margin; y <= height-margin-bSize; y += bSize {
		for x := margin; x <= width-margin-bSize; x += bSize {
			// Пропускаем контрольные точки (зона 16x16 для стабильности поиска)
			if (x < 16 && y < 16) || (x >= width-16 && y < 16) || (x < 16 && y >= height-16) || (x >= width-16 && y >= height-16) {
				continue
			}
			// Пропускаем Timing Patterns (они на краях x=1, y=1)
			if x < 6 || y < 6 {
				continue
			}

			if bitIdx < len(bits) {
				val := 0
				for i := 0; i < bitsPerBlock; i++ {
					if bitIdx+i < len(bits) {
						if bits[bitIdx+i] {
							val |= 1 << uint(bitsPerBlock-1-i)
						}
					}
				}
				c := DataPalette[val]

				// Рисуем блок
				for dy := 0; dy < bSize; dy++ {
					for dx := 0; dx < bSize; dx++ {
						img.SetRGBA(x+dx, y+dy, c)
					}
				}
				bitIdx += bitsPerBlock
			} else {
				// Просто пропускаем
			}
		}
	}

	if bitIdx < len(bits) {
		fmt.Printf("Codec Warning: Data truncated! Only %d bits of %d encoded.\n", bitIdx, len(bits))
	}
	return img
}

// Decode извлекает данные из изображения.
func Decode(img *image.RGBA, margin int) []byte {
	offsetX, offsetY, ok := FindMarkers(img, CurrentMode)
	if !ok {
		return nil
	}

	// Читаем blockSize из метаданных (16, 4)
	var sumRM, sumGM, sumBM uint32
	for dy := 0; dy < 4; dy++ {
		for dx := 0; dx < 4; dx++ {
			c := img.RGBAAt(offsetX+16+dx, offsetY+4+dy)
			sumRM += uint32(c.R)
			sumGM += uint32(c.G)
			sumBM += uint32(c.B)
		}
	}
	avgColorM := color.RGBA{uint8(sumRM / 16), uint8(sumGM / 16), uint8(sumBM / 16), 255}
	minDistM := 1000000
	bestIdxM := 0
	for i, pc := range DataPalette {
		dr := int(avgColorM.R) - int(pc.R)
		dg := int(avgColorM.G) - int(pc.G)
		db := int(avgColorM.B) - int(pc.B)
		dist := dr*dr + dg*dg + db*db
		if dist < minDistM {
			minDistM = dist
			bestIdxM = i
		}
	}
	effectiveBlockSize := bestIdxM + 2
	if effectiveBlockSize < 2 || effectiveBlockSize > 16 {
		effectiveBlockSize = GetBlockSize() // Fallback to global
	}

	// Координаты центров маркеров для трансформации
	rx, ry := float64(offsetX+markerOffset+markerSize/2), float64(offsetY+markerOffset+markerSize/2)
	gx, gy := float64(offsetX+width-markerOffset-markerSize/2), float64(offsetY+markerOffset+markerSize/2)
	bx, by := float64(offsetX+markerOffset+markerSize/2), float64(offsetY+height-markerOffset-markerSize/2)
	qx, qy := float64(offsetX+width-markerOffset-markerSize/2), float64(offsetY+height-markerOffset-markerSize/2)

	// Функция для билинейной интерполяции координат
	transform := func(u, v float64) (float64, float64) {
		distX := float64(width - markerSize - 2*markerOffset)
		distY := float64(height - markerSize - 2*markerOffset)
		fu := (u - float64(markerOffset+markerSize/2)) / distX
		fv := (v - float64(markerOffset+markerSize/2)) / distY
		x := rx*(1-fu)*(1-fv) + gx*fu*(1-fv) + bx*(1-fu)*fv + qx*fu*fv
		y := ry*(1-fu)*(1-fv) + gy*fu*(1-fv) + by*(1-fu)*fv + qy*fu*fv
		return x, y
	}

	var bits []bool
	for y := margin; y <= height-margin-effectiveBlockSize; y += effectiveBlockSize {
		for x := margin; x <= width-margin-effectiveBlockSize; x += effectiveBlockSize {
			if (x < 16 && y < 16) || (x >= width-16 && y < 16) || (x < 16 && y >= height-16) || (x >= width-16 && y >= height-16) {
				continue
			}
			if x < 6 || y < 6 {
				continue
			}

			// Сэмплируем центр блока
			var sumR, sumG, sumB uint32
			points := 0
			sampleSize := 1
			if effectiveBlockSize >= 6 {
				sampleSize = 2
			}
			for dy := -sampleSize; dy <= sampleSize; dy++ {
				for dx := -sampleSize; dx <= sampleSize; dx++ {
					pxReal, pyReal := transform(float64(x)+float64(effectiveBlockSize)/2.0+float64(dx), float64(y)+float64(effectiveBlockSize)/2.0+float64(dy))
					px, py := int(pxReal), int(pyReal)
					if px >= 0 && px < img.Bounds().Dx() && py >= 0 && py < img.Bounds().Dy() {
						c := img.RGBAAt(px, py)
						sumR += uint32(c.R)
						sumG += uint32(c.G)
						sumB += uint32(c.B)
						points++
					}
				}
			}

			if points > 0 {
				avgColor := color.RGBA{uint8(sumR / uint32(points)), uint8(sumG / uint32(points)), uint8(sumB / uint32(points)), 255}
				minDist := 1000000
				bestIdx := 0
				for i, pc := range DataPalette {
					dr, dg, db := int(avgColor.R)-int(pc.R), int(avgColor.G)-int(pc.G), int(avgColor.B)-int(pc.B)
					dist := dr*dr + dg*dg + db*db
					if dist < minDist {
						minDist = dist
						bestIdx = i
					}
				}
				for i := 0; i < bitsPerBlock; i++ {
					bits = append(bits, (bestIdx>>uint(bitsPerBlock-1-i))&1 == 1)
				}
			} else {
				for i := 0; i < bitsPerBlock; i++ {
					bits = append(bits, false)
				}
			}
		}
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
		b ^= 0xAA // Снимаем маску
		fullData = append(fullData, b)
	}

	if len(fullData) < 255 {
		return nil
	}

	// 1. Декодируем первый блок, чтобы узнать длину данных
	decodedFirst, ok := rsDecode(fullData[:255], 32)
	if !ok {
		return nil
	}

	if len(decodedFirst) < 3 || (decodedFirst[0] != 0x04 && decodedFirst[0] != 0x03) {
		return nil
	}

	dataLen := int(decodedFirst[1])<<8 | int(decodedFirst[2])
	if dataLen > 16384 { // Разумный предел для одного кадра
		return nil
	}
	// blockDataLen = 223
	numBlocks := (dataLen + 7 + 222) / 223
	totalEncodedLen := numBlocks * 255

	if len(fullData) < totalEncodedLen || totalEncodedLen <= 0 {
		return nil
	}

	// 2. Декодируем все необходимые блоки
	decoded, ok := rsDecode(fullData[:totalEncodedLen], 32)
	if !ok || len(decoded) < 3+dataLen+4 {
		return nil
	}

	payload := decoded[3 : 3+dataLen]
	receivedCRC := binary.BigEndian.Uint32(decoded[3+dataLen : 7+dataLen])
	if crc32.ChecksumIEEE(decoded[:3+dataLen]) == receivedCRC {
		return payload
	}

	return nil
}
