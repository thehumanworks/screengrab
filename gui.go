package main

import (
	"bytes"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kbinani/screenshot"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend
var frontendAssets embed.FS

// runGUI launches the Wails v3 desktop shell. On macOS 26+ the window is
// backed by NSGlassEffectView; on earlier macOS it falls back to
// NSVisualEffectView. On other platforms the webview is opaque and the CSS
// Liquid Glass recipe in frontend/style.css carries the look.
func runGUI(cfg config) error {
	if screenshot.NumActiveDisplays() == 0 {
		return errors.New("no active displays detected")
	}

	svc := newCaptureService(cfg)

	logLevel := slog.LevelInfo
	if cfg.devtools {
		logLevel = slog.LevelDebug
	}
	app := application.New(application.Options{
		Name:        "screengrab",
		Description: "Sample the screen at a fixed FPS for AI ingestion.",
		LogLevel:    logLevel,
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(frontendAssets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	svc.app = app

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "screengrab",
		URL:              "/",
		Width:            1100,
		Height:           760,
		MinWidth:         860,
		MinHeight:        620,
		Frameless:             true,
		BackgroundType:        application.BackgroundTypeTransparent,
		DevToolsEnabled:       cfg.devtools,
		EnableFileDrop:        false,
		FullscreenButtonState: application.ButtonHidden,
		Mac: application.MacWindow{
			Backdrop:                application.MacBackdropLiquidGlass,
			InvisibleTitleBarHeight: 36,
			TitleBar:                application.MacTitleBarHiddenInset,
			LiquidGlass: application.MacLiquidGlass{
				Style:        application.LiquidGlassStyleAutomatic,
				Material:     application.NSVisualEffectMaterialContentBackground,
				CornerRadius: 18.0,
				TintColor:    nil,
			},
		},
	})
	svc.window = win

	return app.Run()
}

// CaptureService is registered as a Wails Service. Every exported method is
// callable from JS via `Call.ByName("screengrab.CaptureService.MethodName", ...args)`.
// The FQN prefix is `<go-package-path>.<struct-name>` — see Wails v3's
// getMethods in pkg/application/bindings.go. The frontend keeps this prefix
// in a single constant; gui_bindings_test.go enforces that the same set of
// methods is reachable from Go reflection so a rename on either side fails
// CI rather than silently breaking the GUI at runtime.
type CaptureService struct {
	app    *application.App
	window *application.WebviewWindow

	cfg config

	mu          sync.Mutex
	captureDir  string
	framePaths  []string
	stopFlag    atomic.Bool
	recording   atomic.Bool
	recordStart time.Time
}

func newCaptureService(cfg config) *CaptureService {
	return &CaptureService{cfg: cfg}
}

type DisplayInfo struct {
	Index  int `json:"index"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ListDisplays is kept for back-compat with anything outside the bundled
// frontend that already drove the GUI by display index. The bundled
// frontend prefers ListSources, which also includes maximized-app windows.
func (s *CaptureService) ListDisplays() []DisplayInfo {
	n := screenshot.NumActiveDisplays()
	out := make([]DisplayInfo, 0, n)
	for i := 0; i < n; i++ {
		b := screenshot.GetDisplayBounds(i)
		out = append(out, DisplayInfo{Index: i, Width: b.Dx(), Height: b.Dy()})
	}
	return out
}

// ListSources returns every capturable target — physical displays plus
// macOS windows (including those on inactive Spaces, i.e. maximized apps
// that the user would normally need to swipe to reach). Each source carries
// a canonical ID string that the frontend hands back to SnapshotSource and
// StartRecording.
func (s *CaptureService) ListSources() []Source {
	return listSources()
}

// SnapshotDisplay returns a base64-encoded PNG of the given display. Kept
// for the same back-compat reason as ListDisplays.
func (s *CaptureService) SnapshotDisplay(displayIdx int) (string, error) {
	img, err := captureFrame(displayIdx)
	if err != nil {
		return "", err
	}
	return encodePNGBase64(img)
}

// SnapshotSource returns a base64 PNG of the requested source. The frontend
// renders it inside the region picker so the user can drag a rectangle in
// source-local coordinates (0,0 = top-left of that display or window).
func (s *CaptureService) SnapshotSource(sourceID string) (string, error) {
	src, err := parseSource(sourceID)
	if err != nil {
		return "", err
	}
	img, err := captureSource(src)
	if err != nil {
		return "", err
	}
	return encodePNGBase64(img)
}

type RecordRequest struct {
	// SourceID is the canonical Source identifier ("display:N" or
	// "window:0xID"). When empty, Display is used for back-compat.
	SourceID string  `json:"source_id"`
	Display  int     `json:"display"`
	FPS      float64 `json:"fps"`
	X        int     `json:"x"`
	Y        int     `json:"y"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Output   string  `json:"output"`
}

type RecordingState struct {
	Recording   bool    `json:"recording"`
	Elapsed     float64 `json:"elapsed"`
	FrameCount  int     `json:"frame_count"`
	CaptureDir  string  `json:"capture_dir"`
	FramePaths  []string `json:"frame_paths"`
}

func (s *CaptureService) StartRecording(req RecordRequest) error {
	if s.recording.Load() {
		return errors.New("already recording")
	}
	if req.FPS <= 0 {
		req.FPS = defaultFPS
	}
	if req.Output == "" {
		req.Output = s.cfg.output
	}

	sourceID := req.SourceID
	if sourceID == "" {
		sourceID = displaySourceID(req.Display)
	}
	src, err := parseSource(sourceID)
	if err != nil {
		return err
	}

	dir := filepath.Join(req.Output, "capture-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create capture dir %q: %w", dir, err)
	}

	s.mu.Lock()
	s.captureDir = dir
	s.framePaths = nil
	s.mu.Unlock()
	s.stopFlag.Store(false)
	s.recording.Store(true)
	s.recordStart = time.Now()

	go s.captureLoop(req, src)
	return nil
}

func (s *CaptureService) StopRecording() {
	s.stopFlag.Store(true)
}

func (s *CaptureService) RecordingStatus() RecordingState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := RecordingState{
		Recording:  s.recording.Load(),
		CaptureDir: s.captureDir,
		FrameCount: len(s.framePaths),
		FramePaths: append([]string(nil), s.framePaths...),
	}
	if !s.recordStart.IsZero() {
		state.Elapsed = time.Since(s.recordStart).Seconds()
	}
	return state
}

func (s *CaptureService) captureLoop(req RecordRequest, src Source) {
	defer s.recording.Store(false)

	interval := time.Duration(float64(time.Second) / req.FPS)
	idx := 0

	emit := func(name string, data any) {
		if s.app == nil {
			return
		}
		s.app.Event.Emit(name, data)
	}

	doCapture := func() error {
		var img *image.RGBA
		var err error
		if req.Width > 0 && req.Height > 0 {
			img, err = captureSourceRegion(src, req.X, req.Y, req.Width, req.Height)
		} else {
			img, err = captureSource(src)
		}
		if err != nil {
			// Per-frame failure is non-fatal — the most common cause is
			// the user picking a window on an inactive Space and not
			// having swiped to it yet. Surface a frontend-visible event
			// so the GUI can show a "waiting for target Space" hint, but
			// keep the ticker going so capture resumes the moment the
			// user swipes over.
			emit("capture:frame_error", map[string]any{
				"index":   idx,
				"error":   err.Error(),
				"elapsed": time.Since(s.recordStart).Seconds(),
			})
			return nil
		}
		path := filepath.Join(s.captureDir, fmt.Sprintf("frame_%04d.png", idx))
		if err := writePNG(path, img); err != nil {
			return err
		}
		s.mu.Lock()
		s.framePaths = append(s.framePaths, path)
		count := len(s.framePaths)
		dir := s.captureDir
		s.mu.Unlock()
		idx++
		emit("capture:frame", map[string]any{
			"index":   idx - 1,
			"path":    path,
			"count":   count,
			"dir":     dir,
			"elapsed": time.Since(s.recordStart).Seconds(),
		})
		return nil
	}

	if err := doCapture(); err != nil {
		emit("capture:error", err.Error())
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if s.stopFlag.Load() {
			break
		}
		select {
		case <-ticker.C:
			if s.stopFlag.Load() {
				goto done
			}
			if err := doCapture(); err != nil {
				emit("capture:error", err.Error())
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
done:
	s.mu.Lock()
	final := append([]string(nil), s.framePaths...)
	s.mu.Unlock()
	emit("capture:complete", map[string]any{
		"frames":   final,
		"count":    len(final),
		"elapsed":  time.Since(s.recordStart).Seconds(),
	})
}

// FramePreview returns a base64 thumbnail for a recorded frame so the review
// grid in the frontend does not need to read disk files through the asset
// handler.
func (s *CaptureService) FramePreview(index int) (string, error) {
	s.mu.Lock()
	if index < 0 || index >= len(s.framePaths) {
		s.mu.Unlock()
		return "", fmt.Errorf("frame %d out of range", index)
	}
	path := s.framePaths[index]
	s.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open frame %q: %w", path, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// SaveSelected copies the chosen frames into a fresh dest directory and
// places the resolved path on the system clipboard so the user can paste it
// into another tool. Returns the absolute destination path.
func (s *CaptureService) SaveSelected(indices []int, outputBase string) (string, error) {
	s.mu.Lock()
	all := append([]string(nil), s.framePaths...)
	s.mu.Unlock()

	if len(indices) == 0 {
		return "", errors.New("no frames selected")
	}
	if outputBase == "" {
		outputBase = s.cfg.output
	}

	sort.Ints(indices)
	srcs := make([]string, 0, len(indices))
	for _, i := range indices {
		if i < 0 || i >= len(all) {
			return "", fmt.Errorf("frame index %d out of range", i)
		}
		srcs = append(srcs, all[i])
	}

	dest := filepath.Join(outputBase, "selected-"+time.Now().Format("20060102-150405"))
	if err := copyFrames(srcs, dest); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		abs = dest
	}
	if s.app != nil {
		s.app.Clipboard.SetText(abs)
	}
	return abs, nil
}

// Platform reports the runtime OS so the frontend can swap visuals (for
// example, suppressing the CSS Liquid Glass layers on macOS where the window
// itself is the OS-rendered material).
func (s *CaptureService) Platform() string { return runtime.GOOS }

// Quit closes the GUI cleanly from the frontend.
func (s *CaptureService) Quit() {
	if s.app != nil {
		s.app.Quit()
	}
}

// copyFrames copies each src path into dest using the canonical
// frame_NNNN.png naming. Returns an error if any copy fails; partial output
// is left in place for inspection.
func copyFrames(srcs []string, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create dest dir %q: %w", dest, err)
	}
	for i, src := range srcs {
		name := fmt.Sprintf("frame_%04d.png", i)
		if err := copyFile(src, filepath.Join(dest, name)); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	sf, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}
	defer sf.Close()
	df, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	if _, err := io.Copy(df, sf); err != nil {
		df.Close()
		return fmt.Errorf("copy %q -> %q: %w", src, dst, err)
	}
	return df.Close()
}

func encodePNGBase64(img image.Image) (string, error) {
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
