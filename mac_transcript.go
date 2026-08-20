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

// On-device recognition delivers a SEQUENCE of per-utterance final results —
// bestTranscription resets between them — so keeping only the transcription
// seen at the first isFinal callback truncates the transcript to a single
// utterance. The collector pins every result as it arrives and only lets the
// caller proceed when the task itself reports completion.
@interface SGSpeechCollector : NSObject <SFSpeechRecognitionTaskDelegate>
@property (nonatomic, strong) NSMutableArray<NSDictionary *> *segments;
@property (nonatomic, strong) NSMutableArray<NSString *> *parts;
@property (nonatomic, strong) dispatch_semaphore_t sem;
@property (nonatomic, strong) NSError *error;
@property (nonatomic) double baseOffset;
@property (nonatomic) double lastEnd;
@end

@implementation SGSpeechCollector

- (instancetype)init {
    self = [super init];
    if (self != nil) {
        _segments = [NSMutableArray array];
        _parts = [NSMutableArray array];
        _sem = dispatch_semaphore_create(0);
        _baseOffset = 0;
        _lastEnd = 0;
    }
    return self;
}

- (void)speechRecognitionTask:(SFSpeechRecognitionTask *)task
          didFinishRecognition:(SFSpeechRecognitionResult *)recognitionResult {
    (void)task;
    SFTranscription *transcription = recognitionResult.bestTranscription;
    if (transcription == nil || transcription.segments.count == 0) return;
    double first = transcription.segments.firstObject.timestamp;
    // The on-device recognizer restarts its timeline after long utterance
    // boundaries; when incoming timestamps rewind, rebase them after
    // everything already emitted so offsets stay absolute and monotonic.
    if (self.baseOffset + first + 0.05 < self.lastEnd) {
        self.baseOffset = self.lastEnd;
    }
    for (SFTranscriptionSegment *segment in transcription.segments) {
        double start = self.baseOffset + segment.timestamp;
        double end = start + segment.duration;
        if (end > self.lastEnd) self.lastEnd = end;
        [self.segments addObject:@{
            @"start_seconds": @(start),
            @"end_seconds": @(end),
            @"text": segment.substring ?: @"",
            @"confidence": @(segment.confidence),
        }];
    }
    if (transcription.formattedString.length > 0) {
        [self.parts addObject:transcription.formattedString];
    }
}

- (void)speechRecognitionTask:(SFSpeechRecognitionTask *)task didFinishSuccessfully:(BOOL)successfully {
    if (!successfully && self.error == nil && task.error != nil) {
        self.error = task.error;
    }
    dispatch_semaphore_signal(self.sem);
}

- (void)speechRecognitionTaskWasCancelled:(SFSpeechRecognitionTask *)task {
    (void)task;
    if (self.error == nil) {
        self.error = [NSError errorWithDomain:@"io.thehumanworks.screengrab.speech"
                                         code:1
                                     userInfo:@{NSLocalizedDescriptionKey: @"speech recognition task was cancelled"}];
    }
    dispatch_semaphore_signal(self.sem);
}

@end

static BOOL sg_speech_is_no_speech_error(NSError *error) {
    if (error == nil) return NO;
    return [error.domain isEqualToString:@"kAFAssistantErrorDomain"] && error.code == 1110;
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

        SGSpeechCollector *collector = [[SGSpeechCollector alloc] init];
        SFSpeechRecognitionTask *task = [recognizer recognitionTaskWithRequest:request delegate:collector];

        long wait_result = dispatch_semaphore_wait(
            collector.sem,
            dispatch_time(DISPATCH_TIME_NOW, (int64_t)(10 * 60 * NSEC_PER_SEC))
        );
        if (wait_result != 0) {
            [task cancel];
            prepared.code = 0;
            prepared.err = sg_speech_copy_string(@"on-device transcription timed out");
            return prepared;
        }

        // A track with no detectable speech (e.g. a silent system-audio
        // recording) is a valid empty transcript, not a failure; and a late
        // recognizer error must not discard the utterances already pinned.
        if (collector.segments.count == 0 && collector.error != nil &&
            !sg_speech_is_no_speech_error(collector.error)) {
            prepared.code = 0;
            prepared.err = sg_speech_copy_string(collector.error.localizedDescription ?: @"speech recognizer returned no transcript");
            return prepared;
        }

        NSString *text = collector.parts.count > 0
            ? [collector.parts componentsJoinedByString:@" "]
            : @"";
        NSDictionary *document = @{
            @"version": @1,
            @"status": @"complete",
            @"locale": localeID,
            @"text": text,
            @"segments": collector.segments,
        };
        NSError *jsonError = nil;
        NSData *jsonData = [NSJSONSerialization dataWithJSONObject:document options:NSJSONWritingSortedKeys error:&jsonError];
        if (jsonData == nil) {
            prepared.code = 0;
            prepared.err = sg_speech_copy_string(jsonError.localizedDescription ?: @"could not serialize transcript");
            return prepared;
        }

        prepared.code = 1;
        prepared.text = sg_speech_copy_string(text);
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
