# ADR 0003: Generated Output Overwrite Safety

## Status

Accepted

## Context

The previous CLI used `os.Create`, so generated frames and spritesheets could silently replace earlier captures. That is risky for unattended agents and makes failure recovery ambiguous.

## Decision

Refuse to write into an output directory that already contains generated screengrab files unless `--overwrite` is set. Generated files include `frame_*`, `spritesheet.*`, and `manifest.json`.

When `--overwrite` is set, remove known generated files before capture so stale frames from a longer previous run do not remain mixed with the new result.

## Consequences

Default CLI behavior is safer for agents. Existing scripts that intentionally reuse an output directory must opt in with `--overwrite` or use a fresh `--output`.
