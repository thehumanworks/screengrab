package main

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIOffSpaceWindowWaitsAndResumes(t *testing.T) {
	ticks := make(chan time.Time, 2)
	ticks <- time.Now()
	ticks <- time.Now()
	stop := make(chan os.Signal)
	src := Source{ID: "window:0x1ebe", Kind: SourceKindWindow, OnScreen: false}

	attempts := 0
	existenceChecks := 0
	stopped, err := runCaptureLoop(2, ticks, stop, func() (bool, error) {
		_, captured, err := captureCLIFrame(src, func() (*image.RGBA, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("Failed to start stream due to audio/video capture failure")
			}
			return image.NewRGBA(image.Rect(0, 0, 2, 2)), nil
		}, func(Source) bool {
			existenceChecks++
			return true
		})
		if err != nil {
			return false, err
		}
		return captured, nil
	})
	if err != nil {
		t.Fatalf("runCaptureLoop: %v", err)
	}
	if stopped {
		t.Fatal("capture reported a signal stop")
	}
	if attempts != 3 {
		t.Fatalf("capture attempts = %d, want 3 (off-Space miss plus two live frames)", attempts)
	}
	if existenceChecks != 1 {
		t.Fatalf("window existence checks = %d, want 1 for the failed frame", existenceChecks)
	}
}

func TestCLICaptureLoopFailsWhenWindowCloses(t *testing.T) {
	want := errors.New("window capture failed")
	src := Source{ID: "window:0x1ebe", Kind: SourceKindWindow}
	_, err := runCaptureLoop(1, make(chan time.Time), make(chan os.Signal), func() (bool, error) {
		_, captured, err := captureCLIFrame(src, func() (*image.RGBA, error) {
			return nil, want
		}, func(Source) bool {
			return false
		})
		return captured, err
	})
	if !errors.Is(err, want) {
		t.Fatalf("runCaptureLoop error = %v, want %v", err, want)
	}
}

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

	transcriptWithoutMic := config{fps: 2, mode: "frames", format: "png", quality: 85, output: ".", transcript: true}
	if err := normalizeConfig(&transcriptWithoutMic); err == nil || !strings.Contains(err.Error(), "--microphone") {
		t.Fatalf("expected transcript without microphone to fail, got %v", err)
	}

	localeWithoutTranscript := config{fps: 2, mode: "frames", format: "png", quality: 85, output: ".", transcriptLocale: "en-GB"}
	if err := normalizeConfig(&localeWithoutTranscript); err == nil || !strings.Contains(err.Error(), "--transcript") {
		t.Fatalf("expected locale without transcript to fail, got %v", err)
	}
}

func TestNormalizeConfigAudioSelection(t *testing.T) {
	base := config{fps: 2, mode: "frames", format: "png", quality: 85, output: "."}

	micAlias := base
	micAlias.microphone = true
	if err := normalizeConfig(&micAlias); err != nil {
		t.Fatalf("normalizeConfig --microphone: %v", err)
	}
	if micAlias.audio != "mic" || !micAlias.audioMic() || micAlias.audioSystem() {
		t.Fatalf("--microphone alias resolved to audio=%q", micAlias.audio)
	}

	merged := base
	merged.microphone = true
	merged.audio = "system"
	if err := normalizeConfig(&merged); err != nil {
		t.Fatalf("normalizeConfig --microphone --audio system: %v", err)
	}
	if merged.audio != "both" || !merged.audioMic() || !merged.audioSystem() {
		t.Fatalf("--microphone + --audio system resolved to audio=%q", merged.audio)
	}

	systemOnly := base
	systemOnly.audio = "System"
	if err := normalizeConfig(&systemOnly); err != nil {
		t.Fatalf("normalizeConfig --audio system: %v", err)
	}
	if systemOnly.audio != "system" || systemOnly.audioMic() || !systemOnly.audioSystem() || systemOnly.microphone {
		t.Fatalf("--audio system resolved to audio=%q microphone=%v", systemOnly.audio, systemOnly.microphone)
	}

	invalid := base
	invalid.audio = "speakers"
	if err := normalizeConfig(&invalid); err == nil || !strings.Contains(err.Error(), "--audio") {
		t.Fatalf("expected invalid --audio value to fail, got %v", err)
	}

	transcriptWithSystem := base
	transcriptWithSystem.audio = "system"
	transcriptWithSystem.transcript = true
	if err := normalizeConfig(&transcriptWithSystem); err != nil {
		t.Fatalf("--transcript with --audio system should validate: %v", err)
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

func TestGeneratedFilePatternsIncludeAudioArtifacts(t *testing.T) {
	patterns := strings.Join(generatedFilePatterns("out"), "\n")
	for _, name := range []string{"audio.wav", "system_audio.wav", "transcript.txt", "transcript.json", "system_transcript.txt", "system_transcript.json"} {
		if !strings.Contains(patterns, name) {
			t.Fatalf("generated patterns do not include %s", name)
		}
	}
}

func TestTimedArtifactValidation(t *testing.T) {
	a := 0.02
	b := 0.52
	sa := 0.01
	sb := 0.51
	timeline := []frameTiming{
		{Index: 0, CaptureOffsetSeconds: 0, AudioOffsetSeconds: &a, SystemAudioOffsetSeconds: &sa},
		{Index: 1, CaptureOffsetSeconds: 0.5, AudioOffsetSeconds: &b, SystemAudioOffsetSeconds: &sb},
	}
	if err := validateFrameTimeline(timeline, 1, "audio", micOffset); err != nil {
		t.Fatalf("valid frame timeline: %v", err)
	}
	if err := validateFrameTimeline(timeline, 1, "system audio", systemOffset); err != nil {
		t.Fatalf("valid system audio timeline: %v", err)
	}
	bad := -0.1
	timeline[1].AudioOffsetSeconds = &bad
	if err := validateFrameTimeline(timeline, 1, "audio", micOffset); err == nil {
		t.Fatal("expected unordered/negative frame timeline to fail")
	}
	timeline[1].SystemAudioOffsetSeconds = nil
	if err := validateFrameTimeline(timeline, 1, "system audio", systemOffset); err == nil {
		t.Fatal("expected missing system audio offset to fail")
	}

	doc := transcriptDocument{Segments: []transcriptSegment{
		{StartSeconds: 0.1, EndSeconds: 0.3, Text: "hello"},
		{StartSeconds: 0.4, EndSeconds: 0.8, Text: "world"},
	}}
	if err := validateTranscriptDocument(doc, 1); err != nil {
		t.Fatalf("valid transcript: %v", err)
	}
	doc.Segments[1].StartSeconds = 0.05
	if err := validateTranscriptDocument(doc, 1); err == nil {
		t.Fatal("expected unordered transcript to fail")
	}
}
