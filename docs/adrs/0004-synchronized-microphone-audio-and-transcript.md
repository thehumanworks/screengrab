# ADR 0004: Synchronized Microphone Audio and Transcript

## Status

Accepted

## Context

A screen capture currently contains only sampled images. That is insufficient when spoken explanation, narration, or a conversation is part of the recorded activity. The audio, transcript, and images must remain associated without turning the product into a video recorder or making silent capture more expensive or permission-heavy.

Association cannot rely on frame number alone. Frames are sampled at a low FPS, and window captures can skip intervals while the target is on an inactive macOS Space. Each successful frame and transcript segment therefore needs a timestamp on the recorded audio timeline.

Microphone audio and transcripts are sensitive data. Capture must be explicit, locally visible, and usable without silently sending audio to an external service.

## Decision

Add opt-in microphone recording and post-capture transcription to CLI and GUI capture sessions. The existing silent behavior remains the default.

### Product surface

- Add `--microphone` to record the current default input device.
- Add `--transcript` to generate `transcript.txt` and timed `transcript.json` after recording stops. `--transcript` requires `--microphone`.
- Add `--transcript-locale` for an explicit recognition locale; otherwise use the system locale.
- Add matching GUI controls. Enabling transcription also exposes its locale and requires microphone capture to remain enabled.
- Show distinct GUI states for recording audio and transcribing. The macOS recording indicator is not a substitute for an in-app microphone indicator.
- Support the verified macOS target first. Other platforms must return a clear unsupported error when either feature is requested; they must not silently produce a screen-only capture.

The first version records only the default microphone. Input-device selection, system/application audio, mixing, speaker diarization, and live caption editing are deferred.

### Capture and synchronization

Use a capture-session coordinator shared by the CLI and GUI:

1. Validate options and obtain microphone and, when requested, speech-recognition permission before starting screen capture.
2. Start the microphone recorder and confirm it is running.
3. Start the existing immediate screen capture. At the beginning of each successful frame acquisition, read the recorder's current audio time and store it as that frame's `audio_offset_seconds`.
4. On duration, frame limit, GUI stop, SIGINT, SIGTERM, or capture failure, stop the frame loop and microphone recorder exactly once and finalize the audio file.
5. If requested, transcribe the finalized audio file and write both transcript forms.
6. Write `manifest.json` last so it describes the final or partial state of every artifact.

Only successful frames appear in the manifest. Their audio offsets preserve off-Space gaps rather than pretending that remaining frame numbers are evenly spaced. Captures without microphone audio retain elapsed capture offsets but omit `audio_offset_seconds`.

### Native implementation

On macOS, add an in-tree CGo bridge using AVFoundation to record the default microphone as 16-bit PCM in `audio.wav`. Use the recorder's media time for frame association rather than deriving audio position from FPS or wall-clock time. PCM avoids a runtime encoder dependency and is accepted directly by the Speech framework.

Use Apple's Speech framework to transcribe the finalized file. Require on-device recognition. If the requested locale does not support on-device recognition, preserve the audio and frames and report transcription as unavailable; do not silently upload audio or fall back to a cloud service.

Keep the implementation in the existing package:

- `audio.go` owns capture-session audio state and artifact metadata.
- `mac_audio.go` and `mac_audio_stub.go` provide the darwin implementation and explicit unsupported behavior.
- `transcript.go`, `mac_transcript.go`, and `mac_transcript_stub.go` own transcript schemas and platform behavior.
- `main.go` wires CLI flags, lifecycle, manifest output, and overwrite cleanup.
- `gui.go` wires recording lifecycle, status, review data, and save behavior.
- `frontend/index.html`, `frontend/main.js`, and `frontend/style.css` add setup, recording, transcription, review, and error states without changing the existing visual system.
- `Makefile` adds `NSMicrophoneUsageDescription` and `NSSpeechRecognitionUsageDescription` to the app bundle.

Do not add a Go dependency, shell out to `ffmpeg`, or change the still-image capture primitives. AVFoundation and Speech are system frameworks linked into the existing single binary.

### Artifacts and manifest

An audio-enabled capture directory contains:

```text
capture/
  frame_0000.png
  frame_0001.png
  audio.wav
  transcript.txt
  transcript.json
  manifest.json
```

`transcript.txt` contains only readable transcript text. `transcript.json` is versioned and contains the recognition locale, full text, completion status, and ordered segments with `start_seconds`, `end_seconds`, `text`, and confidence when the platform supplies it.

Bump the capture manifest to version 2. Each frame file entry gains an optional `capture_offset_seconds` and `audio_offset_seconds`. The manifest gains optional `audio` and `transcript` objects containing artifact paths, formats, durations, locale, and completion status. The manifest's aggregate `partial` flag distinguishes usable incomplete captures from complete sessions. Do not put full transcript text in the compact `--json` stdout summary; expose only artifact paths and status there.

The capture directory is the unit of association. GUI `SaveSelected` must copy `audio.wav`, both transcript files, and a rewritten manifest alongside the selected frames. The complete audio and transcript are retained even when the selected frames are sparse; the selected manifest identifies the original frame offsets so consumers can correlate them.

### Failure behavior

- If microphone access is denied, unavailable, or cannot start, fail before the first frame is captured.
- If microphone capture fails after recording starts, stop the session, preserve all finalized partial artifacts, mark audio `partial` or `failed`, and surface a non-success result.
- If screen capture fails fatally, stop and finalize audio before returning the screen error.
- Per-frame off-Space failures remain non-fatal and do not interrupt audio.
- If transcription is unavailable or fails, keep the completed audio and frames, write the failure status and diagnostic to the manifest, and make the capture result partial rather than deleting usable artifacts.
- Post-capture transcription is bounded by the Speech-framework timeout. A timeout or recognition failure preserves audio and frames and records the transcript as failed.

The CLI exits non-zero for a requested microphone failure. A transcription-only failure is reported as partial in JSON and with a warning in human output after all durable artifacts have been written. The GUI proceeds to review and clearly identifies the missing or partial transcript.

### Privacy and security

- Microphone and transcription are off by default and require explicit selection for every capture.
- The GUI displays microphone activity for the entire time the recorder is active.
- On-device recognition is required; no audio or transcript is sent by screengrab over the network.
- Microphone-enabled session directories are created with owner-only permissions, and their audio, transcript, and manifest files are owner-readable and owner-writable.
- Do not persist device identifiers, permission state, or transcript contents in logs. The clipboard continues to receive only an output path.
- Partial files are retained intentionally for recovery and are never uploaded or automatically removed.

## Validation required before acceptance

Implementation starts by replacing the audio non-goal in `contract.md` with binary criteria covering:

- silent capture remains the default and produces no audio or transcript files;
- `--microphone --transcript` produces non-empty `audio.wav`, `transcript.txt`, `transcript.json`, and manifest associations;
- manifest frame offsets and transcript segment offsets are ordered and within audio duration;
- permission denial fails before frame capture and leaves no misleading successful manifest;
- SIGINT and GUI stop finalize audio and preserve partial output;
- transcription failure preserves audio and frames and is visible in manifest, CLI output, and GUI review;
- `SaveSelected` retains audio, transcript, selected frame offsets, and a rewritten manifest;
- non-darwin audio and transcription stubs return explicit unsupported errors instead of silently degrading to screen-only output.

Unit tests cover flag validation, state transitions, manifest version 2, ordered offsets, failure finalization, overwrite cleanup for the new artifacts, and selected-output copying. A macOS integration check records a spoken phrase while a visible event occurs, verifies that it appears in the transcript, and confirms that the corresponding frame offset falls within one frame interval of the transcript segment.

The existing full build, CLI, frames, spritesheet, JSON manifest, SIGINT, GUI-binding, and contract verification commands must continue to pass.

## Consequences

Captures can carry spoken context in a form that humans and agents can consume directly while preserving the project's low-FPS still-image model. Timed metadata makes the relationship explicit even when frame capture has gaps.

Audio-enabled runs require two additional macOS permissions, create sensitive artifacts, and add a post-stop transcription phase. On-device-only transcription trades broader locale and machine support for a clear privacy boundary. The native bridges add platform-specific code, but the binary keeps no runtime dependency on a media tool, speech model, or external service.

## Rejected alternatives

- Changing the current ScreenCaptureKit `capturesAudio` setting is insufficient: the existing screenshot path returns individual images and has no continuous audio output. Replacing it with a stream solely for microphone capture would unnecessarily disrupt the frame path.
- Muxing frames and audio into a video conflicts with the product's frame-first output and adds encoding complexity consumers do not need.
- Cloud transcription would require credentials, network behavior, retention policy, and user consent outside the current product boundary.
- Saving only a transcript would remove the source artifact needed to audit recognition errors or regenerate the transcript.
- Inferring timestamps from frame number and FPS would misalign recordings whenever frames are skipped or capture stalls.
