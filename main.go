package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kbinani/screenshot"
)

const defaultFPS = 2.0

type config struct {
	fps              float64
	duration         time.Duration
	frames           int
	output           string
	mode             string
	format           string
	quality          int
	display          int
	source           string
	region           string
	maxDim           int
	cols             int
	gui              bool
	devtools         bool
	listSources      bool
	json             bool
	overwrite        bool
	microphone       bool
	audio            string
	transcript       bool
	transcriptLocale string
}

func (c config) audioMic() bool    { return c.audio == "mic" || c.audio == "both" }
func (c config) audioSystem() bool { return c.audio == "system" || c.audio == "both" }
func (c config) audioAny() bool    { return c.audio != "" }

type regionSpec struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type captureManifest struct {
	OK               bool                `json:"ok"`
	Partial          bool                `json:"partial,omitempty"`
	Version          int                 `json:"version"`
	Source           Source              `json:"source"`
	Mode             string              `json:"mode"`
	Output           string              `json:"output"`
	ManifestPath     string              `json:"manifest_path"`
	Format           string              `json:"format"`
	Quality          int                 `json:"quality,omitempty"`
	FPS              float64             `json:"fps"`
	Duration         string              `json:"duration,omitempty"`
	RequestedFrames  int                 `json:"requested_frames,omitempty"`
	CapturedFrames   int                 `json:"captured_frames"`
	Region           *regionSpec         `json:"region,omitempty"`
	MaxDim           int                 `json:"max_dim,omitempty"`
	FrameWidth       int                 `json:"frame_width,omitempty"`
	FrameHeight      int                 `json:"frame_height,omitempty"`
	FirstFile        string              `json:"first_file,omitempty"`
	LastFile         string              `json:"last_file,omitempty"`
	StartedAt        string              `json:"started_at"`
	FinishedAt       string              `json:"finished_at"`
	ElapsedSeconds   float64             `json:"elapsed_seconds"`
	Files            []outputFile        `json:"files"`
	FrameTimeline    []frameTiming       `json:"frame_timeline,omitempty"`
	Spritesheet      *sheetMeta          `json:"spritesheet,omitempty"`
	Audio            *audioArtifact      `json:"audio,omitempty"`
	SystemAudio      *audioArtifact      `json:"system_audio,omitempty"`
	Transcript       *transcriptArtifact `json:"transcript,omitempty"`
	SystemTranscript *transcriptArtifact `json:"system_transcript,omitempty"`
}

type captureSummary struct {
	OK               bool                `json:"ok"`
	Partial          bool                `json:"partial,omitempty"`
	Version          int                 `json:"version"`
	Source           Source              `json:"source"`
	Mode             string              `json:"mode"`
	Output           string              `json:"output"`
	ManifestPath     string              `json:"manifest_path"`
	Format           string              `json:"format"`
	Quality          int                 `json:"quality,omitempty"`
	FPS              float64             `json:"fps"`
	Duration         string              `json:"duration,omitempty"`
	RequestedFrames  int                 `json:"requested_frames,omitempty"`
	CapturedFrames   int                 `json:"captured_frames"`
	Region           *regionSpec         `json:"region,omitempty"`
	MaxDim           int                 `json:"max_dim,omitempty"`
	FrameWidth       int                 `json:"frame_width,omitempty"`
	FrameHeight      int                 `json:"frame_height,omitempty"`
	FirstFile        string              `json:"first_file,omitempty"`
	LastFile         string              `json:"last_file,omitempty"`
	ElapsedSeconds   float64             `json:"elapsed_seconds"`
	Spritesheet      *sheetMeta          `json:"spritesheet,omitempty"`
	Audio            *audioArtifact      `json:"audio,omitempty"`
	SystemAudio      *audioArtifact      `json:"system_audio,omitempty"`
	Transcript       *transcriptArtifact `json:"transcript,omitempty"`
	SystemTranscript *transcriptArtifact `json:"system_transcript,omitempty"`
}

type outputFile struct {
	Path                     string   `json:"path"`
	Type                     string   `json:"type"`
	Index                    int      `json:"index"`
	Width                    int      `json:"width,omitempty"`
	Height                   int      `json:"height,omitempty"`
	Bytes                    int64    `json:"bytes"`
	CaptureOffsetSeconds     *float64 `json:"capture_offset_seconds,omitempty"`
	AudioOffsetSeconds       *float64 `json:"audio_offset_seconds,omitempty"`
	SystemAudioOffsetSeconds *float64 `json:"system_audio_offset_seconds,omitempty"`
}

type frameTiming struct {
	Index                    int      `json:"index"`
	CaptureOffsetSeconds     float64  `json:"capture_offset_seconds"`
	AudioOffsetSeconds       *float64 `json:"audio_offset_seconds,omitempty"`
	SystemAudioOffsetSeconds *float64 `json:"system_audio_offset_seconds,omitempty"`
}

func main() {
	cfg := parseFlags()

	var err error
	switch {
	case cfg.listSources:
		err = runListSources(cfg)
	case cfg.gui:
		err = runGUI(cfg)
	default:
		err = run(cfg)
	}
	if err != nil {
		if cfg.json {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "screengrab: %v\n", err)
		}
		os.Exit(1)
	}
}

func runListSources(cfg config) error {
	srcs := listSources()
	if cfg.json {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok":      true,
			"count":   len(srcs),
			"sources": srcs,
		})
	}
	if len(srcs) == 0 {
		fmt.Println("(no capturable sources detected — on macOS this usually means Screen Recording permission has not been granted)")
		return nil
	}
	fmt.Println("Visibility legend: [live] = on the current Space, capture works now.")
	fmt.Println("                   [off-Space] = on another macOS Space; swipe to it before recording.")
	fmt.Println()
	for _, s := range srcs {
		state := "live"
		if !s.OnScreen {
			state = "off-Space"
		}
		fmt.Printf("%-22s  [%-9s]  %s  %d×%d\n", s.ID, state, s.Name, s.Width, s.Height)
	}
	return nil
}

// resolveCLISource picks the capture source for the headless CLI run.
// --source wins when set; otherwise we fall back to the legacy --display N
// form so existing scripts keep working without a flag rename.
func resolveCLISource(cfg config) (Source, error) {
	if cfg.source != "" {
		return parseSource(cfg.source)
	}
	return resolveDisplaySource(cfg.display)
}

func parseFlags() config {
	var cfg config
	flag.Float64Var(&cfg.fps, "fps", defaultFPS, "frames per second to capture (sane defaults are low because output feeds AI models)")
	flag.DurationVar(&cfg.duration, "duration", 0, "max recording duration (e.g. 10s, 1m). 0 = record until SIGINT (Ctrl+C)")
	flag.IntVar(&cfg.frames, "frames", 0, "maximum frames to capture. 0 = derive from --duration or run until stopped")
	flag.StringVar(&cfg.output, "output", "screengrab-out", "output directory")
	flag.StringVar(&cfg.mode, "mode", "frames", "output mode: 'frames' for individual PNGs, 'spritesheet' for one composite PNG with sidecar JSON")
	flag.StringVar(&cfg.format, "format", "png", "image format: 'png' or 'jpg'/'jpeg'")
	flag.IntVar(&cfg.quality, "quality", 85, "JPEG quality from 1 to 100; ignored for PNG")
	flag.IntVar(&cfg.display, "display", 0, "display index to capture (0 = primary); shorthand for --source display:N")
	flag.StringVar(&cfg.source, "source", "", "capture source: 'display:N' for a physical display, or 'window:0xID' for a macOS maximized-app window (see --list-sources). Overrides --display when set.")
	flag.StringVar(&cfg.region, "region", "", "optional source-local crop rectangle 'x,y,w,h' before scaling/encoding")
	flag.IntVar(&cfg.maxDim, "max-dim", 0, "downscale each frame so its longest edge is at most this many pixels. 0 = original size")
	flag.IntVar(&cfg.cols, "cols", 0, "columns in spritesheet (0 = auto ~= sqrt of frame count)")
	flag.BoolVar(&cfg.gui, "gui", false, "launch the cross-platform desktop GUI (region picker, multi-select frame review, clipboard handoff)")
	flag.BoolVar(&cfg.devtools, "devtools", false, "open the webview devtools panel when --gui is set")
	flag.BoolVar(&cfg.listSources, "list-sources", false, "print every capturable source (displays and macOS windows) and exit")
	flag.BoolVar(&cfg.json, "json", false, "emit machine-readable JSON for --list-sources and final capture summary")
	flag.BoolVar(&cfg.overwrite, "overwrite", false, "allow replacing generated files already present in the output directory")
	flag.BoolVar(&cfg.microphone, "microphone", false, "record the default microphone to audio.wav (macOS); shorthand for --audio mic")
	flag.StringVar(&cfg.audio, "audio", "", "audio tracks to record (macOS): 'mic' for the default microphone (audio.wav), 'system' for what the captured source is playing (system_audio.wav), or 'both'")
	flag.BoolVar(&cfg.transcript, "transcript", false, "generate timed transcripts for each recorded audio track; requires --audio or --microphone")
	flag.StringVar(&cfg.transcriptLocale, "transcript-locale", "", "speech-recognition locale (default: system locale); requires --transcript")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: screengrab [flags]\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Records the screen at a fixed FPS and outputs frames or a spritesheet.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Stop a long-running capture with Ctrl+C; partial output is flushed.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	return cfg
}

func normalizeConfig(cfg *config) error {
	if cfg.fps <= 0 {
		return fmt.Errorf("--fps must be > 0 (got %v)", cfg.fps)
	}
	if cfg.frames < 0 {
		return fmt.Errorf("--frames must be >= 0 (got %d)", cfg.frames)
	}
	if cfg.mode != "frames" && cfg.mode != "spritesheet" {
		return fmt.Errorf("--mode must be 'frames' or 'spritesheet' (got %q)", cfg.mode)
	}
	cfg.format = strings.ToLower(strings.TrimSpace(cfg.format))
	switch cfg.format {
	case "png":
	case "jpg", "jpeg":
		cfg.format = "jpeg"
	default:
		return fmt.Errorf("--format must be 'png' or 'jpg'/'jpeg' (got %q)", cfg.format)
	}
	if cfg.quality < 1 || cfg.quality > 100 {
		return fmt.Errorf("--quality must be between 1 and 100 (got %d)", cfg.quality)
	}
	if cfg.maxDim < 0 {
		return fmt.Errorf("--max-dim must be >= 0 (got %d)", cfg.maxDim)
	}
	if cfg.cols < 0 {
		return fmt.Errorf("--cols must be >= 0 (got %d)", cfg.cols)
	}
	cfg.audio = strings.ToLower(strings.TrimSpace(cfg.audio))
	if cfg.microphone {
		switch cfg.audio {
		case "", "mic":
			cfg.audio = "mic"
		case "system", "both":
			cfg.audio = "both"
		}
	}
	switch cfg.audio {
	case "", "mic", "system", "both":
	default:
		return fmt.Errorf("--audio must be 'mic', 'system', or 'both' (got %q)", cfg.audio)
	}
	cfg.microphone = cfg.audioMic()
	if cfg.transcript && !cfg.audioAny() {
		return errors.New("--transcript requires an audio track (--audio mic|system|both, or --microphone)")
	}
	if cfg.transcriptLocale != "" && !cfg.transcript {
		return errors.New("--transcript-locale requires --transcript")
	}
	absOutput, err := filepath.Abs(cfg.output)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	cfg.output = absOutput
	return nil
}

func parseRegionSpec(raw string) (*regionSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return nil, fmt.Errorf("--region must be 'x,y,w,h' (got %q)", raw)
	}
	vals := [4]int{}
	for i, part := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("--region must contain integers (got %q in %q)", part, raw)
		}
		vals[i] = v
	}
	if vals[0] < 0 || vals[1] < 0 {
		return nil, fmt.Errorf("--region x and y must be >= 0 (got %d,%d)", vals[0], vals[1])
	}
	if vals[2] <= 0 || vals[3] <= 0 {
		return nil, fmt.Errorf("--region width and height must be > 0 (got %d,%d)", vals[2], vals[3])
	}
	return &regionSpec{X: vals[0], Y: vals[1], Width: vals[2], Height: vals[3]}, nil
}

func prepareOutputDir(cfg config) error {
	matches := []string{}
	for _, pattern := range generatedFilePatterns(cfg.output) {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		matches = append(matches, paths...)
	}
	if len(matches) == 0 {
		return nil
	}
	if !cfg.overwrite {
		return fmt.Errorf("output directory contains generated files; rerun with --overwrite or choose a new --output (first existing file: %s)", matches[0])
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove existing generated file %s: %w", path, err)
		}
	}
	return nil
}

func generatedFilePatterns(output string) []string {
	return []string{
		filepath.Join(output, "frame_*.png"),
		filepath.Join(output, "frame_*.jpg"),
		filepath.Join(output, "frame_*.jpeg"),
		filepath.Join(output, "spritesheet.png"),
		filepath.Join(output, "spritesheet.jpg"),
		filepath.Join(output, "spritesheet.jpeg"),
		filepath.Join(output, "spritesheet.json"),
		filepath.Join(output, "manifest.json"),
		filepath.Join(output, "audio.wav"),
		filepath.Join(output, "system_audio.wav"),
		filepath.Join(output, "transcript.txt"),
		filepath.Join(output, "transcript.json"),
		filepath.Join(output, "system_transcript.txt"),
		filepath.Join(output, "system_transcript.json"),
	}
}

func imageExtension(format string) string {
	if format == "jpeg" {
		return ".jpg"
	}
	return ".png"
}

func fitToMaxDim(img *image.RGBA, maxDim int) *image.RGBA {
	if maxDim <= 0 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxDim {
		return img
	}
	scale := float64(maxDim) / float64(longest)
	nw := int(math.Round(float64(w) * scale))
	nh := int(math.Round(float64(h) * scale))
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + int(float64(y)*float64(h)/float64(nh))
		if sy >= b.Max.Y {
			sy = b.Max.Y - 1
		}
		for x := 0; x < nw; x++ {
			sx := b.Min.X + int(float64(x)*float64(w)/float64(nw))
			if sx >= b.Max.X {
				sx = b.Max.X - 1
			}
			dst.SetRGBA(x, y, img.RGBAAt(sx, sy))
		}
	}
	return dst
}

func describeFile(path, kind string, index, width, height int) (outputFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return outputFile{}, err
	}
	return outputFile{
		Path:   path,
		Type:   kind,
		Index:  index,
		Width:  width,
		Height: height,
		Bytes:  info.Size(),
	}, nil
}

func writeManifest(path string, manifest captureManifest) error {
	return writeJSONFile(config{overwrite: true}, path, manifest, false)
}

func writeJSONFile(cfg config, path string, value any, indent bool) error {
	f, err := openOutputFile(path, cfg.overwrite)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if indent {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(value)
}

func plannedFrameCount(fps float64, duration time.Duration, maxFrames int) int {
	targetFrames := 0
	if duration > 0 {
		targetFrames = int(math.Round(fps * duration.Seconds()))
		if targetFrames < 1 {
			targetFrames = 1
		}
	}
	if maxFrames > 0 && (targetFrames == 0 || maxFrames < targetFrames) {
		targetFrames = maxFrames
	}
	return targetFrames
}

func runCaptureLoop(targetFrames int, ticks <-chan time.Time, stopCh <-chan os.Signal, captureOne func() (bool, error)) (bool, error) {
	capturedFrames := 0
	for {
		captured, err := captureOne()
		if err != nil {
			return false, err
		}
		if captured {
			capturedFrames++
			if targetFrames > 0 && capturedFrames >= targetFrames {
				return false, nil
			}
		}

		select {
		case <-stopCh:
			return true, nil
		case <-ticks:
		}
	}
}

func windowSourceStillExists(src Source) bool {
	if src.Kind != SourceKindWindow {
		return false
	}
	id, err := parseWindowID(strings.TrimPrefix(src.ID, SourceKindWindow+":"))
	return err == nil && macWindowExists(id)
}

func captureCLIFrame(src Source, capture func() (*image.RGBA, error), stillExists func(Source) bool) (*image.RGBA, bool, error) {
	img, err := capture()
	if err == nil {
		return img, true, nil
	}
	if src.Kind == SourceKindWindow && stillExists(src) {
		return nil, false, nil
	}
	return nil, false, err
}

func firstLastFile(files []outputFile) (string, string) {
	if len(files) == 0 {
		return "", ""
	}
	return files[0].Path, files[len(files)-1].Path
}

func firstOutputDimensions(files []outputFile, sheet *sheetMeta) (int, int) {
	for _, file := range files {
		if file.Width > 0 && file.Height > 0 {
			return file.Width, file.Height
		}
	}
	if sheet != nil {
		return sheet.FrameWidth, sheet.FrameHeight
	}
	return 0, 0
}

func summaryFromManifest(m captureManifest) captureSummary {
	return captureSummary{
		OK:               m.OK,
		Partial:          m.Partial,
		Version:          m.Version,
		Source:           m.Source,
		Mode:             m.Mode,
		Output:           m.Output,
		ManifestPath:     m.ManifestPath,
		Format:           m.Format,
		Quality:          m.Quality,
		FPS:              m.FPS,
		Duration:         m.Duration,
		RequestedFrames:  m.RequestedFrames,
		CapturedFrames:   m.CapturedFrames,
		Region:           m.Region,
		MaxDim:           m.MaxDim,
		FrameWidth:       m.FrameWidth,
		FrameHeight:      m.FrameHeight,
		FirstFile:        m.FirstFile,
		LastFile:         m.LastFile,
		ElapsedSeconds:   m.ElapsedSeconds,
		Spritesheet:      m.Spritesheet,
		Audio:            m.Audio,
		SystemAudio:      m.SystemAudio,
		Transcript:       m.Transcript,
		SystemTranscript: m.SystemTranscript,
	}
}

func run(cfg config) error {
	if err := normalizeConfig(&cfg); err != nil {
		return err
	}
	resolvedTranscriptLocale := cfg.transcriptLocale
	var transcriptPreflightErr error
	if cfg.transcript {
		locale, err := prepareTranscription(cfg.transcriptLocale)
		resolvedTranscriptLocale = locale
		if err != nil {
			if !errors.Is(err, errTranscriptionUnavailable) {
				return err
			}
			transcriptPreflightErr = err
		}
	}
	src, err := resolveCLISource(cfg)
	if err != nil {
		return err
	}
	region, err := parseRegionSpec(cfg.region)
	if err != nil {
		return err
	}
	if region != nil && (region.X+region.Width > src.Width || region.Y+region.Height > src.Height) {
		return fmt.Errorf("--region %d,%d,%d,%d exceeds source bounds %dx%d", region.X, region.Y, region.Width, region.Height, src.Width, src.Height)
	}
	outputMode := os.FileMode(0o755)
	if cfg.audioAny() {
		outputMode = 0o700
	}
	if err := os.MkdirAll(cfg.output, outputMode); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if cfg.audioAny() {
		if err := os.Chmod(cfg.output, 0o700); err != nil {
			return fmt.Errorf("secure output dir: %w", err)
		}
	}
	if err := prepareOutputDir(cfg); err != nil {
		return err
	}

	var mic, sys *audioRecorder
	if cfg.audioMic() {
		mic, err = startMicrophoneRecorder(filepath.Join(cfg.output, "audio.wav"))
		if err != nil {
			return err
		}
	}
	if cfg.audioSystem() {
		sys, err = startSystemAudioRecorder(filepath.Join(cfg.output, "system_audio.wav"), src)
		if err != nil {
			_, _ = mic.Stop()
			return err
		}
	}
	audioStopped := false
	defer func() {
		if !audioStopped {
			_, _ = mic.Stop()
			_, _ = sys.Stop()
		}
	}()

	interval := time.Duration(float64(time.Second) / cfg.fps)

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopCh)

	// When duration is set, capture a deterministic frame count: round(fps * seconds).
	// duration acts as the natural stopping condition and avoids ticker/deadline races.
	targetFrames := plannedFrameCount(cfg.fps, cfg.duration, cfg.frames)

	logf := func(format string, args ...any) {
		if !cfg.json {
			fmt.Fprintf(os.Stderr, format, args...)
		}
	}
	logf("screengrab: source=%s name=%q size=%dx%d fps=%v interval=%v mode=%s format=%s output=%s\n",
		src.ID, src.Name, src.Width, src.Height, cfg.fps, interval, cfg.mode, cfg.format, cfg.output)
	if targetFrames > 0 {
		logf("screengrab: will capture %d frames or stop on Ctrl+C\n", targetFrames)
	} else {
		logf("screengrab: recording... press Ctrl+C to stop\n")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	frames := []*image.RGBA{} // only used by spritesheet mode
	files := []outputFile{}
	timeline := []frameTiming{}
	frameCount := 0
	start := time.Now()

	waitingForWindow := false
	captureOne := func() (bool, error) {
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
		capture := func() (*image.RGBA, error) {
			if region != nil {
				return captureSourceRegion(src, region.X, region.Y, region.Width, region.Height)
			}
			return captureSource(src)
		}
		img, captured, err := captureCLIFrame(src, capture, windowSourceStillExists)
		if err != nil {
			return false, err
		}
		if !captured {
			if !waitingForWindow {
				logf("screengrab: waiting for %s; switch to its macOS Space if it is not visible\n", src.ID)
				waitingForWindow = true
			}
			return false, nil
		}
		if waitingForWindow {
			logf("screengrab: %s is live; capture resumed\n", src.ID)
			waitingForWindow = false
		}
		img = fitToMaxDim(img, cfg.maxDim)
		if file, err := handleFrame(cfg, img, frameCount, &frames); err != nil {
			return false, err
		} else if file != nil {
			file.CaptureOffsetSeconds = &captureOffset
			file.AudioOffsetSeconds = audioOffset
			file.SystemAudioOffsetSeconds = sysOffset
			files = append(files, *file)
		}
		timeline = append(timeline, frameTiming{Index: frameCount, CaptureOffsetSeconds: captureOffset, AudioOffsetSeconds: audioOffset, SystemAudioOffsetSeconds: sysOffset})
		frameCount++
		return true, nil
	}

	// The loop attempts one frame immediately at t=0 so a short duration still
	// yields frames. A still-existing window can temporarily have no compositor
	// frame while it is on another Space; those misses do not consume the target.
	stopped, captureErr := runCaptureLoop(targetFrames, ticker.C, stopCh, captureOne)
	if stopped {
		logf("screengrab: SIGINT received, stopping\n")
	}

	finished := time.Now()
	elapsed := finished.Sub(start)
	logf("screengrab: captured %d frames in %v\n", frameCount, elapsed.Round(time.Millisecond))

	audio, audioErr := finishAudio(mic, timeline, "audio", micOffset)
	systemAudio, sysAudioErr := finishAudio(sys, timeline, "system audio", systemOffset)
	audioStopped = true

	var transcript, systemTranscript *transcriptArtifact
	var transcriptErr error
	if cfg.transcript {
		var micErr, sysErr error
		transcript, micErr = produceTranscript(cfg.output, "transcript", audio, resolvedTranscriptLocale, transcriptPreflightErr)
		systemTranscript, sysErr = produceTranscript(cfg.output, "system_transcript", systemAudio, resolvedTranscriptLocale, transcriptPreflightErr)
		transcriptErr = errors.Join(micErr, sysErr)
		if transcriptErr != nil {
			logf("screengrab: transcription incomplete: %v\n", transcriptErr)
		}
	}

	var sheet *sheetMeta
	if cfg.mode == "spritesheet" && captureErr == nil {
		if frameCount == 0 {
			captureErr = errors.New("no frames captured; nothing to composite")
		} else {
			meta, sheetFiles, sheetErr := writeSpritesheet(cfg, frames)
			if sheetErr != nil {
				captureErr = fmt.Errorf("write spritesheet: %w", sheetErr)
			} else {
				sheet = &meta
				files = append(files, sheetFiles...)
			}
		}
	}
	ok := captureErr == nil && audioErr == nil && sysAudioErr == nil && transcriptErr == nil
	manifest := captureManifest{
		OK:               ok,
		Partial:          !ok && (frameCount > 0 || audio != nil || systemAudio != nil),
		Version:          2,
		Source:           src,
		Mode:             cfg.mode,
		Output:           cfg.output,
		ManifestPath:     filepath.Join(cfg.output, "manifest.json"),
		Format:           cfg.format,
		FPS:              cfg.fps,
		RequestedFrames:  targetFrames,
		CapturedFrames:   frameCount,
		Region:           region,
		MaxDim:           cfg.maxDim,
		StartedAt:        start.Format(time.RFC3339Nano),
		FinishedAt:       finished.Format(time.RFC3339Nano),
		ElapsedSeconds:   elapsed.Seconds(),
		Files:            files,
		FrameTimeline:    timeline,
		Spritesheet:      sheet,
		Audio:            audio,
		SystemAudio:      systemAudio,
		Transcript:       transcript,
		SystemTranscript: systemTranscript,
	}
	manifest.FirstFile, manifest.LastFile = firstLastFile(files)
	manifest.FrameWidth, manifest.FrameHeight = firstOutputDimensions(files, sheet)
	if cfg.duration > 0 {
		manifest.Duration = cfg.duration.String()
	}
	if cfg.format == "jpeg" {
		manifest.Quality = cfg.quality
	}
	if err := writeManifest(manifest.ManifestPath, manifest); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if cfg.audioAny() {
		if err := os.Chmod(manifest.ManifestPath, 0o600); err != nil {
			return fmt.Errorf("secure manifest: %w", err)
		}
	}
	if cfg.json {
		if err := json.NewEncoder(os.Stdout).Encode(summaryFromManifest(manifest)); err != nil {
			return err
		}
	}
	if captureErr != nil {
		return captureErr
	}
	if audioErr != nil {
		return audioErr
	}
	return sysAudioErr
}

func captureFrame(display int) (*image.RGBA, error) {
	img, err := screenshot.CaptureDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("capture display %d: %w", display, err)
	}
	return img, nil
}

// captureRegion grabs the rectangle (x, y, w, h) on the given display, where
// (x, y) are expressed in display-local coordinates (0,0 = top-left of that
// display). w or h <= 0 captures the full display.
func captureRegion(display, x, y, w, h int) (*image.RGBA, error) {
	if w <= 0 || h <= 0 {
		return captureFrame(display)
	}
	bounds := screenshot.GetDisplayBounds(display)
	rx := bounds.Min.X + x
	ry := bounds.Min.Y + y
	img, err := screenshot.Capture(rx, ry, w, h)
	if err != nil {
		return nil, fmt.Errorf("capture region display=%d rect=(%d,%d %dx%d): %w",
			display, rx, ry, w, h, err)
	}
	return img, nil
}

func handleFrame(cfg config, img *image.RGBA, index int, frames *[]*image.RGBA) (*outputFile, error) {
	switch cfg.mode {
	case "frames":
		path := filepath.Join(cfg.output, fmt.Sprintf("frame_%04d%s", index, imageExtension(cfg.format)))
		if err := writeImage(path, img, cfg); err != nil {
			return nil, err
		}
		file, err := describeFile(path, "frame", index, img.Bounds().Dx(), img.Bounds().Dy())
		if err != nil {
			return nil, err
		}
		return &file, nil
	case "spritesheet":
		*frames = append(*frames, img)
		return nil, nil
	}
	return nil, nil
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

func writeImage(path string, img image.Image, cfg config) error {
	f, err := openOutputFile(path, cfg.overwrite)
	if err != nil {
		return err
	}
	defer f.Close()
	switch cfg.format {
	case "png":
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		return enc.Encode(f, img)
	case "jpeg":
		return jpeg.Encode(f, img, &jpeg.Options{Quality: cfg.quality})
	default:
		return fmt.Errorf("unsupported image format %q", cfg.format)
	}
}

func openOutputFile(path string, overwrite bool) (*os.File, error) {
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%s already exists; pass --overwrite to replace it", path)
		}
		return nil, err
	}
	return f, nil
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
	Format      string  `json:"format"`
	Quality     int     `json:"quality,omitempty"`
}

func writeSpritesheet(cfg config, frames []*image.RGBA) (sheetMeta, []outputFile, error) {
	n := len(frames)
	if n == 0 {
		return sheetMeta{}, nil, errors.New("no frames")
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

	sheetPath := filepath.Join(cfg.output, "spritesheet"+imageExtension(cfg.format))
	if err := writeImage(sheetPath, sheet, cfg); err != nil {
		return sheetMeta{}, nil, err
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
	meta.Format = cfg.format
	if cfg.format == "jpeg" {
		meta.Quality = cfg.quality
	}

	metaPath := filepath.Join(cfg.output, "spritesheet.json")
	if err := writeJSONFile(cfg, metaPath, meta, true); err != nil {
		return sheetMeta{}, nil, err
	}

	if !cfg.json {
		fmt.Fprintf(os.Stderr, "screengrab: wrote %s (%dx%d, %d frames in %dx%d grid)\n",
			sheetPath, meta.SheetWidth, meta.SheetHeight, n, cols, rows)
	}
	sheetFile, err := describeFile(sheetPath, "spritesheet", 0, meta.SheetWidth, meta.SheetHeight)
	if err != nil {
		return sheetMeta{}, nil, err
	}
	metaFile, err := describeFile(metaPath, "spritesheet_metadata", 0, 0, 0)
	if err != nil {
		return sheetMeta{}, nil, err
	}
	return meta, []outputFile{sheetFile, metaFile}, nil
}
