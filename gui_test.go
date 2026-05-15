package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeTinyPNG(t *testing.T, path string, fill color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, fill)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func TestCopyFrames(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "selected")

	colors := []color.RGBA{
		{R: 255, G: 0, B: 0, A: 255},
		{R: 0, G: 255, B: 0, A: 255},
		{R: 0, G: 0, B: 255, A: 255},
		{R: 255, G: 255, B: 0, A: 255},
	}
	var allFrames []string
	for i, c := range colors {
		p := filepath.Join(src, "frame_"+padN(i)+".png")
		writeTinyPNG(t, p, c)
		allFrames = append(allFrames, p)
	}

	// Select indices 0 and 2 only — verifies subset selection.
	selectedIdx := []int{0, 2}
	sort.Ints(selectedIdx)
	var srcs []string
	for _, i := range selectedIdx {
		srcs = append(srcs, allFrames[i])
	}

	if err := copyFrames(srcs, dst); err != nil {
		t.Fatalf("copyFrames: %v", err)
	}

	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if len(entries) != len(srcs) {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dst has %d files (%v), want %d", len(entries), names, len(srcs))
	}

	for i, srcPath := range srcs {
		srcBytes, err := os.ReadFile(srcPath)
		if err != nil {
			t.Fatalf("read src %s: %v", srcPath, err)
		}
		dstPath := filepath.Join(dst, "frame_"+padN(i)+".png")
		dstBytes, err := os.ReadFile(dstPath)
		if err != nil {
			t.Fatalf("read dst %s: %v", dstPath, err)
		}
		if !bytes.Equal(srcBytes, dstBytes) {
			t.Fatalf("byte mismatch for %s vs %s", srcPath, dstPath)
		}
	}
}

func TestCopyFramesCreatesDestDir(t *testing.T) {
	src := t.TempDir()
	srcPath := filepath.Join(src, "frame_0000.png")
	writeTinyPNG(t, srcPath, color.RGBA{R: 10, G: 20, B: 30, A: 255})

	// Destination two levels deep, neither exists yet.
	dst := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := copyFrames([]string{srcPath}, dst); err != nil {
		t.Fatalf("copyFrames: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "frame_0000.png")); err != nil {
		t.Fatalf("expected destination file present: %v", err)
	}
}

func padN(i int) string {
	s := []byte{'0', '0', '0', '0'}
	idx := len(s) - 1
	for i > 0 && idx >= 0 {
		s[idx] = byte('0' + i%10)
		i /= 10
		idx--
	}
	return string(s)
}
