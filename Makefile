# screengrab — top-level Makefile.
#
# All targets are .PHONY; the binary itself is the only file artifact and is
# tracked via its name. Override PREFIX or BIN if you want a different install
# location, e.g. `make install PREFIX=$HOME/.local`.

BINARY      := screengrab
PKG         := .
PREFIX      ?= /usr/local
BIN         ?= $(PREFIX)/bin
GO          ?= go
GOFLAGS     ?=
BUILD_TAGS  ?=

# macOS deployment target. The Wails v3 Liquid Glass code paths compile
# against the macOS 26 SDK, so the linker has to be told to match — otherwise
# `ld` warns that the object files were built for a newer macOS than the link
# target (Go's default is 11.0).
#
# MACOSX_DEPLOYMENT_TARGET covers cgo's compile step. The Go linker, however,
# hardcodes minos=11.0 on darwin regardless of the env var, so we also pass
# `-Wl,-platform_version` through extldflags to make the final Mach-O record
# `minos 26.0`. The residual single-line "passed two min versions" warning is
# benign — `ld` picks the higher of the two and the binary is correct.
MACOSX_DEPLOYMENT_TARGET ?= 26.0
export MACOSX_DEPLOYMENT_TARGET

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
EXTLDFLAGS  := -Wl,-platform_version,macos,$(MACOSX_DEPLOYMENT_TARGET),$(MACOSX_DEPLOYMENT_TARGET)
LDFLAGS     ?= -s -w -extldflags=$(EXTLDFLAGS)
else
LDFLAGS     ?= -s -w
endif

# Optional version stamp; falls back to the short git SHA, or "dev" outside a
# repo. Surfaced in `--version` only if main.go ever reads it via -ldflags -X.
VERSION     ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} \
	  /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ─── Build ──────────────────────────────────────────────────────────────

.PHONY: build
build: ## Compile the binary into ./screengrab.
	$(GO) build $(GOFLAGS) -tags '$(BUILD_TAGS)' -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

.PHONY: build-release
build-release: ## Strip + trimpath release build for distribution.
	$(GO) build $(GOFLAGS) -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

# ─── Run ────────────────────────────────────────────────────────────────

.PHONY: run
run: build ## Build then run a 5 second capture into out_demo/.
	./$(BINARY) --duration 5s --fps 2 --mode frames --output out_demo

.PHONY: gui
gui: build ## Build then launch the Wails desktop GUI.
	./$(BINARY) --gui

.PHONY: devtools
devtools: build ## Build then launch the GUI with the webview devtools panel open.
	./$(BINARY) --gui --devtools

# ─── Test / lint ────────────────────────────────────────────────────────

.PHONY: test
test: ## Run the Go test suite.
	$(GO) test -count=1 -ldflags '$(LDFLAGS)' ./...

.PHONY: test-race
test-race: ## Run tests with the data race detector.
	$(GO) test -race -count=1 -ldflags '$(LDFLAGS)' ./...

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format Go sources in place.
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## Reconcile go.mod and go.sum.
	$(GO) mod tidy

# ─── Contract verification ──────────────────────────────────────────────
#
# These commands mirror contract.md. If any step fails, the change being
# proposed has regressed a binary acceptance criterion.

.PHONY: verify
verify: build ## Run every contract criterion end-to-end.
	@echo "→ C2: required CLI flags"
	@./$(BINARY) --help 2>&1 | grep -qE '\-fps' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-duration' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-output' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-mode' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-display' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-json' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-frames' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-region' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-max-dim' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-format' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-quality' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-overwrite' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-microphone' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-transcript' && \
	  ./$(BINARY) --help 2>&1 | grep -qE '\-transcript-locale'
	@echo "→ C3: defaultFPS ≤ 4"
	@grep -qE '^const defaultFPS = ([0-4](\.[0-9]+)?)$$' main.go
	@echo "→ C4: frames mode produces fps*duration PNGs"
	@rm -rf out_frames && ./$(BINARY) --duration 2s --fps 2 --mode frames --output out_frames >/dev/null
	@count=$$(ls out_frames/*.png 2>/dev/null | wc -l | tr -d ' ') && \
	  [ "$$count" = "4" ] || (echo "expected 4 frames, got $$count" && exit 1)
	@test ! -e out_frames/audio.wav && test ! -e out_frames/transcript.txt && test ! -e out_frames/transcript.json
	@echo "→ C5: spritesheet mode produces png + json"
	@rm -rf out_sheet && ./$(BINARY) --duration 2s --fps 2 --mode spritesheet --output out_sheet >/dev/null
	@test -f out_sheet/spritesheet.png && test -f out_sheet/spritesheet.json
	@echo "→ C7: --gui flag advertised"
	@./$(BINARY) --help 2>&1 | grep -qE '\-gui'
	@echo "→ C8: Wails present, Fyne absent"
	@grep -q 'github.com/wailsapp/wails/v3' go.mod
	@! grep -q 'fyne.io' go.mod
	@echo "→ C9: native macOS Liquid Glass markers"
	@grep -qE 'MacBackdropLiquidGlass' gui.go
	@grep -qE 'MacLiquidGlass\{' gui.go
	@grep -qE 'NSVisualEffectMaterial' gui.go
	@echo "→ C10: captureService is a Wails Service"
	@grep -qE 'application\.NewService\(' gui.go
	@echo "→ C11/C16: TestCopyFrames + binding regression test"
	@$(GO) test -count=1 -ldflags '$(LDFLAGS)' ./... >/dev/null
	@echo "→ C26-C30: agent-ready CLI controls"
	@$(GO) test -count=1 -ldflags '$(LDFLAGS)' ./... -run 'Test(ParseRegionSpec|PlannedFrameCountCapsDuration|FitToMaxDim|NormalizeConfigFormatAndValidation|WriteImageJPEG|PrepareOutputDirOverwriteGuard)' >/dev/null
	@echo "→ C31-C34: synchronized microphone and transcript artifacts"
	@grep -qE 'AVAudioEngine' mac_audio.go
	@grep -qE 'SFSpeechURLRecognitionRequest' mac_transcript.go
	@grep -qE 'requiresOnDeviceRecognition' mac_transcript.go
	@$(GO) test -count=1 -ldflags '$(LDFLAGS)' ./... -run 'Test(GeneratedFilePatternsIncludeAudioArtifacts|TimedArtifactValidation|CopySelectedCapturePreservesAssociatedArtifacts|CaptureServiceBindingsMatchFrontend)' >/dev/null
	@echo "→ C12: frontend is embedded"
	@grep -qE '^//go:embed.*frontend' gui.go
	@echo "→ C13: CSS Liquid Glass + a11y fallbacks"
	@grep -qE 'backdrop-filter' frontend/style.css
	@grep -qE 'prefers-reduced-transparency' frontend/style.css
	@grep -qE 'prefers-reduced-motion' frontend/style.css
	@rm -rf out_frames out_sheet out_agent
	@echo "✓ all contract criteria pass"

# ─── Install / uninstall ────────────────────────────────────────────────

.PHONY: install
install: build-release ## Install the binary system-wide (default /usr/local/bin; override with PREFIX=).
	@mkdir -p "$(BIN)"
	install -m 0755 $(BINARY) "$(BIN)/$(BINARY)"
	@echo "→ installed $(BIN)/$(BINARY)"

.PHONY: uninstall
uninstall: ## Remove the system-installed binary.
	@rm -f "$(BIN)/$(BINARY)"
	@echo "→ removed $(BIN)/$(BINARY)"

# ─── macOS .app bundle (best-effort) ────────────────────────────────────

APP_NAME    := screengrab.app
APP_DIR     := dist/$(APP_NAME)

.PHONY: app
app: build-release ## Package a minimal screengrab.app bundle on macOS.
	@if [ "$$(uname)" != "Darwin" ]; then echo "app: macOS only"; exit 1; fi
	@rm -rf "$(APP_DIR)"
	@mkdir -p "$(APP_DIR)/Contents/MacOS" "$(APP_DIR)/Contents/Resources"
	@cp $(BINARY) "$(APP_DIR)/Contents/MacOS/$(BINARY)"
	@printf '<?xml version="1.0" encoding="UTF-8"?>\n\
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n\
<plist version="1.0">\n\
<dict>\n\
  <key>CFBundleName</key><string>screengrab</string>\n\
  <key>CFBundleIdentifier</key><string>io.thehumanworks.screengrab</string>\n\
  <key>CFBundleVersion</key><string>$(VERSION)</string>\n\
  <key>CFBundleShortVersionString</key><string>$(VERSION)</string>\n\
  <key>CFBundleExecutable</key><string>$(BINARY)</string>\n\
  <key>CFBundlePackageType</key><string>APPL</string>\n\
  <key>LSMinimumSystemVersion</key><string>26.0</string>\n\
  <key>NSHighResolutionCapable</key><true/>\n\
  <key>NSScreenCaptureUsageDescription</key><string>screengrab samples the screen for AI ingestion.</string>\n\
  <key>NSMicrophoneUsageDescription</key><string>screengrab records microphone narration alongside screen captures when you enable it.</string>\n\
  <key>NSSpeechRecognitionUsageDescription</key><string>screengrab creates a local text transcript from recorded microphone audio when you enable it.</string>\n\
</dict>\n\
</plist>\n' > "$(APP_DIR)/Contents/Info.plist"
	@/usr/bin/codesign --force --deep --sign - "$(APP_DIR)"
	@echo "→ built $(APP_DIR) with an ad-hoc signature (use a Developer ID signature and notarization for distribution)"

# ─── Cleanup ────────────────────────────────────────────────────────────

.PHONY: clean
clean: ## Remove build artifacts and demo capture directories.
	rm -f $(BINARY)
	rm -rf out_* out_demo dist screengrab-out capture-* selected-*
