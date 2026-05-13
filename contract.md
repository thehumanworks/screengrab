# screengrab — verification contract

Binary criteria for "done". Each row is checkable with a single command and either passes or fails.

| # | Criterion | Verifiable signal |
|---|-----------|-------------------|
| 1 | Source compiles to a single binary on macOS | `cd screengrab && go build -o screengrab .` exits 0 and produces `./screengrab` |
| 2 | CLI exposes the required flags | `./screengrab --help` exit 0 and stdout contains all of: `--fps`, `--duration`, `--output`, `--mode`, `--display` |
| 3 | Default FPS is sane for AI consumption (≤ 4) | `grep -n "defaultFPS" main.go` resolves to a constant whose value is ≤ 4 |
| 4 | Frames mode produces N=fps*duration individual PNGs | `./screengrab --duration 2s --fps 2 --mode frames --output out_frames` produces exactly 4 files matching `out_frames/frame_*.png`, each a valid PNG per `file` |
| 5 | Spritesheet mode produces one composite PNG plus sidecar JSON | `./screengrab --duration 2s --fps 2 --mode spritesheet --output out_sheet` produces `out_sheet/spritesheet.png` (valid PNG) and `out_sheet/spritesheet.json` describing rows, cols, frame count, frame size |
| 6 | SIGINT triggers a clean stop and flushes output | Sending SIGINT mid-run leaves a non-empty output directory and exits 0 |

Non-goals: audio, cursor capture toggling, region selection, Linux/Windows hard verification (best-effort via `kbinani/screenshot`'s cross-platform backends; macOS is the verified target).

Permission note: on macOS the first run prompts for Screen Recording permission in System Settings → Privacy & Security. If denied, captures return solid-black frames, which is a host permission issue rather than a CLI defect.
