package main

import (
	"fmt"
	"os"
	"sync"
)

type audioArtifact struct {
	Path            string  `json:"path"`
	Format          string  `json:"format"`
	Codec           string  `json:"codec"`
	DurationSeconds float64 `json:"duration_seconds"`
	Status          string  `json:"status"`
	Error           string  `json:"error,omitempty"`
}

type microphoneRecorder struct {
	mu       sync.Mutex
	platform *platformMicrophone
	path     string
	stopped  bool
	artifact audioArtifact
	stopErr  error
}

func startMicrophoneRecorder(path string) (*microphoneRecorder, error) {
	platform, err := startPlatformMicrophone(path)
	if err != nil {
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil && !os.IsNotExist(chmodErr) {
			return nil, fmt.Errorf("%w; secure partial microphone output %q: %v", err, path, chmodErr)
		}
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_, _ = platform.stop()
		return nil, fmt.Errorf("secure microphone output %q: %w", path, err)
	}
	return &microphoneRecorder{platform: platform, path: path}, nil
}

func (r *microphoneRecorder) CurrentTime() float64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return r.artifact.DurationSeconds
	}
	return r.platform.currentTime()
}

func (r *microphoneRecorder) Stop() (audioArtifact, error) {
	if r == nil {
		return audioArtifact{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return r.artifact, r.stopErr
	}
	duration, err := r.platform.stop()
	r.stopped = true
	r.artifact = audioArtifact{
		Path:            r.path,
		Format:          "wav",
		Codec:           "pcm-s16le",
		DurationSeconds: duration,
		Status:          "complete",
	}
	if err != nil {
		r.stopErr = err
	} else if duration <= 0 {
		r.stopErr = fmt.Errorf("microphone produced no audio samples")
	} else if info, statErr := os.Stat(r.path); statErr != nil || info.Size() == 0 {
		r.stopErr = statErr
		if r.stopErr == nil {
			r.stopErr = fmt.Errorf("microphone output is empty")
		}
	}
	if r.stopErr != nil {
		r.artifact.Status = "failed"
		r.artifact.Error = r.stopErr.Error()
	}
	return r.artifact, r.stopErr
}
