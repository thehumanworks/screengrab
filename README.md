# screengrab

A small Go CLI that records the screen and emits **frames** instead of a video. Designed for feeding short screen captures into vision-capable AI models, which prefer a handful of still images over an MP4.

You start it, do something on screen, stop it (or let a timer expire), and you get a folder of PNGs or a single spritesheet PNG with a JSON sidecar describing the grid.

## Why frames, not video

Most multimodal LLMs accept images, not video. Re-encoding to MP4 and then sampling frames externally is round-trip overhead with two failure points. `screengrab` skips the video step entirely: it samples the screen at a fixed FPS and writes PNGs directly, with a sane default rate (2 fps) that keeps the frame count manageable.

## Install

Requires Go 1.26+ (matches the `go` directive in `go.mod`). Single-binary build, no runtime dependencies.

```sh
git clone https://github.com/thehumanworks/screengrab
cd screengrab
go build -o screengrab .
```

The resulting `./screengrab` is a self-contained binary. On macOS arm64 it is around 3.5 MB.

## Quick start

```sh
# Record for 10 seconds at 2 fps, write individual PNGs to ./capture/
./screengrab --duration 10s --output capture

# Record until you press Ctrl+C
./screengrab --output capture

# 5-second clip composited into one spritesheet PNG
./screengrab --duration 5s --mode spritesheet --output sheet

# Faster sampling, secondary display
./screengrab --duration 4s --fps 4 --display 1 --output clip

# Agent-friendly bounded capture: 3 JPEG frames, cropped and downscaled,
# with compact JSON on stdout and full metadata in manifest.json
./screengrab --frames 3 --region 0,0,1280,720 --max-dim 768 --format jpg --quality 80 --json --output clip

# Discover capturable sources (displays + macOS windows, incl. maximized apps
# on inactive Spaces that normally need a swipe to reach)
./screengrab --list-sources

# Discover sources as JSON for scripts/agents
./screengrab --list-sources --json

# Record a single macOS window by its ID from --list-sources
./screengrab --duration 4s --source window:0x21cb --output window-clip

# Record microphone narration and create a timed, on-device transcript
./screengrab --duration 10s --microphone --transcript --output narrated-capture

# Record what the captured source is playing (system audio) alongside frames,
# plus your narration, with a transcript for each track
./screengrab --duration 10s --audio both --transcript --output demo-capture
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fps` | `2` | Frames per second. Kept deliberately low because output is intended for AI models. |
| `--duration` | `0` (run until Ctrl+C) | Max recording duration, e.g. `10s`, `1m`, `90s`. When set, exactly `round(fps * seconds)` frames are captured. |
| `--frames` | `0` | Maximum frames to capture. `0` means derive from `--duration` or run until stopped. When both `--duration` and `--frames` are set, the smaller frame count wins. |
| `--output` | `screengrab-out` | Output directory (created if missing). |
| `--mode` | `frames` | `frames` writes one PNG per capture; `spritesheet` writes one composite PNG plus `spritesheet.json`. |
| `--format` | `png` | Image format: `png`, `jpg`, or `jpeg`. JPEG output uses `.jpg` filenames. |
| `--quality` | `85` | JPEG quality from `1` to `100`; ignored for PNG. |
| `--display` | `0` | Display index. `0` is the primary display. Kept as a shorthand for `--source display:N`. |
| `--source` | (unset) | Explicit capture source: `display:N` for a physical display, or `window:0xID` for a macOS window (run `--list-sources` to get the IDs). Overrides `--display` when present. |
| `--list-sources` | `false` | Print every capturable source (displays and macOS windows) and exit. |
| `--region` | (unset) | Optional source-local crop rectangle, formatted as `x,y,w,h`, applied before scaling and encoding. |
| `--max-dim` | `0` | Downscale each frame so its longest edge is at most this many pixels. `0` keeps original size. |
| `--cols` | `0` (auto) | Columns in spritesheet grid; defaults to `ceil(sqrt(frame_count))`. |
| `--json` | `false` | Emit machine-readable JSON for `--list-sources` and final capture summaries. Capture summaries point to `manifest.json` for the full file list. |
| `--overwrite` | `false` | Allow replacing generated files already present in the output directory. Without this, existing `frame_*`, `spritesheet.*`, or `manifest.json` files cause a fail-fast error. |
| `--audio` | (unset) | Audio tracks to record on macOS: `mic` (default microphone → `audio.wav`), `system` (what the captured display or window is playing → `system_audio.wav`), or `both`. Audio recording is opt-in. |
| `--microphone` | `false` | Shorthand for `--audio mic`; kept for back-compat. Combining it with `--audio system` records both tracks. |
| `--transcript` | `false` | Create timed transcripts (`transcript.*` for the microphone, `system_transcript.*` for system audio) using on-device recognition. Requires `--audio` or `--microphone`. |
| `--transcript-locale` | system locale | Recognition locale such as `en-GB` or `en-US`. Requires `--transcript`. |
| `--gui` | `false` | Launch the cross-platform desktop GUI instead of the headless CLI flow. |
| `--devtools` | `false` | Open the webview developer tools panel when `--gui` is set. |

`Ctrl+C` (SIGINT) and SIGTERM stop a long-running capture cleanly and flush any frames already captured.

## Desktop GUI

Run `./screengrab --gui` to open the desktop view, powered by [Wails v3](https://v3.wails.io). The Go side opens an OS-native webview; on macOS 26+ the window itself is backed by `NSGlassEffectView` (real Apple Liquid Glass), falling back to `NSVisualEffectView` on earlier macOS. On Windows and Linux the webview is opaque and the matching CSS Liquid Glass recipe in `frontend/style.css` carries the look.

The flow is:

1. **Setup**: pick a capture source from the dropdown — the list is grouped into *Displays* (physical screens) and *Windowed apps*, where each window is labelled with its owning application. A maximized macOS app on its own Space appears in the windowed-apps group, so you can record it without having to swipe over there first. Then set the FPS, output directory, region, and optionally enable microphone recording and on-device transcription.
2. **Recording**: click *Start Recording*; the GUI captures at the configured FPS into a timestamped sub-directory of the output base. A visible microphone state remains on while audio is being recorded. Press *Stop Recording* when done.
3. **Transcribing**: when requested, microphone capture is finalized and the GUI shows a separate transcription state before review.
4. **Review**: every captured frame appears as a thumbnail in a grid. The review also identifies the audio artifact and displays a completed transcript. Click frames to toggle selection, or use *Select All* / *Clear*.
5. **Save**: click *Save Selected & Copy Path*. The selected frames, complete audio, transcript files, and a rewritten manifest are copied to a new `selected-YYYYMMDD-HHMMSS/` sub-directory. The absolute path is written to the clipboard.

The GUI shares the same `captureSource` / `captureSourceRegion` primitives as the CLI; flags like `--display`, `--fps`, and `--output` are honoured as initial values and can be edited live in the setup screen. Pass `--devtools` if you need to open the webview developer panel.

## Output

### `frames` mode

```
out/
  frame_0000.png
  frame_0001.png
  frame_0002.png
  ...
```

Each PNG is the full display resolution, 8-bit per channel, non-interlaced.
If `--format jpg` is used, frame files are JPEGs named `frame_0000.jpg`,
`frame_0001.jpg`, and so on.

### `spritesheet` mode

```
out/
  spritesheet.png      # one big PNG, grid of frames
  spritesheet.json     # describes the grid
```

`spritesheet.json` example:

```json
{
  "frames": 4,
  "cols": 2,
  "rows": 2,
  "frame_width": 1920,
  "frame_height": 1080,
  "sheet_width": 3840,
  "sheet_height": 2160,
  "fps": 2
}
```

Use the metadata to slice the sheet back into frames programmatically. Frames are laid out left-to-right, top-to-bottom in capture order.

### `manifest.json`

Every capture run writes `manifest.json` in the output directory. It records the
source, fps, requested and captured frame counts, crop region, `--max-dim`,
format/quality, output dimensions, byte sizes, elapsed time, and generated
file paths. Manifest version 2 adds per-frame capture offsets and, for
audio-enabled captures, offsets into each audio timeline
(`audio_offset_seconds` for the microphone, `system_audio_offset_seconds` for
system audio) plus `audio` / `system_audio` and `transcript` /
`system_transcript` artifact objects. This preserves synchronization even when
an off-Space window skips frames. With `--json`, stdout is a compact run summary that includes
`manifest_path`; read the manifest only when you need the full file list or
timed transcript associations.

### Audio tracks and transcripts

An audio-enabled capture (`--audio both --transcript` here) adds these sidecars
without creating a video:

```text
out/
  frame_0000.png
  frame_0001.png
  audio.wav               # microphone (mic / both)
  system_audio.wav        # system audio (system / both)
  transcript.txt
  transcript.json
  system_transcript.txt
  system_transcript.json
  manifest.json
```

`audio.wav` is 16-bit PCM from the default microphone; `system_audio.wav` is
16-bit PCM of whatever the captured source is playing, recorded via
ScreenCaptureKit's audio stream. When the source is a window, system audio is
scoped to the owning app; when it is a display, the full system mix is
recorded. The two tracks stay separate on purpose — "what the user said" and
"what the machine played" are distinct signals for a consuming AI agent, and
each gets its own timed transcript.

`transcript.txt` is the readable text; `transcript.json` contains ordered
segments with start/end times, text, and confidence when macOS supplies it
(same shape for `system_transcript.*`). Transcription uses Apple's on-device
Speech recognizer and never falls back to a cloud service. If on-device
recognition is unavailable for the locale, the screen frames and audio remain
usable and the manifest records the transcript as unavailable.

## macOS permissions

The first time you run `screengrab`, macOS will prompt for Screen Recording permission under **System Settings → Privacy & Security → Screen Recording**. Grant it to your terminal app (Terminal, iTerm2, Ghostty, etc.). If permission is denied, captures still succeed mechanically but produce solid-black frames.

`--audio system` needs no extra permission — ScreenCaptureKit's audio stream is
covered by the same Screen Recording grant as the frames.
`--microphone` (or `--audio mic`) additionally requests **Microphone** access. `--transcript` requests
**Speech Recognition** access and requires an on-device recognizer for the chosen
locale. These permissions are requested only when their flags or GUI controls are
enabled. If microphone or speech permission is denied, capture fails before the
first frame so it cannot be mistaken for a complete narrated recording. Packaged
`.app` builds include the corresponding usage descriptions in `Info.plist` and
are ad-hoc signed for local testing. Distribution builds still require a
Developer ID signature and notarization.

### macOS Spaces and "windowed apps"

A maximized macOS application lives on its own Space — you swipe between Spaces using the trackpad or `Ctrl-←` / `Ctrl-→`. `screengrab --list-sources` lists every such app window, and the GUI shows them under a *Windowed apps* group. Each entry is tagged `[live]` when its content is currently on the active Space, or `[off-Space]` when it is on a different Space.

You can pick an `[off-Space]` window as your recording target. ScreenCaptureKit may have a capture-ready frame for it, but that is not guaranteed; when it does not, `SCScreenshotManager` reports a generic audio/video capture failure until the compositor makes the window available again. In practice this means:

- Region picker preview is only available for `[live]` sources.
- Recording an off-Space window waits instead of exiting. The CLI prints one `waiting for window:…` message; the GUI emits `capture:frame_error` events for its status UI. The moment you swipe to that Space, capture resumes and frames begin landing on disk.

The recommended flow for a fullscreen app is therefore: select its `window:0xID` source in the CLI (or pick it in the GUI), start recording, then swipe to the target Space and interact with the app. When you stop, output contains only the frames that landed while the Space was visible.

## Cross-platform

The capture backend is [`kbinani/screenshot`](https://github.com/kbinani/screenshot), which has implementations for macOS, Linux (X11), and Windows. The desktop GUI uses Wails v3, whose webview is WebKit on macOS, WebView2 on Windows, and WebKitGTK on Linux. macOS arm64 is the only verified target; full builds on the other platforms are unverified. Microphone recording, system-audio recording, and transcription are currently macOS-only; their non-macOS stubs return an explicit unsupported error instead of silently producing screen-only output.

Wails v3 is currently in **alpha** (this build uses `v3.0.0-alpha.91`). The native Liquid Glass APIs (`MacBackdropLiquidGlass`, `LiquidGlassStyle*`, `NSVisualEffectMaterial*`) are macOS-only; non-Mac platforms render a transparent webview that falls back to the CSS recipe.

## Performance notes

- In `spritesheet` mode, all frames are held in memory until the run ends. A 5K display at 5120×2880 is roughly 60 MB per RGBA frame, so long sheet captures can use serious RAM. For long captures, prefer `frames` mode and composite externally if needed.
- The capture is naive `screencapture`-style sampling — frames are independent stills, not delta-compressed. PNG file sizes are large; that is the trade-off for a video-free pipeline.

## Project layout

```
screengrab/
  main.go             # CLI, capture loop, frame and spritesheet writers, captureFrame / captureRegion helpers
  source.go           # unified Source abstraction (display:N | window:0xID), parseSource, listSources, captureSource
  mac_capture.go      # darwin-only CGo bindings to ScreenCaptureKit for window enumeration and capture
  mac_capture_stub.go # !darwin stub: returns ErrWindowCaptureUnsupported
  audio.go            # audio recorder lifecycle (mic + system) and artifact metadata
  mac_audio.go        # darwin AVFoundation microphone recorder
  mac_audio_stub.go   # explicit unsupported behavior on other platforms
  mac_system_audio.go # darwin ScreenCaptureKit system-audio recorder
  mac_system_audio_stub.go # explicit unsupported behavior on other platforms
  transcript.go       # transcript schema and durable artifact writers
  mac_transcript.go   # darwin on-device Speech framework bridge
  mac_transcript_stub.go # explicit unsupported behavior on other platforms
  gui.go              # Wails v3 desktop entry: MacLiquidGlass window + captureService bindings
  gui_test.go         # tests for the copyFrames helper used by the GUI's save flow
  source_test.go      # parseSource + Source ID round-trip tests
  frontend/
    index.html        # four-view shell (setup, region picker, recording, review)
    style.css         # Liquid Glass content surfaces + reduced-transparency / contrast / motion fallbacks
    main.js           # state machine, Wails runtime calls via Call.ByName, drag-to-select region picker
  contract.md         # binary acceptance criteria for the project (CLI + GUI)
  go.mod / go.sum
  README.md           # this file
  CLAUDE.md           # guidance for AI agents working on this repo
  AGENTS.md           # symlink → CLAUDE.md
```

## License

Not specified. Treat as private until a license is added.
