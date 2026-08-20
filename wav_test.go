package main

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writeTestWAV(t *testing.T, path string, sampleRate, channels int, seconds float64, declaredDataLen int64) []byte {
	t.Helper()
	frames := int(float64(sampleRate) * seconds)
	blockAlign := channels * 2
	data := make([]byte, frames*blockAlign)
	for i := 0; i < frames; i++ {
		sample := int16(10000 * math.Sin(2*math.Pi*440*float64(i)/float64(sampleRate)))
		for c := 0; c < channels; c++ {
			binary.LittleEndian.PutUint16(data[(i*channels+c)*2:], uint16(sample))
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test wav: %v", err)
	}
	defer f.Close()
	format := wavFormat{Channels: channels, SampleRate: sampleRate, BitsPerSample: 16, BlockAlign: blockAlign}
	if declaredDataLen < 0 {
		declaredDataLen = int64(len(data))
	}
	if err := writeWAVHeader(f, format, declaredDataLen); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("write data: %v", err)
	}
	return data
}

func TestSplitWAVForTranscription(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.wav")
	data := writeTestWAV(t, path, 8000, 1, 2.5, -1)

	chunks, err := splitWAVForTranscription(path, 1.0)
	if err != nil {
		t.Fatalf("splitWAVForTranscription: %v", err)
	}
	defer func() {
		for _, c := range chunks {
			os.Remove(c.Path)
		}
	}()
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	wantOffsets := []float64{0, 1, 2}
	total := []byte{}
	for i, chunk := range chunks {
		if math.Abs(chunk.OffsetSeconds-wantOffsets[i]) > 1e-9 {
			t.Fatalf("chunk %d offset = %v, want %v", i, chunk.OffsetSeconds, wantOffsets[i])
		}
		format, err := readWAVFormat(chunk.Path)
		if err != nil {
			t.Fatalf("chunk %d unreadable: %v", i, err)
		}
		if format.SampleRate != 8000 || format.Channels != 1 || format.BitsPerSample != 16 {
			t.Fatalf("chunk %d format = %+v", i, format)
		}
		raw, err := os.ReadFile(chunk.Path)
		if err != nil {
			t.Fatalf("read chunk %d: %v", i, err)
		}
		total = append(total, raw[format.DataOffset:format.DataOffset+format.DataSize]...)
		info, err := os.Stat(chunk.Path)
		if err != nil {
			t.Fatalf("stat chunk %d: %v", i, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("chunk %d mode = %v, want 0600", i, info.Mode().Perm())
		}
	}
	if len(total) != len(data) {
		t.Fatalf("chunks carry %d data bytes, source has %d", len(total), len(data))
	}
	for i := range total {
		if total[i] != data[i] {
			t.Fatalf("chunk data diverges from source at byte %d", i)
		}
	}
}

func TestSplitWAVForTranscriptionRejectsShortAudio(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.wav")
	writeTestWAV(t, path, 8000, 1, 0.5, -1)
	if _, err := splitWAVForTranscription(path, 1.0); !errors.Is(err, errWAVTooShortToSplit) {
		t.Fatalf("expected errWAVTooShortToSplit, got %v", err)
	}
}

func TestReadWAVFormatClampsUnfinalizedHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unfinalized.wav")
	// A writer that died before finalizing leaves a zero-length data chunk
	// in the header even though the samples are on disk.
	data := writeTestWAV(t, path, 8000, 1, 1.0, 0)
	format, err := readWAVFormat(path)
	if err != nil {
		t.Fatalf("readWAVFormat: %v", err)
	}
	if format.DataSize != int64(len(data)) {
		t.Fatalf("DataSize = %d, want %d", format.DataSize, len(data))
	}
	if got := format.durationSeconds(); math.Abs(got-1.0) > 1e-6 {
		t.Fatalf("duration = %v, want 1.0", got)
	}
}

func TestMergeTranscriptDocuments(t *testing.T) {
	docs := []transcriptDocument{
		{
			Locale: "en-US",
			Text:   "hello there",
			Segments: []transcriptSegment{
				{StartSeconds: 0.5, EndSeconds: 1.2, Text: "hello"},
				{StartSeconds: 1.4, EndSeconds: 2.0, Text: "there"},
			},
		},
		{
			Locale:   "en-US",
			Text:     "",
			Segments: []transcriptSegment{},
		},
		{
			Locale: "en-US",
			Text:   "again",
			Segments: []transcriptSegment{
				{StartSeconds: 0.3, EndSeconds: 0.9, Text: "again"},
			},
		},
	}
	merged := mergeTranscriptDocuments(docs, []float64{0, 60, 120})
	if merged.Text != "hello there again" {
		t.Fatalf("merged text = %q", merged.Text)
	}
	if merged.Locale != "en-US" || merged.Status != "complete" || merged.Version != 1 {
		t.Fatalf("merged metadata = %+v", merged)
	}
	if len(merged.Segments) != 3 {
		t.Fatalf("merged segments = %d, want 3", len(merged.Segments))
	}
	if merged.Segments[2].StartSeconds != 120.3 || merged.Segments[2].EndSeconds != 120.9 {
		t.Fatalf("offset not applied: %+v", merged.Segments[2])
	}
	if err := validateTranscriptDocument(merged, 121.0); err != nil {
		t.Fatalf("merged document fails validation: %v", err)
	}
}
