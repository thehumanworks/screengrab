package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRegionSpec(t *testing.T) {
	got, err := parseRegionSpec("10, 20, 300, 200")
	if err != nil {
		t.Fatalf("parseRegionSpec: %v", err)
	}
	if *got != (regionSpec{X: 10, Y: 20, Width: 300, Height: 200}) {
		t.Fatalf("region = %+v", *got)
	}

	if _, err := parseRegionSpec("10,20,0,100"); err == nil {
		t.Fatalf("expected zero-width region to fail")
	}
	if _, err := parseRegionSpec("10,20,30"); err == nil {
		t.Fatalf("expected malformed region to fail")
	}
}

func TestPlannedFrameCountCapsDuration(t *testing.T) {
	if got := plannedFrameCount(2, 10*time.Second, 3); got != 3 {
		t.Fatalf("plannedFrameCount duration+cap = %d, want 3", got)
	}
	if got := plannedFrameCount(2, 500*time.Millisecond, 0); got != 1 {
		t.Fatalf("plannedFrameCount short duration = %d, want 1", got)
	}
	if got := plannedFrameCount(2, 0, 4); got != 4 {
		t.Fatalf("plannedFrameCount frame cap only = %d, want 4", got)
	}
}

func TestFitToMaxDim(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), A: 255})
		}
	}

	got := fitToMaxDim(img, 100)
	if got.Bounds().Dx() != 100 || got.Bounds().Dy() != 50 {
		t.Fatalf("scaled size = %dx%d, want 100x50", got.Bounds().Dx(), got.Bounds().Dy())
	}
	if same := fitToMaxDim(img, 500); same != img {
		t.Fatalf("fitToMaxDim should not upscale")
	}
}

func TestNormalizeConfigFormatAndValidation(t *testing.T) {
	cfg := config{fps: 2, mode: "frames", format: "jpg", quality: 75, output: "."}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if cfg.format != "jpeg" {
		t.Fatalf("format = %q, want jpeg", cfg.format)
	}

	bad := config{fps: 2, mode: "frames", format: "jpeg", quality: 101, output: "."}
	if err := normalizeConfig(&bad); err == nil {
		t.Fatalf("expected invalid quality to fail")
	}
}

func TestWriteImageJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	path := filepath.Join(t.TempDir(), "frame_0000.jpg")
	cfg := config{format: "jpeg", quality: 70}
	if err := writeImage(path, img, cfg); err != nil {
		t.Fatalf("writeImage jpeg: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open jpeg: %v", err)
	}
	defer f.Close()
	_, format, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decode jpeg config: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("format = %q, want jpeg", format)
	}
}

func TestPrepareOutputDirOverwriteGuard(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "frame_0000.png")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}
	err := prepareOutputDir(config{output: dir})
	if err == nil || !strings.Contains(err.Error(), "--overwrite") {
		t.Fatalf("expected overwrite guidance, got %v", err)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("existing file should remain without overwrite: %v", err)
	}
	if err := prepareOutputDir(config{output: dir, overwrite: true}); err != nil {
		t.Fatalf("prepareOutputDir overwrite: %v", err)
	}
	if _, err := os.Stat(existing); !os.IsNotExist(err) {
		t.Fatalf("existing file should be removed with overwrite, stat err=%v", err)
	}
}
