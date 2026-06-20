# ADR 0001: Agent-Readable CLI Output

## Status

Accepted

## Context

AI agents need stable, compact output they can parse without scraping human prose. The CLI already writes frames and spritesheet metadata, but the run result was only stderr text. Dumping every generated file path to stdout is also wasteful for long captures.

## Decision

Add `--json` for machine-readable command output:

- `--list-sources --json` emits a compact JSON object with `ok`, `count`, and `sources`.
- Capture runs with `--json` emit a compact summary object on stdout.
- Every capture run writes `manifest.json` in the output directory with the full file list, dimensions, byte sizes, source, fps, timing, region, scaling, format, and spritesheet metadata.

## Consequences

Agents can use stdout for a low-token decision point and read `manifest.json` only when they need full detail. Human stderr logs remain the default for non-JSON runs.
