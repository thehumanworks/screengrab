package main

import (
	"fmt"
	"image"
	"sort"
	"strconv"
	"strings"

	"github.com/kbinani/screenshot"
)

// SourceKindDisplay is a physical display (one of screenshot.NumActiveDisplays()).
// SourceKindWindow is a single macOS window — typically a maximized app on
// its own Space, but any normal-layer window works.
const (
	SourceKindDisplay = "display"
	SourceKindWindow  = "window"
)

// Source is the unit the CLI and GUI both think in. The string ID is the
// canonical form the CLI and the frontend use to round-trip a selection back
// across the bridge.
//
//   display:0           — physical display, index 0
//   window:0x1A2B       — macOS window, hex CGWindowID
//   window:1234         — macOS window, decimal CGWindowID (both forms parse)
type Source struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	App    string `json:"app,omitempty"`
	Title  string `json:"title,omitempty"`
	// OnScreen is true when the source's content is being rendered to
	// some physical display right now. For displays it is always true.
	// For windows it reflects SCWindow.onScreen — false when the window
	// is on an inactive macOS Space, which is the user-visible case that
	// requires a swipe to bring it forward. SCScreenshotManager cannot
	// snapshot a window with OnScreen=false: the compositor has no
	// pixels for it, so the GUI uses this flag to swap in a fallback.
	OnScreen bool `json:"on_screen"`
}

func displaySourceID(idx int) string { return SourceKindDisplay + ":" + strconv.Itoa(idx) }
func windowSourceID(id uint32) string {
	return SourceKindWindow + ":0x" + strconv.FormatUint(uint64(id), 16)
}

// parseSource accepts the canonical "kind:id" form, plus a legacy plain
// integer that means "display:N" so the old --display flag and any user
// shorthand keep working.
func parseSource(s string) (Source, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Source{}, fmt.Errorf("empty source")
	}

	kind, rest, ok := strings.Cut(s, ":")
	if !ok {
		idx, err := strconv.Atoi(s)
		if err != nil {
			return Source{}, fmt.Errorf("invalid source %q: expected 'display:N' or 'window:ID', or a plain integer for legacy display index", s)
		}
		return resolveDisplaySource(idx)
	}

	switch kind {
	case SourceKindDisplay:
		idx, err := strconv.Atoi(rest)
		if err != nil {
			return Source{}, fmt.Errorf("invalid display index %q: %w", rest, err)
		}
		return resolveDisplaySource(idx)
	case SourceKindWindow:
		id, err := parseWindowID(rest)
		if err != nil {
			return Source{}, fmt.Errorf("invalid window id %q: %w", rest, err)
		}
		return resolveWindowSource(id)
	default:
		return Source{}, fmt.Errorf("invalid source kind %q (expected %q or %q)", kind, SourceKindDisplay, SourceKindWindow)
	}
}

func parseWindowID(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, err := strconv.ParseUint(s[2:], 16, 32)
		if err != nil {
			return 0, err
		}
		return uint32(v), nil
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

func resolveDisplaySource(idx int) (Source, error) {
	n := screenshot.NumActiveDisplays()
	if idx < 0 || idx >= n {
		return Source{}, fmt.Errorf("display %d out of range (active displays: %d)", idx, n)
	}
	b := screenshot.GetDisplayBounds(idx)
	return Source{
		ID:       displaySourceID(idx),
		Kind:     SourceKindDisplay,
		Name:     fmt.Sprintf("Display %d (%d×%d)", idx, b.Dx(), b.Dy()),
		Width:    b.Dx(),
		Height:   b.Dy(),
		OnScreen: true,
	}, nil
}

func resolveWindowSource(id uint32) (Source, error) {
	wins, err := listMacWindows()
	if err != nil {
		return Source{}, err
	}
	for _, w := range wins {
		if w.ID == id {
			return windowToSource(w), nil
		}
	}
	return Source{}, fmt.Errorf("no window with id 0x%x (window may have closed or you may need to grant Screen Recording permission)", id)
}

func windowToSource(w macWindow) Source {
	name := w.App
	if w.Title != "" {
		if name != "" {
			name = name + " — " + w.Title
		} else {
			name = w.Title
		}
	}
	if name == "" {
		name = fmt.Sprintf("Window 0x%x", w.ID)
	}
	return Source{
		ID:       windowSourceID(w.ID),
		Kind:     SourceKindWindow,
		Name:     name,
		Width:    w.Width,
		Height:   w.Height,
		App:      w.App,
		Title:    w.Title,
		OnScreen: w.OnScreen,
	}
}

// listSources returns every selectable target: physical displays first, then
// macOS windows that pass the size/layer filters. On non-darwin platforms
// listMacWindows is a stub that returns nil, so the list is displays-only.
//
// The window list is aggressively pruned because raw SCShareableContent.windows
// includes the AppKit/XPC view-service helpers ("CursorUIViewService",
// "Open and Save Panel Service", "AutoFill (…)", etc.) which look like real
// windows in the API but are popovers or tooltips with no recordable
// content. We also dedupe by (app, title) since helper apps spawn a long
// tail of near-identical 64×64 entries. Windows that match a display's
// resolution within a small tolerance are tagged as fullscreen-space
// candidates and prefixed in the visible name so the user can pick the
// "maximized app on its own Space" entry without confusion.
func listSources() []Source {
	out := []Source{}

	displayBounds := make([]image.Rectangle, 0)
	for i := 0; i < screenshot.NumActiveDisplays(); i++ {
		displayBounds = append(displayBounds, screenshot.GetDisplayBounds(i))
		if src, err := resolveDisplaySource(i); err == nil {
			out = append(out, src)
		}
	}

	wins, err := listMacWindows()
	if err != nil {
		return out
	}

	seen := make(map[string]bool)
	windowSrcs := make([]Source, 0, len(wins))
	for _, w := range wins {
		if !isLikelyUserWindow(w) {
			continue
		}
		key := w.App + "|" + w.Title
		if w.Title == "" {
			// Untitled windows tend to be transient overlays; if an app
			// has multiple, keep the largest and drop the rest by
			// keying purely on the app name in that case.
			key = w.App + "|<untitled>"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		src := windowToSource(w)
		if isFullscreenSpace(w, displayBounds) {
			src.Name = "Fullscreen Space — " + src.Name
		}
		windowSrcs = append(windowSrcs, src)
	}

	// Sort: on-screen items first (they can preview right now), then
	// fullscreen-space candidates (the user's primary recordable target),
	// then everything else alphabetically. This puts the actually-useful
	// rows at the top of the picker.
	sort.SliceStable(windowSrcs, func(i, j int) bool {
		if windowSrcs[i].OnScreen != windowSrcs[j].OnScreen {
			return windowSrcs[i].OnScreen
		}
		iFs := strings.HasPrefix(windowSrcs[i].Name, "Fullscreen Space")
		jFs := strings.HasPrefix(windowSrcs[j].Name, "Fullscreen Space")
		if iFs != jFs {
			return iFs
		}
		return windowSrcs[i].Name < windowSrcs[j].Name
	})

	return append(out, windowSrcs...)
}

// isLikelyUserWindow filters out XPC/UI helpers and popovers. The rules are
// deliberately heuristic — SCK doesn't expose a "real window" flag — but
// they reflect what the user perceives as a recordable app window.
func isLikelyUserWindow(w macWindow) bool {
	if isHelperApp(w.App) {
		return false
	}
	// Drop popovers/tooltips. A real app window worth recording is at least
	// the size of a small dialog. Empty-title windows have to be larger
	// because real apps tend to title their main window.
	if w.Title == "" {
		if w.Width < 400 || w.Height < 300 {
			return false
		}
	} else {
		if w.Width < 200 || w.Height < 120 {
			return false
		}
	}
	return true
}

// isHelperApp returns true when the owning-app name reads like an AppKit
// helper or XPC view service rather than a regular GUI application. The
// canonical examples seen in the wild are *UIViewService, *Helper, *Agent,
// *XPCService, *Daemon, plus the macOS file-dialog stand-in
// "Open and Save Panel Service (Preview)" — note the trailing parenthetical
// is what made an earlier suffix-only check miss it, so we use a substring
// match on word boundaries instead.
func isHelperApp(name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	// Word- or near-word-boundary substrings that mark a helper process.
	// Substring match catches "Open and Save Panel Service (Preview)";
	// the trailing space variants catch cases like
	// "Audio Component Helper" where the helper word isn't at the end.
	needles := []string{
		"service",
		"helper",
		"agent",
		"daemon",
		"xpc",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// isFullscreenSpace returns true when the window's pixel footprint matches
// any physical display within a small tolerance. macOS gives every
// fullscreen app its own Space whose backing window inherits the display's
// dimensions almost exactly — that's the signal we use to recognise the
// "maximized app you swipe to" without needing the private Spaces API.
func isFullscreenSpace(w macWindow, displays []image.Rectangle) bool {
	for _, b := range displays {
		dw, dh := b.Dx(), b.Dy()
		if absInt(w.Width-dw) <= 8 && absInt(w.Height-dh) <= 80 {
			return true
		}
	}
	return false
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// captureSource captures the full target. For a window source the result is
// the window's current pixels regardless of which macOS Space currently has
// focus, courtesy of SCContentFilter initWithDesktopIndependentWindow.
func captureSource(src Source) (*image.RGBA, error) {
	switch src.Kind {
	case SourceKindDisplay:
		idx, err := strconv.Atoi(strings.TrimPrefix(src.ID, SourceKindDisplay+":"))
		if err != nil {
			return nil, fmt.Errorf("invalid display source id %q", src.ID)
		}
		return captureFrame(idx)
	case SourceKindWindow:
		id, err := parseWindowID(strings.TrimPrefix(src.ID, SourceKindWindow+":"))
		if err != nil {
			return nil, fmt.Errorf("invalid window source id %q", src.ID)
		}
		return captureMacWindow(id)
	default:
		return nil, fmt.Errorf("unknown source kind %q", src.Kind)
	}
}

// captureSourceRegion captures a sub-rectangle of the target in
// target-local coordinates (0,0 = top-left of the display or window).
func captureSourceRegion(src Source, x, y, w, h int) (*image.RGBA, error) {
	switch src.Kind {
	case SourceKindDisplay:
		idx, err := strconv.Atoi(strings.TrimPrefix(src.ID, SourceKindDisplay+":"))
		if err != nil {
			return nil, fmt.Errorf("invalid display source id %q", src.ID)
		}
		return captureRegion(idx, x, y, w, h)
	case SourceKindWindow:
		id, err := parseWindowID(strings.TrimPrefix(src.ID, SourceKindWindow+":"))
		if err != nil {
			return nil, fmt.Errorf("invalid window source id %q", src.ID)
		}
		return captureMacWindowRegion(id, x, y, w, h)
	default:
		return nil, fmt.Errorf("unknown source kind %q", src.Kind)
	}
}
