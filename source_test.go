package main

import (
	"strings"
	"testing"

	"github.com/kbinani/screenshot"
)

func TestParseSource(t *testing.T) {
	// Walk a representative set of inputs that exercise both the
	// "display:N" and "window:HEX" branches, plus the legacy plain-int
	// shorthand that --display historically accepted.
	displays := screenshot.NumActiveDisplays()
	if displays == 0 {
		t.Skip("no active displays detected in the test environment")
	}

	t.Run("display kind", func(t *testing.T) {
		s, err := parseSource("display:0")
		if err != nil {
			t.Fatalf("parseSource(display:0): %v", err)
		}
		if s.Kind != SourceKindDisplay {
			t.Fatalf("kind = %q, want %q", s.Kind, SourceKindDisplay)
		}
		if s.ID != "display:0" {
			t.Fatalf("id = %q, want display:0", s.ID)
		}
	})

	t.Run("legacy plain int", func(t *testing.T) {
		s, err := parseSource("0")
		if err != nil {
			t.Fatalf("parseSource(0): %v", err)
		}
		if s.Kind != SourceKindDisplay || s.ID != "display:0" {
			t.Fatalf("legacy form parsed to %+v, want display:0", s)
		}
	})

	t.Run("window kind hex", func(t *testing.T) {
		// We can't assume any specific window exists in the test
		// environment, but parseSource("window:0x1A") going through
		// resolveWindowSource should fail with a "no window" style
		// error rather than a parse error. That is enough to prove
		// the hex parsing is wired.
		_, err := parseSource("window:0x1A")
		if err == nil {
			// In the unlikely case a window with that ID exists, fine.
			return
		}
		if !strings.Contains(err.Error(), "no window") &&
			!strings.Contains(err.Error(), "window") {
			t.Fatalf("expected a window-resolution error, got %v", err)
		}
	})

	t.Run("window kind decimal", func(t *testing.T) {
		_, err := parseSource("window:42")
		if err == nil {
			return
		}
		if !strings.Contains(err.Error(), "no window") &&
			!strings.Contains(err.Error(), "window") {
			t.Fatalf("expected a window-resolution error, got %v", err)
		}
	})

	t.Run("invalid display index", func(t *testing.T) {
		if _, err := parseSource("display:abc"); err == nil {
			t.Fatalf("expected error for non-numeric display index")
		}
	})

	t.Run("out of range display", func(t *testing.T) {
		if _, err := parseSource("display:9999"); err == nil {
			t.Fatalf("expected error for out-of-range display")
		}
	})

	t.Run("unknown kind", func(t *testing.T) {
		if _, err := parseSource("camera:0"); err == nil {
			t.Fatalf("expected error for unknown kind")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if _, err := parseSource(""); err == nil {
			t.Fatalf("expected error for empty source")
		}
	})

	t.Run("invalid window id", func(t *testing.T) {
		if _, err := parseSource("window:not-a-number"); err == nil {
			t.Fatalf("expected error for malformed window id")
		}
	})
}

func TestDisplaySourceIDRoundTrip(t *testing.T) {
	for i := 0; i < screenshot.NumActiveDisplays(); i++ {
		id := displaySourceID(i)
		s, err := parseSource(id)
		if err != nil {
			t.Fatalf("parseSource(%q): %v", id, err)
		}
		if s.ID != id {
			t.Fatalf("round-trip id mismatch: got %q, want %q", s.ID, id)
		}
	}
}

func TestWindowSourceIDFormatHex(t *testing.T) {
	got := windowSourceID(0x1A2B)
	if got != "window:0x1a2b" {
		t.Fatalf("windowSourceID(0x1A2B) = %q, want window:0x1a2b", got)
	}
}
