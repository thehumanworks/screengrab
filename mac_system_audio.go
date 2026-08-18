//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AVFoundation -framework AppKit -framework ScreenCaptureKit -framework CoreMedia

#import <AVFoundation/AVFoundation.h>
#import <Foundation/Foundation.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <dispatch/dispatch.h>
#include <stdlib.h>
#include <string.h>

// System-audio capture rides the same ScreenCaptureKit permission as screen
// capture: an SCStream with capturesAudio delivers CMSampleBuffers of
// whatever the filtered content is playing. A display filter hears all app
// audio; a desktopIndependentWindow filter hears only the owning app. There
// is no separate TCC prompt — Screen Recording covers it — which keeps the
// "silent capture never asks for audio permissions" invariant intact.

typedef struct {
    int ok;
    double duration;
    char *err;
} sg_sys_result;

static char *sg_sys_copy_string(NSString *s) {
    if (s == nil) return NULL;
    const char *utf8 = [s UTF8String];
    if (utf8 == NULL) return NULL;
    size_t n = strlen(utf8);
    char *out = (char *)malloc(n + 1);
    if (out != NULL) memcpy(out, utf8, n + 1);
    return out;
}

static sg_sys_result sg_sys_error_result(NSString *message) {
    sg_sys_result result = {0, 0, NULL};
    result.err = sg_sys_copy_string(message);
    return result;
}

// Same CGS bootstrap as mac_capture.go — without NSApplicationLoad the first
// SCK call aborts a CLI process with CGS_REQUIRE_INIT.
static void sg_sys_ensure_init(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        NSApplicationLoad();
    });
}

static SCShareableContent *sg_sys_get_shareable(void) {
    sg_sys_ensure_init();
    __block SCShareableContent *result = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [SCShareableContent
        getShareableContentExcludingDesktopWindows:YES
                               onScreenWindowsOnly:NO
                                 completionHandler:^(SCShareableContent *content, NSError *error) {
            (void)error;
            result = content;
            dispatch_semaphore_signal(sem);
        }];
    dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(10 * NSEC_PER_SEC)));
    return result;
}

@interface SGSystemAudioWriter : NSObject <SCStreamOutput, SCStreamDelegate>
@end

static SCStream *sg_sys_stream = nil;
static SGSystemAudioWriter *sg_sys_writer = nil;
static dispatch_queue_t sg_sys_queue = nil;
static AVAudioFile *sg_sys_file = nil;
static AVAudioFormat *sg_sys_format = nil;
static NSString *sg_sys_path = nil;
static long long sg_sys_frames = 0;
static NSString *sg_sys_write_error = nil;

@implementation SGSystemAudioWriter

- (void)stream:(SCStream *)stream
    didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
                   ofType:(SCStreamOutputType)type {
    (void)stream;
    if (type != SCStreamOutputTypeAudio) return;
    if (!CMSampleBufferIsValid(sampleBuffer) || !CMSampleBufferDataIsReady(sampleBuffer)) return;
    CMAudioFormatDescriptionRef desc =
        (CMAudioFormatDescriptionRef)CMSampleBufferGetFormatDescription(sampleBuffer);
    if (desc == NULL) return;
    const AudioStreamBasicDescription *asbd = CMAudioFormatDescriptionGetStreamBasicDescription(desc);
    if (asbd == NULL) return;
    CMItemCount frames = CMSampleBufferGetNumSamples(sampleBuffer);
    if (frames <= 0) return;

    @synchronized([SGSystemAudioWriter class]) {
        if (sg_sys_write_error != nil || sg_sys_path == nil) return;
        NSError *error = nil;
        if (sg_sys_file == nil) {
            // The stream's PCM layout is only known once the first buffer
            // arrives, so the output file is created lazily to match it.
            AVAudioFormat *streamFormat = [[AVAudioFormat alloc] initWithStreamDescription:asbd];
            if (streamFormat == nil || streamFormat.sampleRate <= 0) {
                sg_sys_write_error = @"unsupported system audio stream format";
                return;
            }
            NSDictionary *settings = @{
                AVFormatIDKey: @(kAudioFormatLinearPCM),
                AVSampleRateKey: @(streamFormat.sampleRate),
                AVNumberOfChannelsKey: @(streamFormat.channelCount),
                AVLinearPCMBitDepthKey: @16,
                AVLinearPCMIsFloatKey: @NO,
                AVLinearPCMIsBigEndianKey: @NO,
                AVLinearPCMIsNonInterleaved: @NO,
            };
            AVAudioFile *file = [[AVAudioFile alloc]
                initForWriting:[NSURL fileURLWithPath:sg_sys_path]
                      settings:settings
                  commonFormat:streamFormat.commonFormat
                   interleaved:streamFormat.isInterleaved
                         error:&error];
            if (file == nil) {
                sg_sys_write_error = error == nil
                    ? @"could not create system audio output file"
                    : [NSString stringWithFormat:@"create system audio output file: %@", error.localizedDescription];
                return;
            }
            sg_sys_file = file;
            sg_sys_format = streamFormat;
        }

        AVAudioPCMBuffer *pcm = [[AVAudioPCMBuffer alloc] initWithPCMFormat:sg_sys_format
                                                              frameCapacity:(AVAudioFrameCount)frames];
        if (pcm == nil) {
            sg_sys_write_error = @"could not allocate system audio buffer";
            return;
        }
        pcm.frameLength = (AVAudioFrameCount)frames;
        OSStatus status = CMSampleBufferCopyPCMDataIntoAudioBufferList(
            sampleBuffer, 0, (int32_t)frames, pcm.mutableAudioBufferList);
        if (status != noErr) {
            sg_sys_write_error = [NSString stringWithFormat:@"copy system audio samples failed (OSStatus %d)", (int)status];
            return;
        }
        if (![sg_sys_file writeFromBuffer:pcm error:&error]) {
            sg_sys_write_error = error.localizedDescription ?: @"system audio buffer write failed";
            return;
        }
        sg_sys_frames += frames;
    }
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    (void)stream;
    @synchronized([SGSystemAudioWriter class]) {
        if (sg_sys_write_error == nil && error != nil) {
            sg_sys_write_error = [NSString stringWithFormat:@"system audio stream stopped: %@", error.localizedDescription];
        }
    }
}

@end

static sg_sys_result sg_sysaudio_start(const char *path, int is_window, uint32_t window_id, int display_index) {
    @autoreleasepool {
        @synchronized([SGSystemAudioWriter class]) {
            if (sg_sys_stream != nil) {
                return sg_sys_error_result(@"system audio is already recording");
            }
        }

        NSString *filePath = [NSString stringWithUTF8String:path];
        if (filePath == nil) return sg_sys_error_result(@"invalid system audio output path");

        SCShareableContent *content = sg_sys_get_shareable();
        if (content == nil) {
            return sg_sys_error_result(@"could not enumerate shareable content (grant Screen Recording permission)");
        }

        SCContentFilter *filter = nil;
        if (is_window) {
            SCWindow *target = nil;
            for (SCWindow *w in content.windows) {
                if ((uint32_t)w.windowID == window_id) { target = w; break; }
            }
            if (target == nil) {
                return sg_sys_error_result(@"window not found in SCShareableContent (may have closed or you may need to grant Screen Recording permission)");
            }
            filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:target];
        } else {
            NSArray<SCDisplay *> *displays = content.displays;
            if ([displays count] == 0) {
                return sg_sys_error_result(@"no displays available for system audio capture");
            }
            SCDisplay *display = displays[0];
            if (display_index >= 0 && display_index < (int)[displays count]) {
                display = displays[display_index];
            }
            filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
        }

        SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
        config.capturesAudio = YES;
        config.excludesCurrentProcessAudio = YES;
        config.sampleRate = 48000;
        config.channelCount = 2;
        // The stream mandates a video pipeline; keep it as close to free as
        // possible since frames come from SCScreenshotManager, not from here.
        config.width = 2;
        config.height = 2;
        config.minimumFrameInterval = CMTimeMake(1, 1);
        config.showsCursor = NO;

        SGSystemAudioWriter *writer = [[SGSystemAudioWriter alloc] init];
        SCStream *stream = [[SCStream alloc] initWithFilter:filter configuration:config delegate:writer];
        if (stream == nil) {
            return sg_sys_error_result(@"could not create system audio stream");
        }
        dispatch_queue_t queue = dispatch_queue_create("io.thehumanworks.screengrab.sysaudio", DISPATCH_QUEUE_SERIAL);
        NSError *error = nil;
        if (![stream addStreamOutput:writer type:SCStreamOutputTypeAudio sampleHandlerQueue:queue error:&error]) {
            NSString *message = error == nil
                ? @"could not attach system audio output"
                : [NSString stringWithFormat:@"attach system audio output: %@", error.localizedDescription];
            return sg_sys_error_result(message);
        }

        @synchronized([SGSystemAudioWriter class]) {
            sg_sys_stream = stream;
            sg_sys_writer = writer;
            sg_sys_queue = queue;
            sg_sys_file = nil;
            sg_sys_format = nil;
            sg_sys_path = filePath;
            sg_sys_frames = 0;
            sg_sys_write_error = nil;
        }

        __block NSString *startError = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [stream startCaptureWithCompletionHandler:^(NSError *startErr) {
            if (startErr != nil) startError = startErr.localizedDescription;
            dispatch_semaphore_signal(sem);
        }];
        long waited = dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(10 * NSEC_PER_SEC)));
        if (waited != 0) startError = @"timed out starting system audio capture";
        if (startError != nil) {
            @synchronized([SGSystemAudioWriter class]) {
                sg_sys_stream = nil;
                sg_sys_writer = nil;
                sg_sys_queue = nil;
                sg_sys_file = nil;
                sg_sys_format = nil;
                sg_sys_path = nil;
            }
            return sg_sys_error_result([NSString stringWithFormat:@"start system audio capture: %@", startError]);
        }

        sg_sys_result result = {1, 0, NULL};
        return result;
    }
}

static double sg_sysaudio_current_time(void) {
    @synchronized([SGSystemAudioWriter class]) {
        if (sg_sys_format == nil || sg_sys_format.sampleRate <= 0) return 0;
        return (double)sg_sys_frames / sg_sys_format.sampleRate;
    }
}

static sg_sys_result sg_sysaudio_stop(void) {
    @autoreleasepool {
        __block SCStream *stream = nil;
        @synchronized([SGSystemAudioWriter class]) {
            if (sg_sys_stream == nil) {
                return sg_sys_error_result(@"system audio recorder is not running");
            }
            stream = sg_sys_stream;
        }

        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [stream stopCaptureWithCompletionHandler:^(NSError *stopErr) {
            // A stream that already stopped (e.g. the delegate saw an error)
            // reports a failure here; the recorded write error wins below.
            (void)stopErr;
            dispatch_semaphore_signal(sem);
        }];
        dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(10 * NSEC_PER_SEC)));

        @synchronized([SGSystemAudioWriter class]) {
            double duration = 0;
            if (sg_sys_format != nil && sg_sys_format.sampleRate > 0) {
                duration = (double)sg_sys_frames / sg_sys_format.sampleRate;
            }
            NSString *writeError = sg_sys_write_error;
            sg_sys_stream = nil;
            sg_sys_writer = nil;
            sg_sys_queue = nil;
            sg_sys_file = nil;
            sg_sys_format = nil;
            sg_sys_path = nil;
            sg_sys_frames = 0;
            sg_sys_write_error = nil;
            if (writeError != nil) {
                sg_sys_result failed = sg_sys_error_result(writeError);
                failed.duration = duration;
                return failed;
            }
            sg_sys_result result = {1, duration, NULL};
            return result;
        }
    }
}

static void sg_sys_free_result(sg_sys_result result) {
    if (result.err != NULL) free(result.err);
}
*/
import "C"

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"
)

type platformSystemAudio struct{}

func startPlatformSystemAudio(path string, src Source) (*platformSystemAudio, error) {
	isWindow := 0
	windowID := uint32(0)
	displayIndex := 0
	switch src.Kind {
	case SourceKindWindow:
		id, err := parseWindowID(strings.TrimPrefix(src.ID, SourceKindWindow+":"))
		if err != nil {
			return nil, fmt.Errorf("invalid window source id %q", src.ID)
		}
		isWindow = 1
		windowID = id
	case SourceKindDisplay:
		idx, err := strconv.Atoi(strings.TrimPrefix(src.ID, SourceKindDisplay+":"))
		if err != nil {
			return nil, fmt.Errorf("invalid display source id %q", src.ID)
		}
		displayIndex = idx
	default:
		return nil, fmt.Errorf("unknown source kind %q", src.Kind)
	}

	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	result := C.sg_sysaudio_start(cPath, C.int(isWindow), C.uint32_t(windowID), C.int(displayIndex))
	defer C.sg_sys_free_result(result)
	if result.ok == 0 {
		if result.err != nil {
			return nil, fmt.Errorf("start system audio: %s", C.GoString(result.err))
		}
		return nil, fmt.Errorf("start system audio: unknown ScreenCaptureKit error")
	}
	return &platformSystemAudio{}, nil
}

func (r *platformSystemAudio) currentTime() float64 {
	return float64(C.sg_sysaudio_current_time())
}

func (r *platformSystemAudio) stop() (float64, error) {
	result := C.sg_sysaudio_stop()
	defer C.sg_sys_free_result(result)
	if result.ok == 0 {
		if result.err != nil {
			return float64(result.duration), fmt.Errorf("stop system audio: %s", C.GoString(result.err))
		}
		return float64(result.duration), fmt.Errorf("stop system audio: unknown ScreenCaptureKit error")
	}
	return float64(result.duration), nil
}
