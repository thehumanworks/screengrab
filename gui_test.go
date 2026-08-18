package main

import (
	"bytes"
	"encoding/json"
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

func TestCopySelectedCapturePreservesAssociatedArtifacts(t *testing.T) {
	src := t.TempDir()
	frames := make([]outputFile, 0, 3)
	for i := 0; i < 3; i++ {
		path := filepath.Join(src, "frame_"+padN(i)+".png")
		writeTinyPNG(t, path, color.RGBA{R: uint8(30 * i), G: 20, B: 40, A: 255})
		captureOffset := float64(i) * 0.5
		audioOffset := captureOffset + 0.03
		systemAudioOffset := captureOffset + 0.01
		frames = append(frames, outputFile{
			Path:                     path,
			Type:                     "frame",
			Index:                    i,
			CaptureOffsetSeconds:     &captureOffset,
			AudioOffsetSeconds:       &audioOffset,
			SystemAudioOffsetSeconds: &systemAudioOffset,
		})
	}
	audioPath := filepath.Join(src, "audio.wav")
	systemAudioPath := filepath.Join(src, "system_audio.wav")
	textPath := filepath.Join(src, "transcript.txt")
	jsonPath := filepath.Join(src, "transcript.json")
	systemJSONPath := filepath.Join(src, "system_transcript.json")
	for path, contents := range map[string]string{
		audioPath:       "audio",
		systemAudioPath: "system audio",
		textPath:        "hello world\n",
		jsonPath:        `{"version":1,"status":"complete"}`,
		systemJSONPath:  `{"version":1,"status":"complete"}`,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := captureManifest{
		OK:               true,
		Version:          2,
		Output:           src,
		ManifestPath:     filepath.Join(src, "manifest.json"),
		CapturedFrames:   len(frames),
		Files:            frames,
		FrameTimeline:    frameTimelineFromFiles(frames),
		Audio:            &audioArtifact{Path: audioPath, Format: "wav", Codec: "pcm-s16le", Status: "complete", DurationSeconds: 2},
		SystemAudio:      &audioArtifact{Path: systemAudioPath, Format: "wav", Codec: "pcm-s16le", Status: "complete", DurationSeconds: 2},
		Transcript:       &transcriptArtifact{TextPath: textPath, JSONPath: jsonPath, Locale: "en-GB", Status: "complete"},
		SystemTranscript: &transcriptArtifact{JSONPath: systemJSONPath, Locale: "en-GB", Status: "complete"},
	}

	dest := filepath.Join(t.TempDir(), "selected")
	if err := copySelectedCapture(manifest, []int{0, 2}, dest); err != nil {
		t.Fatalf("copySelectedCapture: %v", err)
	}
	for _, name := range []string{"frame_0000.png", "frame_0001.png", "audio.wav", "system_audio.wav", "transcript.txt", "transcript.json", "system_transcript.json", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dest, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got captureManifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode selected manifest: %v", err)
	}
	if got.Version != 2 || got.CapturedFrames != 2 || len(got.Files) != 2 {
		t.Fatalf("selected manifest = version %d, frames %d/%d", got.Version, got.CapturedFrames, len(got.Files))
	}
	if got.Files[0].Index != 0 || got.Files[1].Index != 2 {
		t.Fatalf("selected source indices = %d,%d, want 0,2", got.Files[0].Index, got.Files[1].Index)
	}
	if len(got.FrameTimeline) != 2 || got.FrameTimeline[1].Index != 2 {
		t.Fatalf("selected frame timeline = %+v", got.FrameTimeline)
	}
	if got.Files[1].AudioOffsetSeconds == nil || *got.Files[1].AudioOffsetSeconds != 1.03 {
		t.Fatalf("selected audio offset was not preserved: %+v", got.Files[1].AudioOffsetSeconds)
	}
	if got.Files[1].SystemAudioOffsetSeconds == nil || *got.Files[1].SystemAudioOffsetSeconds != 1.01 {
		t.Fatalf("selected system audio offset was not preserved: %+v", got.Files[1].SystemAudioOffsetSeconds)
	}
	if got.Audio == nil || got.Audio.Path != filepath.Join(dest, "audio.wav") {
		t.Fatalf("selected audio association = %+v", got.Audio)
	}
	if got.SystemAudio == nil || got.SystemAudio.Path != filepath.Join(dest, "system_audio.wav") {
		t.Fatalf("selected system audio association = %+v", got.SystemAudio)
	}
	if got.Transcript == nil || got.Transcript.TextPath != filepath.Join(dest, "transcript.txt") {
		t.Fatalf("selected transcript association = %+v", got.Transcript)
	}
	if got.SystemTranscript == nil || got.SystemTranscript.JSONPath != filepath.Join(dest, "system_transcript.json") {
		t.Fatalf("selected system transcript association = %+v", got.SystemTranscript)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("selected private directory mode = %o, want 700", info.Mode().Perm())
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
