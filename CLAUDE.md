# Agent guide for `screengrab`

This file gives AI coding agents the load-bearing facts about this repo so they can make changes without first having to discover the architecture. Read this before editing anything.

## What this project is

A single-binary Go CLI that samples the screen at a fixed FPS and writes the result as individual PNGs or as one spritesheet PNG plus a JSON sidecar. There is no video pipeline. Default FPS is intentionally low (2) because the output is intended for vision-capable LLMs, which prefer a handful of stills over a long frame stream.

The behaviour is locked down by `contract.md`. Treat that file as the executable spec — any change that breaks a criterion is a regression, any new feature that is worth keeping should add a new binary criterion to that file.

## Files that matter

- `main.go` — the entire CLI. ~250 lines, no internal packages. The capture loop, flag parsing, frames writer, and spritesheet compositor all live here.
- `contract.md` — six binary acceptance criteria. The verification recipe at the bottom of this guide maps 1:1 to these.
- `go.mod` / `go.sum` — only direct dependency is `github.com/kbinani/screenshot`.
- `.gitignore` — already excludes the compiled `screengrab` binary, output directories like `out_*/`, and the usual Go/macOS junk. Do not commit binaries or capture output.

## Build, run, verify

Authoritative commands. Run these from inside `screengrab/`:

```sh
# Build (must exit 0)
go build -o screengrab .

# Help (must exit 0 and list --fps, --duration, --output, --mode, --display)
./screengrab --help

# Frames mode (must produce exactly 4 PNGs at the display resolution)
./screengrab --duration 2s --fps 2 --mode frames --output out_frames
ls out_frames/*.png | wc -l   # → 4

# Spritesheet mode (must produce spritesheet.png + spritesheet.json)
./screengrab --duration 2s --fps 2 --mode spritesheet --output out_sheet
file out_sheet/spritesheet.png    # → PNG image data, 3840 x 2160, ...
cat out_sheet/spritesheet.json    # → {"frames":4,"cols":2,"rows":2,...}

# SIGINT (must exit 0 and leave a non-empty output directory)
./screengrab --output out_sigint &
sleep 1.5 && kill -INT $!
wait
ls out_sigint/*.png
```

If any of these regress, the contract is broken and the change needs to be reverted or fixed before merging.

## Design invariants — do not break these without updating `contract.md`

1. **The default FPS is sane for AI ingestion.** `const defaultFPS` lives at the top of `main.go` and must stay ≤ 4. If you raise it, add a binary criterion in `contract.md` justifying the new ceiling.
2. **`--duration N` produces exactly `round(fps * N)` frames.** This determinism is what makes the contract checkable. The capture loop uses a frame counter, not a deadline race against the ticker, on purpose. If you switch to a deadline-driven model, the count becomes non-deterministic and contract #4 starts flaking.
3. **Capture starts with an immediate frame at t=0.** This way short durations (e.g. `--duration 500ms --fps 2 = 1 frame`) still produce output instead of zero frames waiting for the first ticker fire.
4. **SIGINT/SIGTERM are clean stops, not crashes.** The signal handler breaks the loop, the writer flushes anything already captured, and the process exits 0. Don't replace this with `log.Fatal` or `os.Exit(1)`.
5. **Spritesheet mode holds frames in memory.** This is a known constraint, documented in the README. If you stream to disk and composite at the end, you need to delete the temp PNGs unless the user opts to keep them, otherwise users will be surprised by `frames` output appearing in spritesheet mode.

## Platform reality

- Verified on macOS arm64 (Darwin 25.5, Go 1.26).
- The capture backend (`kbinani/screenshot`) has Linux X11 and Windows builds but they are not tested here. Do not claim cross-platform verification without actually running on the target.
- On macOS, the first capture requires Screen Recording permission. If you see captures that are all-black, it is a permission issue, not a code defect. Don't chase it as a bug.

## Style and scope rules

- **No comments explaining what code does.** Only add a comment when the *why* is non-obvious (an invariant, a deliberate non-race ordering, a workaround). The existing comment near the `targetFrames` block in `main.go` is the right shape: it explains the determinism reason, not the line-by-line behaviour.
- **No premature abstraction.** This is a 250-line single-file CLI. Don't split it into packages, don't add an interface for "capture backends" until there's a second backend, don't introduce config files. New flags go on the existing `flag.*` block in `parseFlags`.
- **No new dependencies without a reason.** `image`, `image/draw`, `image/png`, `encoding/json`, and `flag` from stdlib cover everything except actual screen capture.
- **Don't add error handling for impossible states.** Trust stdlib and `kbinani/screenshot` return values. Validate at the CLI boundary (flag values, output path, display index) and let internal errors bubble up.
- **AGENTS.md is a symlink to this file.** If you're tempted to write two slightly different sets of guidance, write it once here and let the symlink do the work.

## How to extend safely

If you add a feature (e.g. region selection, downscaling, GIF output):

1. Add a binary criterion to `contract.md` first. Phrase it as a single command and an observable outcome (file produced, exit code, stdout substring).
2. Add the flag in `parseFlags` with a sensible default that doesn't change existing behaviour.
3. Run the full verification recipe above to confirm no regression on the original six criteria.
4. Update the README's flag table and quick-start examples.

## What not to do

- Don't add an HTTP server, a daemon mode, or a config file. Out of scope.
- Don't ship the compiled binary in the repo. The build is fast and reproducible; pre-built binaries belong on GitHub Releases, not in `git`.
- Don't replace `kbinani/screenshot` with a shell-out to `screencapture` or `ffmpeg`. The point of this tool is to be a self-contained Go binary with no runtime deps.
- Don't change `main.go:21` (`const defaultFPS = 2.0`) without an explicit reason and an updated contract entry. The "low default for AI consumption" promise in the README depends on it.
