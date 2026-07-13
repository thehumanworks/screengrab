//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework Foundation -framework AppKit -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo

#import <Foundation/Foundation.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <CoreGraphics/CoreGraphics.h>
#include <CoreVideo/CoreVideo.h>
#include <dispatch/dispatch.h>
#include <stdlib.h>
#include <string.h>

// Window enumeration and capture for macOS Spaces, including discovery of
// fullscreen apps on non-active Spaces. The legacy CGWindowListCopyWindowInfo /
// CGWindowListCreateImage path is `unavailable` (hard error) on the macOS 15+
// SDK we ship with, so this file routes everything through ScreenCaptureKit.
// SCK is fundamentally async; we bridge it to a sync call site with a
// dispatch_semaphore_t so the Go side stays straightforward.

// sg_ensure_init wires up the AppKit application context so the CoreGraphics
// window server (CGS) and ScreenCaptureKit's compositor pipeline have
// somewhere to register. Without this the first SCScreenshotManager call
// aborts with `CGS_REQUIRE_INIT` from a CLI process. NSApplicationLoad is
// safe to call even from a non-GUI CLI; it spins up the connection without
// taking over the run loop. dispatch_once keeps the cost amortized.
static void sg_ensure_init(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        NSApplicationLoad();
    });
}

typedef struct {
    uint32_t window_id;
    int x;
    int y;
    int width;
    int height;
    int on_screen;
    char *app_name;
    char *window_title;
} sg_window_entry;

typedef struct {
    sg_window_entry *items;
    int count;
} sg_window_list;

static char *sg_copy_nsstring(NSString *s) {
    if (s == nil) return NULL;
    const char *u = [s UTF8String];
    if (u == NULL) return NULL;
    size_t n = strlen(u);
    char *buf = (char *)malloc(n + 1);
    if (!buf) return NULL;
    memcpy(buf, u, n + 1);
    return buf;
}

static SCShareableContent *sg_get_shareable_sync(void) {
    sg_ensure_init();
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
    // Cap the wait at ten seconds. The first call may stall on the system
    // permission prompt; if the user never grants Screen Recording we'd
    // rather surface an empty list than block the GUI forever.
    dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(10 * NSEC_PER_SEC)));
    return result;
}

static sg_window_list sg_enumerate_windows(void) {
    sg_window_list out;
    out.items = NULL;
    out.count = 0;

    SCShareableContent *content = sg_get_shareable_sync();
    if (content == nil) return out;

    NSArray<SCWindow *> *windows = content.windows;
    NSUInteger n = [windows count];
    if (n == 0) return out;

    sg_window_entry *items = (sg_window_entry *)calloc((size_t)n, sizeof(sg_window_entry));
    if (!items) return out;

    int idx = 0;
    for (NSUInteger i = 0; i < n; i++) {
        SCWindow *w = windows[i];
        // Layer 0 is the normal app window layer; menu bar, dock, status
        // items, etc. all live above and we don't want them in the picker.
        if (w.windowLayer != 0) continue;

        CGRect frame = w.frame;
        if (frame.size.width < 64 || frame.size.height < 64) continue;

        items[idx].window_id = (uint32_t)w.windowID;
        items[idx].x = (int)frame.origin.x;
        items[idx].y = (int)frame.origin.y;
        items[idx].width = (int)frame.size.width;
        items[idx].height = (int)frame.size.height;
        items[idx].on_screen = w.onScreen ? 1 : 0;
        if (w.owningApplication != nil) {
            items[idx].app_name = sg_copy_nsstring(w.owningApplication.applicationName);
        }
        if (w.title != nil) {
            items[idx].window_title = sg_copy_nsstring(w.title);
        }
        idx++;
    }

    out.items = items;
    out.count = idx;
    return out;
}

static void sg_free_window_list(sg_window_list wl) {
    for (int i = 0; i < wl.count; i++) {
        if (wl.items[i].app_name) free(wl.items[i].app_name);
        if (wl.items[i].window_title) free(wl.items[i].window_title);
    }
    if (wl.items) free(wl.items);
}

typedef struct {
    uint8_t *bytes;
    int width;
    int height;
    int stride;
    char *err; // malloc'd NSError description, or NULL on success
} sg_capture_buf;

static SCWindow *sg_find_window(uint32_t window_id) {
    SCShareableContent *content = sg_get_shareable_sync();
    if (content == nil) return nil;
    for (SCWindow *w in content.windows) {
        if ((uint32_t)w.windowID == window_id) return w;
    }
    return nil;
}

static sg_capture_buf sg_capture_window(uint32_t window_id) {
    sg_capture_buf out;
    out.bytes = NULL;
    out.width = 0;
    out.height = 0;
    out.stride = 0;
    out.err = NULL;

    sg_ensure_init();
    SCWindow *target = sg_find_window(window_id);
    if (target == nil) {
        out.err = sg_copy_nsstring(@"window not found in SCShareableContent (may have closed or you may need to grant Screen Recording permission)");
        return out;
    }

    SCContentFilter *filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:target];
    SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
    CGRect frame = target.frame;
    // SCStreamConfiguration wants pixel dimensions. SCWindow.frame is in
    // points; for our use (sampling for vision-capable LLMs) sticking with
    // points is fine — we don't need the extra Retina detail and a smaller
    // image keeps prompt-token cost down.
    config.width = (size_t)MAX(64.0, frame.size.width);
    config.height = (size_t)MAX(64.0, frame.size.height);
    config.pixelFormat = kCVPixelFormatType_32BGRA;
    config.showsCursor = NO;
    config.capturesAudio = NO;
    if (@available(macOS 14.2, *)) {
        config.ignoreShadowsSingleWindow = YES;
    }

    __block CGImageRef captured = NULL;
    __block NSString *err_desc = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [SCScreenshotManager
        captureImageWithFilter:filter
                 configuration:config
             completionHandler:^(CGImageRef image, NSError *error) {
            if (image != NULL) captured = CGImageRetain(image);
            if (error != nil) err_desc = [error localizedDescription];
            dispatch_semaphore_signal(sem);
        }];
    dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(10 * NSEC_PER_SEC)));

    if (captured == NULL) {
        if (err_desc != nil) {
            out.err = sg_copy_nsstring(err_desc);
        } else {
            out.err = sg_copy_nsstring(@"SCScreenshotManager returned nil image with no error (window likely on inactive Space and not currently rendering)");
        }
        return out;
    }

    size_t w = CGImageGetWidth(captured);
    size_t h = CGImageGetHeight(captured);
    if (w == 0 || h == 0) { CGImageRelease(captured); return out; }

    size_t stride = w * 4;
    uint8_t *buf = (uint8_t *)calloc(stride * h, 1);
    if (!buf) { CGImageRelease(captured); return out; }

    CGColorSpaceRef cs = CGColorSpaceCreateDeviceRGB();
    if (cs == NULL) { free(buf); CGImageRelease(captured); return out; }
    CGContextRef ctx = CGBitmapContextCreate(
        buf, w, h, 8, stride, cs,
        kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
    CGColorSpaceRelease(cs);
    if (ctx == NULL) { free(buf); CGImageRelease(captured); return out; }

    CGContextDrawImage(ctx, CGRectMake(0, 0, (CGFloat)w, (CGFloat)h), captured);
    CGContextRelease(ctx);
    CGImageRelease(captured);

    out.bytes = buf;
    out.width = (int)w;
    out.height = (int)h;
    out.stride = (int)stride;
    return out;
}

static void sg_free_capture_buf(sg_capture_buf b) {
    if (b.bytes) free(b.bytes);
    if (b.err) free(b.err);
}

static int sg_window_exists(uint32_t window_id) {
    return sg_find_window(window_id) != nil ? 1 : 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"unsafe"
)

var ErrWindowCaptureUnsupported = errors.New("window-level capture is only implemented on darwin")

type macWindow struct {
	ID       uint32
	X, Y     int
	Width    int
	Height   int
	OnScreen bool
	App      string
	Title    string
}

func listMacWindows() ([]macWindow, error) {
	cList := C.sg_enumerate_windows()
	defer C.sg_free_window_list(cList)

	n := int(cList.count)
	if n == 0 {
		return nil, nil
	}
	items := unsafe.Slice(cList.items, n)

	out := make([]macWindow, 0, n)
	for i := 0; i < n; i++ {
		e := items[i]
		w := macWindow{
			ID:       uint32(e.window_id),
			X:        int(e.x),
			Y:        int(e.y),
			Width:    int(e.width),
			Height:   int(e.height),
			OnScreen: e.on_screen != 0,
		}
		if e.app_name != nil {
			w.App = C.GoString(e.app_name)
		}
		if e.window_title != nil {
			w.Title = C.GoString(e.window_title)
		}
		out = append(out, w)
	}
	return out, nil
}

func captureMacWindow(windowID uint32) (*image.RGBA, error) {
	buf := C.sg_capture_window(C.uint32_t(windowID))
	defer C.sg_free_capture_buf(buf)

	if buf.bytes == nil || buf.width == 0 || buf.height == 0 {
		if buf.err != nil {
			return nil, fmt.Errorf("capture window 0x%x: %s", windowID, C.GoString(buf.err))
		}
		return nil, fmt.Errorf("capture window 0x%x returned nil image", windowID)
	}

	w := int(buf.width)
	h := int(buf.height)
	stride := int(buf.stride)

	src := unsafe.Slice((*byte)(unsafe.Pointer(buf.bytes)), stride*h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if stride == img.Stride {
		copy(img.Pix, src)
	} else {
		row := w * 4
		for y := 0; y < h; y++ {
			copy(img.Pix[y*img.Stride:y*img.Stride+row], src[y*stride:y*stride+row])
		}
	}
	return img, nil
}

func captureMacWindowRegion(windowID uint32, x, y, w, h int) (*image.RGBA, error) {
	full, err := captureMacWindow(windowID)
	if err != nil {
		return nil, err
	}
	if w <= 0 || h <= 0 {
		return full, nil
	}
	r := image.Rect(x, y, x+w, y+h).Intersect(full.Bounds())
	if r.Empty() {
		return nil, fmt.Errorf("region (%d,%d %dx%d) is outside window bounds %v", x, y, w, h, full.Bounds())
	}
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	rowBytes := r.Dx() * 4
	for yy := 0; yy < r.Dy(); yy++ {
		srcRow := (r.Min.Y+yy)*full.Stride + r.Min.X*4
		dstRow := yy * out.Stride
		copy(out.Pix[dstRow:dstRow+rowBytes], full.Pix[srcRow:srcRow+rowBytes])
	}
	return out, nil
}

func macWindowExists(windowID uint32) bool {
	return C.sg_window_exists(C.uint32_t(windowID)) != 0
}
