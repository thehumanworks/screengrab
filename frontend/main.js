// screengrab — frontend controller. Uses the Wails v3 runtime to call
// captureService methods on the Go side. We call by name to avoid running
// the binding generator at build time.

import { Call, Events } from "/wails/runtime.js";

const $ = (id) => document.getElementById(id);

const state = {
  sources: [],         // unified list: displays + macOS windows
  sourceId: "",        // canonical id, e.g. "display:0" or "window:0x1A"
  fps: 2,
  output: "screengrab-out",
  region: { x: 0, y: 0, width: 0, height: 0 },
  framePaths: [],
  selected: new Set(),
  microphone: false,
  transcript: false,
  transcriptLocale: "",
  recordingStatus: null,
};

// ─── view switching ──────────────────────────────────────────────────────

function showView(name) {
  for (const el of document.querySelectorAll(".view")) {
    el.hidden = el.dataset.view !== name;
  }
}

function setSubtitle(text) { $("subtitle").textContent = text; }

function toast(msg, ms = 3200) {
  const t = $("toast");
  t.textContent = msg;
  t.hidden = false;
  requestAnimationFrame(() => t.classList.add("show"));
  clearTimeout(toast._h);
  toast._h = setTimeout(() => {
    t.classList.remove("show");
    setTimeout(() => { t.hidden = true; }, 300);
  }, ms);
}

// ─── service helpers ─────────────────────────────────────────────────────
//
// Wails v3 binds methods under the FQN `<go-package-path>.<struct-name>.<method>`.
// For a `package main` binary, reflect.Type.PkgPath() returns the literal
// string "main" at runtime — verified from Wails' own debug log:
//   "Registering bound method: fqn=main.CaptureService.ListDisplays"
// even though the module is called "screengrab". The earlier mistake here
// was to guess "screengrab.CaptureService"; trust the runtime log, not
// intuition. If CaptureService is ever moved into a sub-package (e.g.
// "screengrab/internal/capture"), the prefix becomes that sub-package's
// import path plus the struct name. gui_bindings_test.go is the regression
// net for both directions.

const SVC_FQN = "main.CaptureService";
const svc = (method, ...args) => Call.ByName(`${SVC_FQN}.${method}`, ...args);

// ─── setup view ──────────────────────────────────────────────────────────

async function initSetup() {
  state.sources = await svc("ListSources");
  const sel = $("sourceSel");
  sel.innerHTML = "";

  // Group physical displays and macOS maximized-app windows so the picker
  // makes the distinction visually obvious. The user said "the ones that
  // require a swipe when an application is maximised" — those are window
  // sources, listed under their own optgroup so they don't blur in with
  // physical displays.
  const displays = state.sources.filter((s) => s.kind === "display");
  const windows  = state.sources.filter((s) => s.kind === "window");

  if (displays.length) {
    const g = document.createElement("optgroup");
    g.label = "Displays";
    for (const d of displays) {
      const opt = document.createElement("option");
      opt.value = d.id;
      opt.textContent = `${d.name}`;
      g.appendChild(opt);
    }
    sel.appendChild(g);
  }
  if (windows.length) {
    const liveGroup = document.createElement("optgroup");
    liveGroup.label = "Windowed apps — on the current Space (live)";
    const offGroup = document.createElement("optgroup");
    offGroup.label = "Windowed apps — on another Space (swipe to capture)";
    for (const w of windows) {
      const opt = document.createElement("option");
      opt.value = w.id;
      const dims = `${w.width}×${w.height}`;
      const tag = w.on_screen ? "" : "  ⤴ swipe over";
      opt.textContent = `${w.name}  (${dims})${tag}`;
      (w.on_screen ? liveGroup : offGroup).appendChild(opt);
    }
    if (liveGroup.children.length) sel.appendChild(liveGroup);
    if (offGroup.children.length) sel.appendChild(offGroup);
  }

  if (!state.sourceId && state.sources.length) state.sourceId = state.sources[0].id;
  sel.value = state.sourceId;
  $("fpsInput").value = state.fps;
  $("outputInput").value = state.output;
	$("microphoneInput").checked = state.microphone;
	$("transcriptInput").checked = state.transcript;
	$("transcriptLocaleInput").value = state.transcriptLocale;
	$("transcriptLocaleInput").disabled = !state.transcript;
  renderRegionSummary();
}

function currentSource() {
  return state.sources.find((s) => s.id === state.sourceId);
}

function renderRegionSummary() {
  const el = $("regionSummary");
  if (state.region.width > 0 && state.region.height > 0) {
    el.textContent = `Region (${state.region.x}, ${state.region.y})  ${state.region.width}×${state.region.height}`;
  } else {
    const src = currentSource();
    if (!src) { el.textContent = "Full source"; }
    else if (src.kind === "window") {
      el.textContent = `Full window  ${src.app || ""}${src.title ? " — " + src.title : ""}  (${src.width}×${src.height})`;
    } else {
      el.textContent = `Full ${src.name}`;
    }
  }
  renderWindowHint();
}

// renderWindowHint shows a heads-up below the region summary whenever the
// current source is a window. The user needs to know two things before
// clicking Start: (1) capture only works while the window's macOS Space is
// visible, and (2) we will give them a 3-second countdown to swipe over.
function renderWindowHint() {
  const hint = $("windowHint");
  if (!hint) return;
  const src = currentSource();
  if (!src || src.kind !== "window") {
    hint.hidden = true;
    return;
  }
  hint.hidden = false;
  if (src.on_screen) {
    hint.innerHTML =
      `<strong>Heads up:</strong> when you click Start, screengrab waits 3 seconds before capture begins so you can ` +
      `bring <em>${escapeHTML(src.name)}</em> forward. Capture pauses any time the window's Space is not visible.`;
  } else {
    hint.innerHTML =
      `<strong>Heads up:</strong> <em>${escapeHTML(src.name)}</em> is on another macOS Space. When you click Start, ` +
      `screengrab counts down 3 seconds so you can swipe over (trackpad swipe or <kbd>Ctrl</kbd> <kbd>→</kbd>). ` +
      `Frames only land while that Space is visible.`;
  }
}

function commitSetupForm() {
  state.sourceId = $("sourceSel").value || state.sourceId;
  const fpsRaw = parseInt($("fpsInput").value, 10);
  if (!Number.isNaN(fpsRaw) && fpsRaw > 0) state.fps = fpsRaw;
  const out = $("outputInput").value.trim();
  if (out) state.output = out;
	state.microphone = $("microphoneInput").checked;
	state.transcript = $("transcriptInput").checked;
	state.transcriptLocale = $("transcriptLocaleInput").value.trim();
}

// ─── region picker ───────────────────────────────────────────────────────

let regionScale = 1;
let regionDisplaySize = { width: 0, height: 0 };

async function showRegionPicker() {
  commitSetupForm();
  showView("region");
  setSubtitle("region picker");
  $("regionRect").hidden = true;
  $("regionLive").textContent = "Region: (drag to select)";

  const src = currentSource();
  if (src && src.kind === "window" && !src.on_screen) {
    // An off-Space window does not reliably have a capture-ready frame.
    // Skip the picker's one-shot snapshot; the recording loop can retry.
    toast(
      `“${src.name}” is on another macOS Space. Swipe over to it to preview a region. ` +
      `You can still record the full window — recording will retry until it is available.`,
      6000,
    );
    showView("setup");
    return;
  }

  try {
    const dataURI = await svc("SnapshotSource", state.sourceId);
    const img = $("regionPreview");
    img.src = dataURI;
    await new Promise((resolve, reject) => {
      if (img.complete && img.naturalWidth) resolve();
      else {
        img.onload = () => resolve();
        img.onerror = () => reject(new Error("preview image failed to decode"));
      }
    });
    regionDisplaySize = src
      ? { width: src.width, height: src.height }
      : { width: img.naturalWidth, height: img.naturalHeight };
    regionScale = img.clientWidth / regionDisplaySize.width;
  } catch (e) {
    toast(`Snapshot failed: ${e?.message ?? e}`);
    showView("setup");
  }
}

(function wireRegionDrag() {
  const stage = $("regionDrag");
  const rect = $("regionRect");
  let dragging = false;
  let startX = 0, startY = 0;

  stage.addEventListener("pointerdown", (e) => {
    stage.setPointerCapture(e.pointerId);
    dragging = true;
    const b = stage.getBoundingClientRect();
    startX = e.clientX - b.left;
    startY = e.clientY - b.top;
    rect.style.left = startX + "px";
    rect.style.top = startY + "px";
    rect.style.width = "0px";
    rect.style.height = "0px";
    rect.hidden = false;
  });

  stage.addEventListener("pointermove", (e) => {
    if (!dragging) return;
    const b = stage.getBoundingClientRect();
    const cx = Math.max(0, Math.min(e.clientX - b.left, b.width));
    const cy = Math.max(0, Math.min(e.clientY - b.top, b.height));
    const x1 = Math.min(cx, startX);
    const y1 = Math.min(cy, startY);
    const w = Math.abs(cx - startX);
    const h = Math.abs(cy - startY);
    rect.style.left = x1 + "px";
    rect.style.top = y1 + "px";
    rect.style.width = w + "px";
    rect.style.height = h + "px";

    // map preview coords back to display coords
    const dx = Math.round(x1 / regionScale);
    const dy = Math.round(y1 / regionScale);
    const dw = Math.round(w / regionScale);
    const dh = Math.round(h / regionScale);
    state.region = { x: dx, y: dy, width: dw, height: dh };
    $("regionLive").textContent = `Region: (${dx}, ${dy})  ${dw}×${dh}`;
  });

  stage.addEventListener("pointerup", () => { dragging = false; });
  stage.addEventListener("pointercancel", () => { dragging = false; });
})();

// ─── recording ───────────────────────────────────────────────────────────

let recordingTimerHandle = null;
let countdownAbort = null;

// runCountdown shows the "switch to the target window's Space" gate before
// recording starts. We only do this for window sources — for displays the
// user is already looking at what they want to capture, so a delay would
// be friction. The function returns true on completion, false if the user
// cancelled, so the caller can bail out of the recording flow.
async function runCountdown(src, seconds = 3) {
  showView("countdown");
  setSubtitle("get ready");

  $("countdownTarget").textContent = `Target: ${src.name}  (${src.width}×${src.height})`;
  const help = $("countdownHelp");
  if (src.on_screen) {
    help.innerHTML =
      `Bring <strong>${escapeHTML(src.name)}</strong> forward before the timer hits zero — ` +
      `screengrab can only capture pixels while the window is being rendered.`;
  } else {
    help.innerHTML =
      `<strong>${escapeHTML(src.name)}</strong> is on another macOS Space. ` +
      `Swipe over to it (or press <kbd>Ctrl</kbd> <kbd>→</kbd>) before the timer hits zero — ` +
      `screengrab can only capture pixels while that Space is visible.`;
  }

  countdownAbort = { cancelled: false };
  const myAbort = countdownAbort;

  for (let n = seconds; n > 0; n--) {
    const el = $("countdownBig");
    // Re-trigger the pop animation each tick.
    el.textContent = String(n);
    el.style.animation = "none";
    void el.offsetWidth;
    el.style.animation = "";

    await new Promise((resolve) => setTimeout(resolve, 1000));
    if (myAbort.cancelled) return false;
  }
  $("countdownBig").textContent = "Recording";
  return true;
}

function escapeHTML(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

async function startRecording() {
  commitSetupForm();
  state.framePaths = [];
  state.selected.clear();
  state.recordingStatus = null;

  const src = currentSource();
  if (src && src.kind === "window") {
    const ok = await runCountdown(src, 3);
    if (!ok) {
      // User pressed Cancel mid-countdown — back to setup.
      showView("setup");
      setSubtitle("capture for vision-capable LLMs");
      return;
    }
  }

  showView("recording");
  setSubtitle("recording");
  $("recElapsed").textContent = "0.0s";
  $("recFrames").textContent = "0";
	$("recAudio").textContent = state.microphone ? "On" : "Off";
	$("recordingSub").textContent = "Press Stop when you are done; partial output is preserved.";

  try {
    await svc("StartRecording", {
      source_id: state.sourceId,
      fps: state.fps,
      x: state.region.x,
      y: state.region.y,
      width: state.region.width,
      height: state.region.height,
      output: state.output,
	  microphone: state.microphone,
	  transcript: state.transcript,
	  transcript_locale: state.transcriptLocale,
    });
  } catch (e) {
    toast(`Start failed: ${e?.message ?? e}`);
    showView("setup");
    return;
  }

  const tick = async () => {
    try {
      const s = await svc("RecordingStatus");
      $("recElapsed").textContent = s.elapsed.toFixed(1) + "s";
      $("recFrames").textContent = String(s.frame_count);
      state.framePaths = s.frame_paths || [];
	  state.recordingStatus = s;
	  $("recAudio").textContent = s.microphone ? "On" : "Off";
	  if (s.transcribing) {
		setSubtitle("transcribing");
		$("recordingSub").textContent = "Screen and microphone capture are complete. Creating the on-device transcript…";
	  }
      if (!s.recording) {
        clearInterval(recordingTimerHandle);
        recordingTimerHandle = null;
        await showReview();
      }
    } catch (_) { /* drop, the next tick will retry */ }
  };
  recordingTimerHandle = setInterval(tick, 220);
}

async function stopRecording() {
  await svc("StopRecording");
}

// ─── review ──────────────────────────────────────────────────────────────

async function showReview() {
  showView("review");
  setSubtitle("review");

  const summary = $("reviewSummary");
  summary.textContent = `${state.framePaths.length} frame${state.framePaths.length === 1 ? "" : "s"} captured.`;

	const artifacts = $("reviewArtifacts");
	const status = state.recordingStatus;
	if (status && (status.audio_path || status.transcript_status)) {
	  artifacts.hidden = false;
	  $("reviewAudioPath").textContent = status.audio_path || "not available";
	  $("reviewTranscriptStatus").textContent = status.transcript_status || "not requested";
	  const text = $("reviewTranscriptText");
	  text.hidden = true;
	  text.textContent = "";
	  if (status.transcript_status === "complete") {
		svc("TranscriptText").then((value) => {
		  text.textContent = value;
		  text.hidden = false;
		}).catch(() => {});
	  }
	} else {
	  artifacts.hidden = true;
	}

  const grid = $("reviewGrid");
  grid.innerHTML = "";
  state.selected.clear();

  for (let i = 0; i < state.framePaths.length; i++) {
    const tile = document.createElement("div");
    tile.className = "frame-tile";
    tile.dataset.idx = String(i);

    const label = document.createElement("div");
    label.className = "frame-index";
    label.textContent = String(i + 1).padStart(3, "0");
    tile.appendChild(label);

    const img = document.createElement("img");
    img.alt = `frame ${i + 1}`;
    tile.appendChild(img);

    // load thumbnail asynchronously
    svc("FramePreview", i).then((uri) => { img.src = uri; }).catch(() => {});

    tile.addEventListener("click", () => toggleFrame(i, tile));
    grid.appendChild(tile);
  }
}

function toggleFrame(i, tile) {
  if (state.selected.has(i)) {
    state.selected.delete(i);
    tile.classList.remove("selected");
  } else {
    state.selected.add(i);
    tile.classList.add("selected");
  }
}

async function saveSelected() {
  if (state.selected.size === 0) {
    toast("Pick at least one frame, then try again.");
    return;
  }
  const indices = Array.from(state.selected).sort((a, b) => a - b);
  try {
    const dest = await svc("SaveSelected", indices, state.output);
    const associated = state.recordingStatus?.audio_path ? " with associated audio and transcript artifacts" : "";
    toast(`Saved ${indices.length} frames${associated} to ${dest} — path copied.`);
  } catch (e) {
    toast(`Save failed: ${e?.message ?? e}`);
  }
}

// ─── wire up ────────────────────────────────────────────────────────────

document.addEventListener("DOMContentLoaded", async () => {
  await initSetup();
  showView("setup");

  $("sourceSel").addEventListener("change", () => {
    state.sourceId = $("sourceSel").value;
    state.region = { x: 0, y: 0, width: 0, height: 0 };
    renderRegionSummary();
  });

  $("pickRegionBtn").addEventListener("click", showRegionPicker);
  $("fullDisplayBtn").addEventListener("click", () => {
    commitSetupForm();
    state.region = { x: 0, y: 0, width: 0, height: 0 };
    renderRegionSummary();
    toast("Region reset to full display.");
  });
  $("startBtn").addEventListener("click", startRecording);

	$("microphoneInput").addEventListener("change", () => {
	  if (!$("microphoneInput").checked) {
		$("transcriptInput").checked = false;
		$("transcriptLocaleInput").disabled = true;
	  }
	});
	$("transcriptInput").addEventListener("change", () => {
	  if ($("transcriptInput").checked) $("microphoneInput").checked = true;
	  $("transcriptLocaleInput").disabled = !$("transcriptInput").checked;
	});

  $("regionConfirmBtn").addEventListener("click", () => {
    renderRegionSummary();
    showView("setup");
    setSubtitle("capture for vision-capable LLMs");
  });
  $("regionCancelBtn").addEventListener("click", () => {
    state.region = { x: 0, y: 0, width: 0, height: 0 };
    renderRegionSummary();
    showView("setup");
    setSubtitle("capture for vision-capable LLMs");
  });
  $("regionResetBtn").addEventListener("click", () => {
    state.region = { x: 0, y: 0, width: 0, height: 0 };
    $("regionRect").hidden = true;
    $("regionLive").textContent = "Region: (drag to select)";
  });

  $("stopBtn").addEventListener("click", stopRecording);

  $("countdownCancelBtn").addEventListener("click", () => {
    if (countdownAbort) countdownAbort.cancelled = true;
  });

  $("selectAllBtn").addEventListener("click", () => {
    state.selected = new Set(state.framePaths.map((_, i) => i));
    for (const t of document.querySelectorAll(".frame-tile")) t.classList.add("selected");
  });
  $("clearAllBtn").addEventListener("click", () => {
    state.selected.clear();
    for (const t of document.querySelectorAll(".frame-tile")) t.classList.remove("selected");
  });
  $("saveBtn").addEventListener("click", saveSelected);
  $("discardBtn").addEventListener("click", () => {
    state.framePaths = [];
    state.selected.clear();
    showView("setup");
    setSubtitle("capture for vision-capable LLMs");
  });

  $("quitBtn").addEventListener("click", () => svc("Quit"));

  Events.On("capture:error", (ev) => {
    toast(`Capture error: ${ev.data}`);
  });
  Events.On("capture:frame_error", (ev) => {
    // Throttle these — when the target is on another Space we may emit
    // one of these per frame interval, which would spam the toast.
    const now = Date.now();
    if (!Events._lastFrameErrToast || now - Events._lastFrameErrToast > 3000) {
      Events._lastFrameErrToast = now;
      const src = currentSource();
      const label = src && src.kind === "window" && !src.on_screen
        ? `${src.name} is still on another Space — swipe over to start capturing pixels.`
        : `Frame skipped: ${ev?.data?.error ?? "unknown error"}`;
      toast(label, 2500);
    }
  });
  Events.On("capture:complete", () => { /* status polling handles transition */ });
	Events.On("capture:transcript_error", (ev) => {
	  toast(`Transcript incomplete: ${ev.data}`, 5000);
	});
});
