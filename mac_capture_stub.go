//go:build !darwin

package main

import (
	"errors"
	"image"
)

var ErrWindowCaptureUnsupported = errors.New("window-level capture is only implemented on darwin")

type macWindow struct {
	ID       uint32
	X, Y     int
	Width    int
	Height   int
	OnScreen bool
	App      string
	Title    string
}

func listMacWindows() ([]macWindow, error) {
	return nil, nil
}

func captureMacWindow(windowID uint32) (*image.RGBA, error) {
	return nil, ErrWindowCaptureUnsupported
}

func captureMacWindowRegion(windowID uint32, x, y, w, h int) (*image.RGBA, error) {
	return nil, ErrWindowCaptureUnsupported
}

func macWindowExists(windowID uint32) bool { return false }
