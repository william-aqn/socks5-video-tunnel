package main

import (
	"image"
)

// VirtualCamera представляет интерфейс для работы с виртуальной камерой
type VirtualCamera interface {
	WriteFrame(img *image.RGBA) error
	Close() error
	GetURL() string
}
