//go:build !darwin

package main

import "errors"

var errSystemAudioUnsupported = errors.New("system audio recording is only implemented on darwin")

type platformSystemAudio struct{}

func startPlatformSystemAudio(path string, src Source) (*platformSystemAudio, error) {
	return nil, errSystemAudioUnsupported
}

func (r *platformSystemAudio) currentTime() float64 { return 0 }

func (r *platformSystemAudio) stop() (float64, error) { return 0, errSystemAudioUnsupported }
