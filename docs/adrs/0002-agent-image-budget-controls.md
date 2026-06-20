# ADR 0002: Agent Image-Budget Controls

## Status

Accepted

## Context

Full-screen PNG capture is often too large for multimodal agent workflows. Agents need simple controls over frame count, pixel area, and encoded size without adding dependencies or a video pipeline.

## Decision

Add low-cost CLI controls:

- `--frames N` caps capture length directly.
- `--region x,y,w,h` crops source-local pixels before encoding.
- `--max-dim N` downscales each frame so the longest edge is at most `N`.
- `--format png|jpg|jpeg` selects PNG or JPEG output.
- `--quality 1..100` controls JPEG quality.

Use `--max-dim` rather than separate `--max-width` and `--max-height` for now. It is simpler to document, preserves aspect ratio, and covers the common agent need: bounding pixel cost. The scaler is a deterministic nearest-neighbor implementation using only the Go standard library.

## Consequences

Agents can bound output with a short command such as `--frames 3 --region 0,0,1280,720 --max-dim 768 --format jpg --quality 80`. More precise width/height controls and adaptive byte-budgeting remain possible later, but are deferred until the simpler controls prove insufficient.
