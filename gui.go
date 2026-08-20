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
		Title:                 "screengrab",
		URL:                   "/",
		Width:                 1100,
		Height:                760,
		MinWidth:              860,
		MinHeight:             620,
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

	mu                sync.Mutex
	captureDir        string
	framePaths        []string
	frameFiles        []outputFile
	lastManifest      *captureManifest
	lastError         string
	stopFlag          atomic.Bool
	recording         atomic.Bool
	transcribing      atomic.Bool
	microphoneActive  atomic.Bool
	systemAudioActive atomic.Bool
	recordStart       time.Time
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
	SourceID         string  `json:"source_id"`
	Display          int     `json:"display"`
	FPS              float64 `json:"fps"`
	X                int     `json:"x"`
	Y                int     `json:"y"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	Output           string  `json:"output"`
	Microphone       bool    `json:"microphone"`
	SystemAudio      bool    `json:"system_audio"`
	Transcript       bool    `json:"transcript"`
	TranscriptLocale string  `json:"transcript_locale"`
}

type RecordingState struct {
	Recording        bool     `json:"recording"`
	Elapsed          float64  `json:"elapsed"`
	FrameCount       int      `json:"frame_count"`
	CaptureDir       string   `json:"capture_dir"`
	FramePaths       []string `json:"frame_paths"`
	Microphone       bool     `json:"microphone"`
	SystemAudio      bool     `json:"system_audio"`
	Transcribing     bool     `json:"transcribing"`
	AudioPath        string   `json:"audio_path,omitempty"`
	SystemAudioPath  string   `json:"system_audio_path,omitempty"`
	TranscriptPath   string   `json:"transcript_path,omitempty"`
	TranscriptStatus string   `json:"transcript_status,omitempty"`
	Error            string   `json:"error,omitempty"`
}

func (s *CaptureService) StartRecording(req RecordRequest) error {
	// Claim the recording slot atomically so two racing Start clicks cannot
	// both open the same audio files; release it on any setup failure.
	if !s.recording.CompareAndSwap(false, true) {
		return errors.New("already recording")
	}
	started := false
	defer func() {
		if !started {
			s.recording.Store(false)
		}
	}()
	if req.FPS <= 0 {
		req.FPS = defaultFPS
	}
	if req.Output == "" {
		req.Output = s.cfg.output
	}
	if req.Transcript && !req.Microphone && !req.SystemAudio {
		return errors.New("transcription requires microphone or system audio recording")
	}
	resolvedLocale := req.TranscriptLocale
	var transcriptPreflightErr error
	if req.Transcript {
		locale, err := prepareTranscription(req.TranscriptLocale)
		resolvedLocale = locale
		if err != nil {
			if !errors.Is(err, errTranscriptionUnavailable) {
				return err
			}
			transcriptPreflightErr = err
		}
	}

	sourceID := req.SourceID
	if sourceID == "" {
		sourceID = displaySourceID(req.Display)
	}
	src, err := parseSource(sourceID)
	if err != nil {
		return err
	}

	// Millisecond precision keeps back-to-back recordings from landing in
	// the same directory and truncating each other's audio artifacts.
	dir := filepath.Join(req.Output, "capture-"+time.Now().Format("20060102-150405.000"))
	anyAudio := req.Microphone || req.SystemAudio
	dirMode := os.FileMode(0o755)
	if anyAudio {
		dirMode = 0o700
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create capture dir %q: %w", dir, err)
	}
	var mic, sys *audioRecorder
	if anyAudio {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure capture dir %q: %w", dir, err)
		}
	}
	if req.Microphone {
		mic, err = startMicrophoneRecorder(filepath.Join(dir, "audio.wav"))
		if err != nil {
			return err
		}
	}
	if req.SystemAudio {
		sys, err = startSystemAudioRecorder(filepath.Join(dir, "system_audio.wav"), src)
		if err != nil {
			_, _ = mic.Stop()
			return err
		}
	}

	s.mu.Lock()
	s.captureDir = dir
	s.framePaths = nil
	s.frameFiles = nil
	s.lastManifest = nil
	s.lastError = ""
	s.mu.Unlock()
	s.stopFlag.Store(false)
	s.transcribing.Store(false)
	s.microphoneActive.Store(req.Microphone)
	s.systemAudioActive.Store(req.SystemAudio)
	s.recordStart = time.Now()

	started = true
	go s.captureLoop(req, src, mic, sys, resolvedLocale, transcriptPreflightErr)
	return nil
}

func (s *CaptureService) StopRecording() {
	s.stopFlag.Store(true)
}

func (s *CaptureService) RecordingStatus() RecordingState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := RecordingState{
		Recording:    s.recording.Load(),
		CaptureDir:   s.captureDir,
		FrameCount:   len(s.framePaths),
		FramePaths:   append([]string(nil), s.framePaths...),
		Microphone:   s.microphoneActive.Load(),
		SystemAudio:  s.systemAudioActive.Load(),
		Transcribing: s.transcribing.Load(),
		Error:        s.lastError,
	}
	if s.lastManifest != nil {
		state.Microphone = s.lastManifest.Audio != nil
		state.SystemAudio = s.lastManifest.SystemAudio != nil
		if s.lastManifest.Audio != nil {
			state.AudioPath = s.lastManifest.Audio.Path
		}
		if s.lastManifest.SystemAudio != nil {
			state.SystemAudioPath = s.lastManifest.SystemAudio.Path
		}
		if s.lastManifest.Transcript != nil {
			state.TranscriptPath = s.lastManifest.Transcript.TextPath
			if state.TranscriptPath == "" {
				state.TranscriptPath = s.lastManifest.Transcript.JSONPath
			}
			state.TranscriptStatus = s.lastManifest.Transcript.Status
		}
	}
	if !s.recordStart.IsZero() {
		state.Elapsed = time.Since(s.recordStart).Seconds()
	}
	return state
}

func (s *CaptureService) captureLoop(req RecordRequest, src Source, mic, sys *audioRecorder, transcriptLocale string, transcriptPreflightErr error) {
	defer s.recording.Store(false)
	defer s.microphoneActive.Store(false)
	defer s.systemAudioActive.Store(false)

	interval := time.Duration(float64(time.Second) / req.FPS)
	idx := 0
	start := s.recordStart
	files := []outputFile{}
	var captureErr error

	emit := func(name string, data any) {
		if s.app == nil {
			return
		}
		s.app.Event.Emit(name, data)
	}

	doCapture := func() error {
		captureOffset := time.Since(start).Seconds()
		var audioOffset, sysOffset *float64
		if mic != nil {
			current := mic.CurrentTime()
			audioOffset = &current
		}
		if sys != nil {
			current := sys.CurrentTime()
			sysOffset = &current
		}
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
		file, err := describeFile(path, "frame", idx, img.Bounds().Dx(), img.Bounds().Dy())
		if err != nil {
			return err
		}
		file.CaptureOffsetSeconds = &captureOffset
		file.AudioOffsetSeconds = audioOffset
		file.SystemAudioOffsetSeconds = sysOffset
		files = append(files, file)
		s.mu.Lock()
		s.framePaths = append(s.framePaths, path)
		s.frameFiles = append(s.frameFiles, file)
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
		captureErr = err
	} else {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for !s.stopFlag.Load() {
			select {
			case <-ticker.C:
				if s.stopFlag.Load() {
					break
				}
				if err := doCapture(); err != nil {
					captureErr = err
					s.stopFlag.Store(true)
				}
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	finished := time.Now()
	timeline := frameTimelineFromFiles(files)
	audio, audioErr := finishAudio(mic, timeline, "audio", micOffset)
	if audioErr != nil && captureErr == nil {
		captureErr = audioErr
	}
	systemAudio, sysAudioErr := finishAudio(sys, timeline, "system audio", systemOffset)
	if sysAudioErr != nil && captureErr == nil {
		captureErr = sysAudioErr
	}

	var transcript, systemTranscript *transcriptArtifact
	var transcriptErr error
	if req.Transcript {
		hasCompleteTrack := (audio != nil && audio.Status == "complete") || (systemAudio != nil && systemAudio.Status == "complete")
		if hasCompleteTrack {
			s.transcribing.Store(true)
			emit("capture:transcribing", map[string]any{"locale": transcriptLocale})
			var micErr, sysErr error
			transcript, micErr = produceTranscript(s.captureDir, "transcript", audio, transcriptLocale, transcriptPreflightErr)
			systemTranscript, sysErr = produceTranscript(s.captureDir, "system_transcript", systemAudio, transcriptLocale, transcriptPreflightErr)
			transcriptErr = errors.Join(micErr, sysErr)
			s.transcribing.Store(false)
		}
	}

	region := (*regionSpec)(nil)
	if req.Width > 0 && req.Height > 0 {
		region = &regionSpec{X: req.X, Y: req.Y, Width: req.Width, Height: req.Height}
	}
	ok := captureErr == nil && transcriptErr == nil
	manifest := captureManifest{
		OK:               ok,
		Partial:          !ok && (len(files) > 0 || audio != nil || systemAudio != nil),
		Version:          2,
		Source:           src,
		Mode:             "frames",
		Output:           s.captureDir,
		ManifestPath:     filepath.Join(s.captureDir, "manifest.json"),
		Format:           "png",
		FPS:              req.FPS,
		CapturedFrames:   len(files),
		Region:           region,
		StartedAt:        start.Format(time.RFC3339Nano),
		FinishedAt:       finished.Format(time.RFC3339Nano),
		ElapsedSeconds:   finished.Sub(start).Seconds(),
		Files:            files,
		FrameTimeline:    timeline,
		Audio:            audio,
		SystemAudio:      systemAudio,
		Transcript:       transcript,
		SystemTranscript: systemTranscript,
	}
	manifest.FirstFile, manifest.LastFile = firstLastFile(files)
	manifest.FrameWidth, manifest.FrameHeight = firstOutputDimensions(files, nil)
	if err := writeManifest(manifest.ManifestPath, manifest); err != nil && captureErr == nil {
		captureErr = err
	}
	if audio != nil || systemAudio != nil {
		_ = os.Chmod(manifest.ManifestPath, 0o600)
	}

	s.mu.Lock()
	final := append([]string(nil), s.framePaths...)
	s.lastManifest = &manifest
	if captureErr != nil {
		s.lastError = captureErr.Error()
	} else if transcriptErr != nil {
		s.lastError = transcriptErr.Error()
	}
	s.mu.Unlock()
	if captureErr != nil {
		emit("capture:error", captureErr.Error())
	}
	if transcriptErr != nil {
		emit("capture:transcript_error", transcriptErr.Error())
	}
	emit("capture:complete", map[string]any{
		"frames":            final,
		"count":             len(final),
		"elapsed":           time.Since(s.recordStart).Seconds(),
		"audio":             audio,
		"system_audio":      systemAudio,
		"transcript":        transcript,
		"system_transcript": systemTranscript,
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

func (s *CaptureService) TranscriptText() (string, error) {
	s.mu.Lock()
	if s.lastManifest == nil || s.lastManifest.Transcript == nil || s.lastManifest.Transcript.TextPath == "" {
		s.mu.Unlock()
		return "", errors.New("no completed transcript")
	}
	path := s.lastManifest.Transcript.TextPath
	s.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read transcript %q: %w", path, err)
	}
	return string(raw), nil
}

// SaveSelected copies the chosen frames into a fresh dest directory and
// places the resolved path on the system clipboard so the user can paste it
// into another tool. Returns the absolute destination path.
func (s *CaptureService) SaveSelected(indices []int, outputBase string) (string, error) {
	s.mu.Lock()
	all := append([]string(nil), s.framePaths...)
	var manifest *captureManifest
	if s.lastManifest != nil {
		copy := *s.lastManifest
		manifest = &copy
	}
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
	if manifest != nil {
		if err := copySelectedCapture(*manifest, indices, dest); err != nil {
			return "", err
		}
	} else {
		if err := copyFrames(srcs, dest); err != nil {
			return "", err
		}
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
	return copyFileMode(src, dst, 0o644)
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	sf, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}
	defer sf.Close()
	df, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	if _, err := io.Copy(df, sf); err != nil {
		df.Close()
		return fmt.Errorf("copy %q -> %q: %w", src, dst, err)
	}
	return df.Close()
}

func copySelectedCapture(manifest captureManifest, indices []int, dest string) error {
	private := manifest.Audio != nil || manifest.SystemAudio != nil || manifest.Transcript != nil || manifest.SystemTranscript != nil
	dirMode := os.FileMode(0o755)
	if private {
		dirMode = 0o700
	}
	if err := os.MkdirAll(dest, dirMode); err != nil {
		return fmt.Errorf("create dest dir %q: %w", dest, err)
	}
	if private {
		if err := os.Chmod(dest, 0o700); err != nil {
			return err
		}
	}

	selected := make([]outputFile, 0, len(indices))
	for selectedIndex, sourceIndex := range indices {
		if sourceIndex < 0 || sourceIndex >= len(manifest.Files) {
			return fmt.Errorf("frame index %d out of range", sourceIndex)
		}
		source := manifest.Files[sourceIndex]
		ext := filepath.Ext(source.Path)
		path := filepath.Join(dest, fmt.Sprintf("frame_%04d%s", selectedIndex, ext))
		if err := copyFile(source.Path, path); err != nil {
			return err
		}
		source.Path = path
		selected = append(selected, source)
	}

	manifest.Output = dest
	manifest.ManifestPath = filepath.Join(dest, "manifest.json")
	manifest.Files = selected
	manifest.FrameTimeline = frameTimelineFromFiles(selected)
	manifest.CapturedFrames = len(selected)
	manifest.RequestedFrames = 0
	manifest.FirstFile, manifest.LastFile = firstLastFile(selected)
	copyAudio := func(artifact *audioArtifact, name string) (*audioArtifact, error) {
		if artifact == nil {
			return nil, nil
		}
		audio := *artifact
		audio.Path = filepath.Join(dest, name)
		if err := copyFileMode(artifact.Path, audio.Path, 0o600); err != nil {
			return nil, err
		}
		return &audio, nil
	}
	copyTranscript := func(artifact *transcriptArtifact, baseName string) (*transcriptArtifact, error) {
		if artifact == nil {
			return nil, nil
		}
		transcript := *artifact
		if transcript.TextPath != "" {
			newPath := filepath.Join(dest, baseName+".txt")
			if err := copyFileMode(transcript.TextPath, newPath, 0o600); err != nil {
				return nil, err
			}
			transcript.TextPath = newPath
		}
		if transcript.JSONPath != "" {
			newPath := filepath.Join(dest, baseName+".json")
			if err := copyFileMode(transcript.JSONPath, newPath, 0o600); err != nil {
				return nil, err
			}
			transcript.JSONPath = newPath
		}
		return &transcript, nil
	}
	var err error
	if manifest.Audio, err = copyAudio(manifest.Audio, "audio.wav"); err != nil {
		return err
	}
	if manifest.SystemAudio, err = copyAudio(manifest.SystemAudio, "system_audio.wav"); err != nil {
		return err
	}
	if manifest.Transcript, err = copyTranscript(manifest.Transcript, "transcript"); err != nil {
		return err
	}
	if manifest.SystemTranscript, err = copyTranscript(manifest.SystemTranscript, "system_transcript"); err != nil {
		return err
	}
	if err := writeManifest(manifest.ManifestPath, manifest); err != nil {
		return err
	}
	if private {
		return os.Chmod(manifest.ManifestPath, 0o600)
	}
	return nil
}

func frameTimelineFromFiles(files []outputFile) []frameTiming {
	out := make([]frameTiming, 0, len(files))
	for _, file := range files {
		if file.CaptureOffsetSeconds == nil {
			continue
		}
		out = append(out, frameTiming{
			Index:                    file.Index,
			CaptureOffsetSeconds:     *file.CaptureOffsetSeconds,
			AudioOffsetSeconds:       file.AudioOffsetSeconds,
			SystemAudioOffsetSeconds: file.SystemAudioOffsetSeconds,
		})
	}
	return out
}

func encodePNGBase64(img image.Image) (string, error) {
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
