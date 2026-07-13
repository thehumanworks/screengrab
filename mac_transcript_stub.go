//go:build !darwin

package main

import "fmt"

func preparePlatformTranscription(locale string) (string, error) {
	return locale, fmt.Errorf("%w: transcription is only implemented on darwin", errTranscriptionUnavailable)
}

func transcribePlatformAudio(audioPath, locale string) (transcriptDocument, error) {
	return transcriptDocument{Locale: locale}, fmt.Errorf("%w: transcription is only implemented on darwin", errTranscriptionUnavailable)
}
