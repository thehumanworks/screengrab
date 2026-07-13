//go:build !darwin

package main

import "errors"

var errMicrophoneUnsupported = errors.New("microphone recording is only implemented on darwin")

type platformMicrophone struct{}

func startPlatformMicrophone(path string) (*platformMicrophone, error) {
	return nil, errMicrophoneUnsupported
}

func (r *platformMicrophone) currentTime() float64 { return 0 }

func (r *platformMicrophone) stop() (float64, error) { return 0, errMicrophoneUnsupported }
