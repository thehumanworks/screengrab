package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errTranscriptionUnavailable = errors.New("on-device transcription is unavailable")

type transcriptSegment struct {
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Text         string  `json:"text"`
	Confidence   float32 `json:"confidence,omitempty"`
}

type transcriptDocument struct {
	Version  int                 `json:"version"`
	Status   string              `json:"status"`
	Locale   string              `json:"locale"`
	Text     string              `json:"text"`
	Segments []transcriptSegment `json:"segments"`
	Error    string              `json:"error,omitempty"`
}

type transcriptArtifact struct {
	TextPath string `json:"text_path,omitempty"`
	JSONPath string `json:"json_path"`
	Locale   string `json:"locale"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

func prepareTranscription(locale string) (string, error) {
	return preparePlatformTranscription(locale)
}

func transcribeAndWrite(audioPath, outputDir, locale string, audioDuration float64) (*transcriptArtifact, error) {
	doc, err := transcribePlatformAudio(audioPath, locale)
	if err == nil {
		err = validateTranscriptDocument(doc, audioDuration)
	}
	if err != nil {
		status := "failed"
		if errors.Is(err, errTranscriptionUnavailable) {
			status = "unavailable"
		}
		doc = transcriptDocument{Version: 1, Status: status, Locale: locale, Error: err.Error(), Segments: []transcriptSegment{}}
	}
	if doc.Version == 0 {
		doc.Version = 1
	}
	if doc.Status == "" {
		doc.Status = "complete"
	}
	if doc.Segments == nil {
		doc.Segments = []transcriptSegment{}
	}

	jsonPath := filepath.Join(outputDir, "transcript.json")
	if writeErr := writePrivateJSON(jsonPath, doc); writeErr != nil {
		return nil, writeErr
	}
	artifact := &transcriptArtifact{
		JSONPath: jsonPath,
		Locale:   doc.Locale,
		Status:   doc.Status,
		Error:    doc.Error,
	}
	if doc.Status == "complete" {
		textPath := filepath.Join(outputDir, "transcript.txt")
		if writeErr := os.WriteFile(textPath, []byte(doc.Text+"\n"), 0o600); writeErr != nil {
			return nil, fmt.Errorf("write transcript text: %w", writeErr)
		}
		artifact.TextPath = textPath
	}
	return artifact, err
}

func validateFrameTimeline(timeline []frameTiming, audioDuration float64) error {
	previous := -1.0
	for _, frame := range timeline {
		if frame.AudioOffsetSeconds == nil {
			return fmt.Errorf("frame %d has no audio offset", frame.Index)
		}
		offset := *frame.AudioOffsetSeconds
		if offset < previous {
			return fmt.Errorf("frame %d audio offset %.6f precedes the prior offset %.6f", frame.Index, offset, previous)
		}
		if offset < 0 || offset > audioDuration+0.5 {
			return fmt.Errorf("frame %d audio offset %.6f is outside audio duration %.6f", frame.Index, offset, audioDuration)
		}
		previous = offset
	}
	return nil
}

func validateTranscriptDocument(doc transcriptDocument, audioDuration float64) error {
	previous := -1.0
	for i, segment := range doc.Segments {
		if segment.StartSeconds < previous {
			return fmt.Errorf("transcript segment %d starts before the prior segment", i)
		}
		if segment.StartSeconds < 0 || segment.EndSeconds < segment.StartSeconds {
			return fmt.Errorf("transcript segment %d has invalid bounds", i)
		}
		if segment.EndSeconds > audioDuration+0.5 {
			return fmt.Errorf("transcript segment %d exceeds audio duration", i)
		}
		previous = segment.StartSeconds
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		f.Close()
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", path, err)
	}
	return nil
}
