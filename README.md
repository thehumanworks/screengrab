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
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fps` | `2` | Frames per second. Kept deliberately low because output is intended for AI models. |
| `--duration` | `0` (run until Ctrl+C) | Max recording duration, e.g. `10s`, `1m`, `90s`. When set, exactly `round(fps * seconds)` frames are captured. |
| `--output` | `screengrab-out` | Output directory (created if missing). |
| `--mode` | `frames` | `frames` writes one PNG per capture; `spritesheet` writes one composite PNG plus `spritesheet.json`. |
| `--display` | `0` | Display index. `0` is the primary display. |
| `--cols` | `0` (auto) | Columns in spritesheet grid; defaults to `ceil(sqrt(frame_count))`. |

`Ctrl+C` (SIGINT) and SIGTERM stop a long-running capture cleanly and flush any frames already captured.

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

## macOS permissions

The first time you run `screengrab`, macOS will prompt for Screen Recording permission under **System Settings → Privacy & Security → Screen Recording**. Grant it to your terminal app (Terminal, iTerm2, Ghostty, etc.). If permission is denied, captures still succeed mechanically but produce solid-black frames.

## Cross-platform

The capture backend is [`kbinani/screenshot`](https://github.com/kbinani/screenshot), which has builds for macOS, Linux (X11), and Windows. macOS is the verified target; the other platforms compile from the same source but are untested here.

## Performance notes

- In `spritesheet` mode, all frames are held in memory until the run ends. A 5K display at 5120×2880 is roughly 60 MB per RGBA frame, so long sheet captures can use serious RAM. For long captures, prefer `frames` mode and composite externally if needed.
- The capture is naive `screencapture`-style sampling — frames are independent stills, not delta-compressed. PNG file sizes are large; that is the trade-off for a video-free pipeline.

## Project layout

```
screengrab/
  main.go         # CLI, capture loop, frame and spritesheet writers
  contract.md     # binary acceptance criteria for the project
  go.mod / go.sum
  README.md       # this file
  CLAUDE.md       # guidance for AI agents working on this repo
  AGENTS.md       # symlink → CLAUDE.md
```

## License

Not specified. Treat as private until a license is added.
