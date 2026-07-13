//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Foundation -framework AVFoundation

#import <AVFoundation/AVFoundation.h>
#import <Foundation/Foundation.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    int ok;
    double duration;
    char *err;
} sg_audio_result;

static AVAudioEngine *sg_audio_engine = nil;
static AVAudioInputNode *sg_audio_input = nil;
static AVAudioFile *sg_audio_file = nil;
static AVAudioFormat *sg_audio_format = nil;
static AVAudioFramePosition sg_audio_frames = 0;
static NSString *sg_audio_write_error = nil;
static BOOL sg_audio_stopping = NO;

static char *sg_audio_copy_string(NSString *s) {
    if (s == nil) return NULL;
    const char *utf8 = [s UTF8String];
    if (utf8 == NULL) return NULL;
    size_t n = strlen(utf8);
    char *out = (char *)malloc(n + 1);
    if (out != NULL) memcpy(out, utf8, n + 1);
    return out;
}

static sg_audio_result sg_audio_error(NSString *message) {
    sg_audio_result result = {0, 0, NULL};
    result.err = sg_audio_copy_string(message);
    return result;
}

static int sg_request_microphone_access(void) {
    AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
    if (status == AVAuthorizationStatusAuthorized) return 1;
    if (status != AVAuthorizationStatusNotDetermined) return 0;

    __block BOOL granted = NO;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL allowed) {
        granted = allowed;
        dispatch_semaphore_signal(sem);
    }];
    long wait_result = dispatch_semaphore_wait(
        sem,
        dispatch_time(DISPATCH_TIME_NOW, (int64_t)(60 * NSEC_PER_SEC))
    );
    return wait_result == 0 && granted ? 1 : 0;
}

static sg_audio_result sg_audio_start(const char *path) {
    @autoreleasepool {
        @synchronized([AVAudioEngine class]) {
            if (sg_audio_engine != nil && sg_audio_engine.isRunning) {
                return sg_audio_error(@"microphone is already recording");
            }
            if (!sg_request_microphone_access()) {
                return sg_audio_error(@"microphone permission was not granted");
            }

            NSString *filePath = [NSString stringWithUTF8String:path];
            if (filePath == nil) return sg_audio_error(@"invalid microphone output path");
            if ([AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeAudio] == nil) {
                return sg_audio_error(@"no default microphone input device is available");
            }
            NSURL *url = [NSURL fileURLWithPath:filePath];
            AVAudioEngine *engine = [[AVAudioEngine alloc] init];
            AVAudioInputNode *input = engine.inputNode;
            AVAudioFormat *inputFormat = [input outputFormatForBus:0];
            if (inputFormat.sampleRate <= 0 || inputFormat.channelCount == 0) {
                return sg_audio_error(@"default microphone has no active audio format");
            }
            NSDictionary *settings = @{
                AVFormatIDKey: @(kAudioFormatLinearPCM),
                AVSampleRateKey: @(inputFormat.sampleRate),
                AVNumberOfChannelsKey: @(inputFormat.channelCount),
                AVLinearPCMBitDepthKey: @16,
                AVLinearPCMIsFloatKey: @NO,
                AVLinearPCMIsBigEndianKey: @NO,
                AVLinearPCMIsNonInterleaved: @NO,
            };

            NSError *error = nil;
            AVAudioFile *file = [[AVAudioFile alloc]
                initForWriting:url
                      settings:settings
                  commonFormat:AVAudioPCMFormatFloat32
                   interleaved:NO
                         error:&error];
            if (file == nil) {
                NSString *message = error == nil
                    ? @"could not create microphone output file"
                    : [NSString stringWithFormat:@"create microphone output file: %@", error.localizedDescription];
                return sg_audio_error(message);
            }

            sg_audio_engine = engine;
            sg_audio_input = input;
            sg_audio_file = file;
            sg_audio_format = inputFormat;
            sg_audio_frames = 0;
            sg_audio_write_error = nil;
            sg_audio_stopping = NO;

            AVAudioNodeTapBlock tapBlock = ^(AVAudioPCMBuffer *buffer, AVAudioTime *when) {
                (void)when;
                @synchronized([AVAudioEngine class]) {
                    if (sg_audio_stopping || sg_audio_file == nil) return;
                    NSError *writeError = nil;
                    if (![sg_audio_file writeFromBuffer:buffer error:&writeError]) {
                        sg_audio_write_error = writeError.localizedDescription ?: @"microphone buffer write failed";
                        return;
                    }
                    sg_audio_frames += buffer.frameLength;
                }
            };
            BOOL installed = NO;
            if (@available(macOS 27.0, *)) {
                installed = [input installTapOnBus:0 bufferSize:4096 format:nil error:&error block:tapBlock];
            } else {
                // macOS 26 exposes only the exception-throwing selector.
                @try {
                    [input installTapOnBus:0 bufferSize:4096 format:nil block:tapBlock];
                    installed = YES;
                } @catch (NSException *exception) {
                    error = [NSError errorWithDomain:@"io.thehumanworks.screengrab.audio"
                                                 code:1
                                             userInfo:@{NSLocalizedDescriptionKey: exception.reason ?: @"could not install microphone tap"}];
                }
            }
            if (!installed) {
                sg_audio_engine = nil;
                sg_audio_input = nil;
                sg_audio_file = nil;
                sg_audio_format = nil;
                NSString *message = error == nil
                    ? @"could not install microphone tap"
                    : [NSString stringWithFormat:@"install microphone tap: %@", error.localizedDescription];
                return sg_audio_error(message);
            }

            [engine prepare];
            if (![engine startAndReturnError:&error]) {
                [input removeTapOnBus:0];
                sg_audio_engine = nil;
                sg_audio_input = nil;
                sg_audio_file = nil;
                sg_audio_format = nil;
                NSString *message = error == nil
                    ? @"could not start microphone audio engine"
                    : [NSString stringWithFormat:@"start microphone audio engine: %@", error.localizedDescription];
                return sg_audio_error(message);
            }
            sg_audio_result result = {1, 0, NULL};
            return result;
        }
    }
}

static double sg_audio_current_time(void) {
    @synchronized([AVAudioEngine class]) {
        if (sg_audio_format == nil || sg_audio_format.sampleRate <= 0) return 0;
        return (double)sg_audio_frames / sg_audio_format.sampleRate;
    }
}

static sg_audio_result sg_audio_stop(void) {
    @autoreleasepool {
        __block AVAudioEngine *engine = nil;
        __block AVAudioInputNode *input = nil;
        @synchronized([AVAudioEngine class]) {
            if (sg_audio_engine == nil) {
                return sg_audio_error(@"microphone recorder is not running");
            }
            sg_audio_stopping = YES;
            engine = sg_audio_engine;
            input = sg_audio_input;
        }

        [input removeTapOnBus:0];
        [engine stop];

        @synchronized([AVAudioEngine class]) {
            double duration = 0;
            if (sg_audio_format != nil && sg_audio_format.sampleRate > 0) {
                duration = (double)sg_audio_frames / sg_audio_format.sampleRate;
            }
            NSString *writeError = sg_audio_write_error;
            sg_audio_engine = nil;
            sg_audio_input = nil;
            sg_audio_file = nil;
            sg_audio_format = nil;
            sg_audio_frames = 0;
            sg_audio_write_error = nil;
            sg_audio_stopping = NO;
            if (writeError != nil) {
                sg_audio_result failed = sg_audio_error(writeError);
                failed.duration = duration;
                return failed;
            }
            sg_audio_result result = {1, duration, NULL};
            return result;
        }
    }
}

static void sg_audio_free_result(sg_audio_result result) {
    if (result.err != NULL) free(result.err);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type platformMicrophone struct{}

func startPlatformMicrophone(path string) (*platformMicrophone, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	result := C.sg_audio_start(cPath)
	defer C.sg_audio_free_result(result)
	if result.ok == 0 {
		if result.err != nil {
			return nil, fmt.Errorf("start microphone: %s", C.GoString(result.err))
		}
		return nil, fmt.Errorf("start microphone: unknown AVFoundation error")
	}
	return &platformMicrophone{}, nil
}

func (r *platformMicrophone) currentTime() float64 {
	return float64(C.sg_audio_current_time())
}

func (r *platformMicrophone) stop() (float64, error) {
	result := C.sg_audio_stop()
	defer C.sg_audio_free_result(result)
	if result.ok == 0 {
		if result.err != nil {
			return float64(result.duration), fmt.Errorf("stop microphone: %s", C.GoString(result.err))
		}
		return float64(result.duration), fmt.Errorf("stop microphone: unknown AVFoundation error")
	}
	return float64(result.duration), nil
}
