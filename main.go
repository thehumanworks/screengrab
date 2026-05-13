package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kbinani/screenshot"
)

const defaultFPS = 2.0

type config struct {
	fps      float64
	duration time.Duration
	output   string
	mode     string
	display  int
	cols     int
}

func main() {
	cfg := parseFlags()

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "screengrab: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.Float64Var(&cfg.fps, "fps", defaultFPS, "frames per second to capture (sane defaults are low because output feeds AI models)")
	flag.DurationVar(&cfg.duration, "duration", 0, "max recording duration (e.g. 10s, 1m). 0 = record until SIGINT (Ctrl+C)")
	flag.StringVar(&cfg.output, "output", "screengrab-out", "output directory")
	flag.StringVar(&cfg.mode, "mode", "frames", "output mode: 'frames' for individual PNGs, 'spritesheet' for one composite PNG with sidecar JSON")
	flag.IntVar(&cfg.display, "display", 0, "display index to capture (0 = primary)")
	flag.IntVar(&cfg.cols, "cols", 0, "columns in spritesheet (0 = auto ~= sqrt of frame count)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: screengrab [flags]\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Records the screen at a fixed FPS and outputs frames or a spritesheet.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Stop a long-running capture with Ctrl+C; partial output is flushed.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	return cfg
}

func run(cfg config) error {
	if cfg.fps <= 0 {
		return fmt.Errorf("--fps must be > 0 (got %v)", cfg.fps)
	}
	if cfg.mode != "frames" && cfg.mode != "spritesheet" {
		return fmt.Errorf("--mode must be 'frames' or 'spritesheet' (got %q)", cfg.mode)
	}
	if cfg.display < 0 || cfg.display >= screenshot.NumActiveDisplays() {
		return fmt.Errorf("--display %d out of range (active displays: %d)", cfg.display, screenshot.NumActiveDisplays())
	}
	if err := os.MkdirAll(cfg.output, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	bounds := screenshot.GetDisplayBounds(cfg.display)
	interval := time.Duration(float64(time.Second) / cfg.fps)

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopCh)

	// When duration is set, capture a deterministic frame count: round(fps * seconds).
	// duration acts as the natural stopping condition and avoids ticker/deadline races.
	targetFrames := 0
	if cfg.duration > 0 {
		targetFrames = int(math.Round(cfg.fps * cfg.duration.Seconds()))
		if targetFrames < 1 {
			targetFrames = 1
		}
	}

	fmt.Fprintf(os.Stderr, "screengrab: display=%d size=%dx%d fps=%v interval=%v mode=%s output=%s\n",
		cfg.display, bounds.Dx(), bounds.Dy(), cfg.fps, interval, cfg.mode, cfg.output)
	if targetFrames > 0 {
		fmt.Fprintf(os.Stderr, "screengrab: will capture %d frames over %v or stop on Ctrl+C\n", targetFrames, cfg.duration)
	} else {
		fmt.Fprintf(os.Stderr, "screengrab: recording... press Ctrl+C to stop\n")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	frames := []*image.RGBA{} // only used by spritesheet mode
	frameCount := 0
	start := time.Now()

	// Capture one frame immediately at t=0 so a short duration still yields frames.
	if img, err := captureFrame(cfg.display); err != nil {
		return err
	} else {
		frameCount++
		if err := handleFrame(cfg, img, frameCount-1, &frames); err != nil {
			return err
		}
	}

loop:
	for {
		if targetFrames > 0 && frameCount >= targetFrames {
			break loop
		}
		select {
		case <-stopCh:
			fmt.Fprintln(os.Stderr, "screengrab: SIGINT received, stopping")
			break loop
		case <-ticker.C:
			img, err := captureFrame(cfg.display)
			if err != nil {
				return err
			}
			frameCount++
			if err := handleFrame(cfg, img, frameCount-1, &frames); err != nil {
				return err
			}
		}
	}

	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "screengrab: captured %d frames in %v\n", frameCount, elapsed.Round(time.Millisecond))

	if cfg.mode == "spritesheet" {
		if frameCount == 0 {
			return errors.New("no frames captured; nothing to composite")
		}
		if err := writeSpritesheet(cfg, frames); err != nil {
			return fmt.Errorf("write spritesheet: %w", err)
		}
	}
	return nil
}

func captureFrame(display int) (*image.RGBA, error) {
	img, err := screenshot.CaptureDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("capture display %d: %w", display, err)
	}
	return img, nil
}

func handleFrame(cfg config, img *image.RGBA, index int, frames *[]*image.RGBA) error {
	switch cfg.mode {
	case "frames":
		path := filepath.Join(cfg.output, fmt.Sprintf("frame_%04d.png", index))
		return writePNG(path, img)
	case "spritesheet":
		*frames = append(*frames, img)
		return nil
	}
	return nil
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	return enc.Encode(f, img)
}

type sheetMeta struct {
	Frames      int     `json:"frames"`
	Cols        int     `json:"cols"`
	Rows        int     `json:"rows"`
	FrameWidth  int     `json:"frame_width"`
	FrameHeight int     `json:"frame_height"`
	SheetWidth  int     `json:"sheet_width"`
	SheetHeight int     `json:"sheet_height"`
	FPS         float64 `json:"fps"`
}

func writeSpritesheet(cfg config, frames []*image.RGBA) error {
	n := len(frames)
	if n == 0 {
		return errors.New("no frames")
	}
	fw := frames[0].Bounds().Dx()
	fh := frames[0].Bounds().Dy()

	cols := cfg.cols
	if cols <= 0 {
		cols = int(math.Ceil(math.Sqrt(float64(n))))
	}
	if cols > n {
		cols = n
	}
	rows := int(math.Ceil(float64(n) / float64(cols)))

	sheet := image.NewRGBA(image.Rect(0, 0, fw*cols, fh*rows))
	for i, fr := range frames {
		col := i % cols
		row := i / cols
		dst := image.Rect(col*fw, row*fh, (col+1)*fw, (row+1)*fh)
		draw.Draw(sheet, dst, fr, fr.Bounds().Min, draw.Src)
	}

	sheetPath := filepath.Join(cfg.output, "spritesheet.png")
	if err := writePNG(sheetPath, sheet); err != nil {
		return err
	}

	meta := sheetMeta{
		Frames:      n,
		Cols:        cols,
		Rows:        rows,
		FrameWidth:  fw,
		FrameHeight: fh,
		SheetWidth:  fw * cols,
		SheetHeight: fh * rows,
		FPS:         cfg.fps,
	}
	metaPath := filepath.Join(cfg.output, "spritesheet.json")
	mf, err := os.Create(metaPath)
	if err != nil {
		return err
	}
	defer mf.Close()
	enc := json.NewEncoder(mf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "screengrab: wrote %s (%dx%d, %d frames in %dx%d grid)\n",
		sheetPath, meta.SheetWidth, meta.SheetHeight, n, cols, rows)
	return nil
}
