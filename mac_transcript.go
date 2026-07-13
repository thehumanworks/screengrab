//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework Speech

#import <Foundation/Foundation.h>
#import <Speech/Speech.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    int code;
    char *locale;
    char *text;
    char *json;
    char *err;
} sg_speech_result;

static char *sg_speech_copy_string(NSString *s) {
    if (s == nil) return NULL;
    const char *utf8 = [s UTF8String];
    if (utf8 == NULL) return NULL;
    size_t n = strlen(utf8);
    char *out = (char *)malloc(n + 1);
    if (out != NULL) memcpy(out, utf8, n + 1);
    return out;
}

static sg_speech_result sg_speech_empty(void) {
    sg_speech_result result = {0, NULL, NULL, NULL, NULL};
    return result;
}

static NSString *sg_speech_locale(const char *requested) {
    if (requested != NULL && requested[0] != '\0') {
        return [NSString stringWithUTF8String:requested];
    }
    return NSLocale.currentLocale.localeIdentifier;
}

static sg_speech_result sg_speech_prepare(const char *requested_locale) {
    @autoreleasepool {
        sg_speech_result result = sg_speech_empty();
        __block SFSpeechRecognizerAuthorizationStatus status = SFSpeechRecognizer.authorizationStatus;
        if (status == SFSpeechRecognizerAuthorizationStatusNotDetermined) {
            dispatch_semaphore_t sem = dispatch_semaphore_create(0);
            [SFSpeechRecognizer requestAuthorization:^(SFSpeechRecognizerAuthorizationStatus newStatus) {
                status = newStatus;
                dispatch_semaphore_signal(sem);
            }];
            long wait_result = dispatch_semaphore_wait(
                sem,
                dispatch_time(DISPATCH_TIME_NOW, (int64_t)(60 * NSEC_PER_SEC))
            );
            if (wait_result != 0) {
                result.err = sg_speech_copy_string(@"speech-recognition permission request timed out");
                return result;
            }
        }
        if (status != SFSpeechRecognizerAuthorizationStatusAuthorized) {
            result.err = sg_speech_copy_string(@"speech-recognition permission was not granted");
            return result;
        }

        NSString *localeID = sg_speech_locale(requested_locale);
        SFSpeechRecognizer *recognizer = [[SFSpeechRecognizer alloc] initWithLocale:[NSLocale localeWithLocaleIdentifier:localeID]];
        result.locale = sg_speech_copy_string(localeID);
        if (recognizer == nil || !recognizer.supportsOnDeviceRecognition) {
            result.code = 2;
            result.err = sg_speech_copy_string(@"on-device recognition is unavailable for the requested locale");
            return result;
        }
        result.code = 1;
        return result;
    }
}

static sg_speech_result sg_speech_transcribe(const char *audio_path, const char *requested_locale) {
    @autoreleasepool {
        sg_speech_result prepared = sg_speech_prepare(requested_locale);
        if (prepared.code != 1) return prepared;

        NSString *localeID = [NSString stringWithUTF8String:prepared.locale];
        SFSpeechRecognizer *recognizer = [[SFSpeechRecognizer alloc] initWithLocale:[NSLocale localeWithLocaleIdentifier:localeID]];
        NSString *audioPath = [NSString stringWithUTF8String:audio_path];
        NSURL *audioURL = [NSURL fileURLWithPath:audioPath];
        SFSpeechURLRecognitionRequest *request = [[SFSpeechURLRecognitionRequest alloc] initWithURL:audioURL];
        request.shouldReportPartialResults = NO;
        request.requiresOnDeviceRecognition = YES;

        __block SFTranscription *transcription = nil;
        __block NSError *recognitionError = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        __block SFSpeechRecognitionTask *task = nil;
        task = [recognizer recognitionTaskWithRequest:request resultHandler:^(SFSpeechRecognitionResult *recognitionResult, NSError *error) {
            if (recognitionResult.isFinal) {
                transcription = recognitionResult.bestTranscription;
                dispatch_semaphore_signal(sem);
            } else if (error != nil) {
                recognitionError = error;
                dispatch_semaphore_signal(sem);
            }
        }];

        long wait_result = dispatch_semaphore_wait(
            sem,
            dispatch_time(DISPATCH_TIME_NOW, (int64_t)(10 * 60 * NSEC_PER_SEC))
        );
        if (wait_result != 0) {
            [task cancel];
            prepared.code = 0;
            prepared.err = sg_speech_copy_string(@"on-device transcription timed out");
            return prepared;
        }
        if (transcription == nil) {
            prepared.code = 0;
            prepared.err = sg_speech_copy_string(recognitionError.localizedDescription ?: @"speech recognizer returned no transcript");
            return prepared;
        }

        NSMutableArray *segments = [NSMutableArray arrayWithCapacity:transcription.segments.count];
        for (SFTranscriptionSegment *segment in transcription.segments) {
            [segments addObject:@{
                @"start_seconds": @(segment.timestamp),
                @"end_seconds": @(segment.timestamp + segment.duration),
                @"text": segment.substring ?: @"",
                @"confidence": @(segment.confidence),
            }];
        }
        NSDictionary *document = @{
            @"version": @1,
            @"status": @"complete",
            @"locale": localeID,
            @"text": transcription.formattedString ?: @"",
            @"segments": segments,
        };
        NSError *jsonError = nil;
        NSData *jsonData = [NSJSONSerialization dataWithJSONObject:document options:NSJSONWritingSortedKeys error:&jsonError];
        if (jsonData == nil) {
            prepared.code = 0;
            prepared.err = sg_speech_copy_string(jsonError.localizedDescription ?: @"could not serialize transcript");
            return prepared;
        }

        prepared.code = 1;
        prepared.text = sg_speech_copy_string(transcription.formattedString ?: @"");
        prepared.json = sg_speech_copy_string([[NSString alloc] initWithData:jsonData encoding:NSUTF8StringEncoding]);
        return prepared;
    }
}

static void sg_speech_free_result(sg_speech_result result) {
    if (result.locale != NULL) free(result.locale);
    if (result.text != NULL) free(result.text);
    if (result.json != NULL) free(result.json);
    if (result.err != NULL) free(result.err);
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

func preparePlatformTranscription(locale string) (string, error) {
	cLocale := C.CString(locale)
	defer C.free(unsafe.Pointer(cLocale))
	result := C.sg_speech_prepare(cLocale)
	defer C.sg_speech_free_result(result)
	resolved := locale
	if result.locale != nil {
		resolved = C.GoString(result.locale)
	}
	if result.code == 2 {
		return resolved, fmt.Errorf("%w: %s", errTranscriptionUnavailable, C.GoString(result.err))
	}
	if result.code != 1 {
		message := "unknown Speech framework error"
		if result.err != nil {
			message = C.GoString(result.err)
		}
		return resolved, fmt.Errorf("prepare transcription: %s", message)
	}
	return resolved, nil
}

func transcribePlatformAudio(audioPath, locale string) (transcriptDocument, error) {
	cPath := C.CString(audioPath)
	cLocale := C.CString(locale)
	defer C.free(unsafe.Pointer(cPath))
	defer C.free(unsafe.Pointer(cLocale))
	result := C.sg_speech_transcribe(cPath, cLocale)
	defer C.sg_speech_free_result(result)
	resolved := locale
	if result.locale != nil {
		resolved = C.GoString(result.locale)
	}
	if result.code == 2 {
		return transcriptDocument{Locale: resolved}, fmt.Errorf("%w: %s", errTranscriptionUnavailable, C.GoString(result.err))
	}
	if result.code != 1 || result.json == nil {
		message := "unknown Speech framework error"
		if result.err != nil {
			message = C.GoString(result.err)
		}
		return transcriptDocument{Locale: resolved}, fmt.Errorf("transcribe audio: %s", message)
	}
	var doc transcriptDocument
	if err := json.Unmarshal([]byte(C.GoString(result.json)), &doc); err != nil {
		return transcriptDocument{Locale: resolved}, fmt.Errorf("decode native transcript: %w", err)
	}
	return doc, nil
}
