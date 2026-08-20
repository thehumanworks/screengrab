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

// The tap hands us buffers the engine owns and reuses; every buffer is
// deep-copied ("pinned") in the tap and the pinned copy is written on a
// dedicated serial queue. The render thread therefore never blocks on disk
// I/O and no buffer can be recycled by the engine before its samples are on
// disk. sg_audio_file and sg_audio_converter are only touched on that queue
// once recording is live; everything else is guarded by the class lock.
static AVAudioEngine *sg_audio_engine = nil;
static AVAudioFile *sg_audio_file = nil;
static AVAudioConverter *sg_audio_converter = nil;
static dispatch_queue_t sg_audio_write_queue = nil;
static double sg_audio_seconds = 0;
static NSString *sg_audio_write_error = nil;
static BOOL sg_audio_stopping = NO;
static id sg_audio_observer = nil;

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

static void sg_audio_set_write_error(NSString *message) {
    if (message == nil) return;
    @synchronized([AVAudioEngine class]) {
        if (sg_audio_write_error == nil) sg_audio_write_error = message;
    }
}

static AVAudioPCMBuffer *sg_audio_pin_buffer(AVAudioPCMBuffer *buffer) {
    if (buffer == nil || buffer.frameLength == 0) return nil;
    AVAudioPCMBuffer *pinned = [[AVAudioPCMBuffer alloc] initWithPCMFormat:buffer.format
                                                             frameCapacity:buffer.frameLength];
    if (pinned == nil) return nil;
    pinned.frameLength = buffer.frameLength;
    const AudioBufferList *src = buffer.audioBufferList;
    AudioBufferList *dst = pinned.mutableAudioBufferList;
    for (UInt32 i = 0; i < src->mNumberBuffers && i < dst->mNumberBuffers; i++) {
        UInt32 n = src->mBuffers[i].mDataByteSize;
        if (n > dst->mBuffers[i].mDataByteSize) n = dst->mBuffers[i].mDataByteSize;
        if (src->mBuffers[i].mData != NULL && dst->mBuffers[i].mData != NULL && n > 0) {
            memcpy(dst->mBuffers[i].mData, src->mBuffers[i].mData, n);
        }
    }
    return pinned;
}

// Runs on the writer queue only. When the input device changed mid-recording
// (AirPods connecting, default-device switch) the tap format no longer
// matches the file's processing format, so route through an AVAudioConverter
// instead of dropping the rest of the take.
static void sg_audio_write_pinned(AVAudioPCMBuffer *pinned) {
    if (pinned == nil || sg_audio_file == nil) return;
    AVAudioPCMBuffer *out = pinned;
    if (![pinned.format isEqual:sg_audio_file.processingFormat]) {
        if (sg_audio_converter == nil || ![sg_audio_converter.inputFormat isEqual:pinned.format]) {
            sg_audio_converter = [[AVAudioConverter alloc] initFromFormat:pinned.format
                                                                 toFormat:sg_audio_file.processingFormat];
        }
        if (sg_audio_converter == nil) {
            sg_audio_set_write_error(@"could not convert microphone audio after a device change");
            return;
        }
        double ratio = sg_audio_file.processingFormat.sampleRate / pinned.format.sampleRate;
        AVAudioFrameCount capacity = (AVAudioFrameCount)((double)pinned.frameLength * ratio) + 64;
        AVAudioPCMBuffer *converted = [[AVAudioPCMBuffer alloc]
            initWithPCMFormat:sg_audio_file.processingFormat frameCapacity:capacity];
        if (converted == nil) {
            sg_audio_set_write_error(@"could not allocate microphone conversion buffer");
            return;
        }
        __block BOOL provided = NO;
        NSError *convertError = nil;
        AVAudioConverterOutputStatus status = [sg_audio_converter
            convertToBuffer:converted
                      error:&convertError
         withInputFromBlock:^AVAudioBuffer *(AVAudioPacketCount inNumberOfPackets,
                                             AVAudioConverterInputStatus *outStatus) {
            (void)inNumberOfPackets;
            if (provided) {
                *outStatus = AVAudioConverterInputStatus_NoDataNow;
                return nil;
            }
            provided = YES;
            *outStatus = AVAudioConverterInputStatus_HaveData;
            return pinned;
        }];
        if (status == AVAudioConverterOutputStatus_Error) {
            sg_audio_set_write_error(convertError.localizedDescription ?: @"microphone format conversion failed");
            return;
        }
        if (converted.frameLength == 0) return;
        out = converted;
    }
    NSError *writeError = nil;
    if (![sg_audio_file writeFromBuffer:out error:&writeError]) {
        sg_audio_set_write_error(writeError.localizedDescription ?: @"microphone buffer write failed");
    }
}

// The error-returning installTapOnBus variant exists only in the macOS 27
// SDK headers; referencing its selector is a hard compile error against the
// macOS 26 SDK CI builds (@available guards runtime, not selector
// visibility). The exception-throwing variant exists on every supported
// SDK, so use it unconditionally and convert the exception into an NSError.
static BOOL sg_audio_install_tap(AVAudioInputNode *input, NSError **outError) {
    @try {
        [input installTapOnBus:0 bufferSize:4096 format:nil block:^(AVAudioPCMBuffer *buffer, AVAudioTime *when) {
            (void)when;
            if (buffer == nil || buffer.frameLength == 0) return;
            dispatch_queue_t queue = nil;
            @synchronized([AVAudioEngine class]) {
                if (sg_audio_stopping || sg_audio_write_queue == nil) return;
                queue = sg_audio_write_queue;
                if (buffer.format.sampleRate > 0) {
                    // The mic clock advances at capture time, not write time,
                    // so frame offsets stay aligned even if disk writes lag.
                    sg_audio_seconds += (double)buffer.frameLength / buffer.format.sampleRate;
                }
            }
            AVAudioPCMBuffer *pinned = sg_audio_pin_buffer(buffer);
            if (pinned == nil) {
                sg_audio_set_write_error(@"could not pin microphone buffer");
                return;
            }
            dispatch_async(queue, ^{ sg_audio_write_pinned(pinned); });
        }];
        return YES;
    } @catch (NSException *exception) {
        if (outError != NULL) {
            *outError = [NSError errorWithDomain:@"io.thehumanworks.screengrab.audio"
                                            code:1
                                        userInfo:@{NSLocalizedDescriptionKey: exception.reason ?: @"could not install microphone tap"}];
        }
        return NO;
    }
}

static sg_audio_result sg_audio_start(const char *path) {
    @autoreleasepool {
        @synchronized([AVAudioEngine class]) {
            if (sg_audio_engine != nil) {
                return sg_audio_error(@"microphone is already recording");
            }
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

        dispatch_queue_t queue = dispatch_queue_create("io.thehumanworks.screengrab.micwrite", DISPATCH_QUEUE_SERIAL);
        @synchronized([AVAudioEngine class]) {
            if (sg_audio_engine != nil) {
                return sg_audio_error(@"microphone is already recording");
            }
            sg_audio_engine = engine;
            sg_audio_file = file;
            sg_audio_converter = nil;
            sg_audio_write_queue = queue;
            sg_audio_seconds = 0;
            sg_audio_write_error = nil;
            sg_audio_stopping = NO;
        }

        NSError *tapError = nil;
        if (!sg_audio_install_tap(input, &tapError)) {
            @synchronized([AVAudioEngine class]) {
                sg_audio_engine = nil;
                sg_audio_write_queue = nil;
            }
            sg_audio_file = nil;
            NSString *message = tapError == nil
                ? @"could not install microphone tap"
                : [NSString stringWithFormat:@"install microphone tap: %@", tapError.localizedDescription];
            return sg_audio_error(message);
        }

        // A configuration change (device switch, sample-rate change) stops
        // the engine silently; without this restart the file would simply end
        // at the moment the user's headphones connected.
        sg_audio_observer = [[NSNotificationCenter defaultCenter]
            addObserverForName:AVAudioEngineConfigurationChangeNotification
                        object:engine
                         queue:nil
                    usingBlock:^(NSNotification *note) {
            (void)note;
            @synchronized([AVAudioEngine class]) {
                if (sg_audio_stopping || sg_audio_engine == nil) return;
                AVAudioInputNode *liveInput = sg_audio_engine.inputNode;
                [liveInput removeTapOnBus:0];
                NSError *retapError = nil;
                if (!sg_audio_install_tap(liveInput, &retapError)) {
                    if (sg_audio_write_error == nil) {
                        sg_audio_write_error = retapError.localizedDescription
                            ?: @"could not reinstall microphone tap after a device change";
                    }
                    return;
                }
                NSError *restartError = nil;
                if (![sg_audio_engine startAndReturnError:&restartError]) {
                    if (sg_audio_write_error == nil) {
                        sg_audio_write_error = [NSString stringWithFormat:@"restart microphone after device change: %@",
                            restartError.localizedDescription ?: @"unknown error"];
                    }
                }
            }
        }];

        [engine prepare];
        if (![engine startAndReturnError:&error]) {
            [[NSNotificationCenter defaultCenter] removeObserver:sg_audio_observer];
            sg_audio_observer = nil;
            [input removeTapOnBus:0];
            @synchronized([AVAudioEngine class]) {
                sg_audio_engine = nil;
                sg_audio_write_queue = nil;
            }
            sg_audio_file = nil;
            NSString *message = error == nil
                ? @"could not start microphone audio engine"
                : [NSString stringWithFormat:@"start microphone audio engine: %@", error.localizedDescription];
            return sg_audio_error(message);
        }
        sg_audio_result result = {1, 0, NULL};
        return result;
    }
}

static double sg_audio_current_time(void) {
    @synchronized([AVAudioEngine class]) {
        return sg_audio_seconds;
    }
}

static sg_audio_result sg_audio_stop(void) {
    @autoreleasepool {
        AVAudioEngine *engine = nil;
        dispatch_queue_t queue = nil;
        @synchronized([AVAudioEngine class]) {
            if (sg_audio_engine == nil) {
                return sg_audio_error(@"microphone recorder is not running");
            }
            sg_audio_stopping = YES;
            engine = sg_audio_engine;
            queue = sg_audio_write_queue;
        }

        if (sg_audio_observer != nil) {
            [[NSNotificationCenter defaultCenter] removeObserver:sg_audio_observer];
            sg_audio_observer = nil;
        }
        [engine.inputNode removeTapOnBus:0];
        [engine stop];
        if (queue != nil) {
            // Drain every pinned buffer, then close the file on the writer
            // queue itself so no straggling write can race the close. Closing
            // (releasing) the AVAudioFile is what finalizes the WAV header.
            dispatch_sync(queue, ^{
                sg_audio_file = nil;
                sg_audio_converter = nil;
            });
        }

        @synchronized([AVAudioEngine class]) {
            double duration = sg_audio_seconds;
            NSString *writeError = sg_audio_write_error;
            sg_audio_engine = nil;
            sg_audio_write_queue = nil;
            sg_audio_seconds = 0;
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
