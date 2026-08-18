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

type platformAudioTap interface {
	currentTime() float64
	stop() (float64, error)
}

type audioRecorder struct {
	mu       sync.Mutex
	platform platformAudioTap
	path     string
	label    string
	stopped  bool
	artifact audioArtifact
	stopErr  error
}

func startMicrophoneRecorder(path string) (*audioRecorder, error) {
	return startAudioRecorder(path, "microphone", func() (platformAudioTap, error) {
		return startPlatformMicrophone(path)
	})
}

func startSystemAudioRecorder(path string, src Source) (*audioRecorder, error) {
	return startAudioRecorder(path, "system audio", func() (platformAudioTap, error) {
		return startPlatformSystemAudio(path, src)
	})
}

func startAudioRecorder(path, label string, start func() (platformAudioTap, error)) (*audioRecorder, error) {
	platform, err := start()
	if err != nil {
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil && !os.IsNotExist(chmodErr) {
			return nil, fmt.Errorf("%w; secure partial %s output %q: %v", err, label, path, chmodErr)
		}
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_, _ = platform.stop()
		return nil, fmt.Errorf("secure %s output %q: %w", label, path, err)
	}
	return &audioRecorder{platform: platform, path: path, label: label}, nil
}

func (r *audioRecorder) CurrentTime() float64 {
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

func (r *audioRecorder) Stop() (audioArtifact, error) {
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
		r.stopErr = fmt.Errorf("%s produced no audio samples", r.label)
	} else if info, statErr := os.Stat(r.path); statErr != nil || info.Size() == 0 {
		r.stopErr = statErr
		if r.stopErr == nil {
			r.stopErr = fmt.Errorf("%s output is empty", r.label)
		}
	}
	if r.stopErr != nil {
		r.artifact.Status = "failed"
		r.artifact.Error = r.stopErr.Error()
	}
	return r.artifact, r.stopErr
}

// finishAudio stops a recorder and cross-checks the frame timeline against the
// recorded duration so a clock that drifted or stalled is surfaced as a failed
// artifact instead of silently misaligned offsets.
func finishAudio(rec *audioRecorder, timeline []frameTiming, label string, offset func(frameTiming) *float64) (*audioArtifact, error) {
	if rec == nil {
		return nil, nil
	}
	artifact, err := rec.Stop()
	if err == nil {
		if verr := validateFrameTimeline(timeline, artifact.DurationSeconds, label, offset); verr != nil {
			err = verr
			artifact.Status = "failed"
			artifact.Error = verr.Error()
		}
	}
	return &artifact, err
}
