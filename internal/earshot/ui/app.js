"use strict";

const form = document.querySelector("#transcription");
const audio = document.querySelector("#audio");
const token = document.querySelector("#token");
const button = document.querySelector("#submit");
const record = document.querySelector("#record");
const recordHelp = document.querySelector("#record-help");
const preview = document.querySelector("#preview");
const maxBytes = Number(form.dataset.maxBytes);
const maxSeconds = Number(form.dataset.maxSeconds);
const canRecord = window.isSecureContext &&
  typeof navigator.mediaDevices?.getUserMedia === "function" &&
  typeof window.MediaRecorder === "function";
let active = null;
let recordedFile = null;
let previewURL = null;
let awaitingMic = false;
let uploading = false;
let captureRequest = 0;
const status = document.querySelector("#status");
const result = document.querySelector("#result");
const transcript = document.querySelector("#text");
const details = document.querySelector("#details");
const endpoint = new URL("../v1/transcribe", window.location.href);
const languages = typeof Intl.DisplayNames === "function" ?
  new Intl.DisplayNames([navigator.language], {type: "language"}) : null;

function showStatus(message, error = false) {
  status.textContent = message;
  status.hidden = !message;
  status.toggleAttribute("data-error", error);
}

function updateControls() {
  audio.disabled = uploading || awaitingMic || Boolean(active);
  record.disabled = uploading || awaitingMic || Boolean(active?.stopping);
  record.textContent = awaitingMic ? "Waiting for microphone…" :
    active ? "Stop recording" : "Record audio";
  button.disabled = uploading || awaitingMic || Boolean(active) ||
    !(recordedFile || audio.files[0]);
}

function clearRecording() {
  recordedFile = null;
  if (previewURL) {
    URL.revokeObjectURL(previewURL);
    previewURL = null;
  }
  preview.hidden = true;
  preview.removeAttribute("src");
  preview.load();
}

function stopTracks(stream) {
  stream.getTracks().forEach((track) => track.stop());
}

function finishCapture(capture) {
  clearTimeout(capture.timer);
  stopTracks(capture.stream);
  if (active !== capture) return;
  active = null;
  if (capture.error) {
    showStatus(capture.error, true);
  } else if (!capture.bytes) {
    showStatus("No audio was recorded. Try making a longer recording.", true);
  } else {
    const mime = capture.chunks[0].type || capture.recorder.mimeType;
    const extensions = {"audio/webm": "webm", "audio/ogg": "ogg", "audio/mp4": "m4a"};
    const extension = extensions[mime.split(";")[0]] || "audio";
    recordedFile = new File(capture.chunks, "recording." + extension, {type: mime});
    audio.value = "";
    previewURL = URL.createObjectURL(recordedFile);
    preview.src = previewURL;
    preview.hidden = false;
    showStatus("Recording ready. Play it back or select Transcribe.");
  }
  updateControls();
}

function stopCapture(capture) {
  if (active !== capture || capture.stopping) return;
  capture.stopping = true;
  if (capture.recorder.state !== "inactive") capture.recorder.stop();
  updateControls();
}

function createRecorder(stream) {
  const candidates = ["audio/webm;codecs=opus", "audio/mp4", "audio/ogg;codecs=opus"];
  for (const mimeType of candidates) {
    if (!MediaRecorder.isTypeSupported?.(mimeType)) continue;
    try {
      return new MediaRecorder(stream, {mimeType});
    } catch {
      // Try the next supported container or the browser's default.
    }
  }
  return new MediaRecorder(stream);
}

record.hidden = !canRecord;
recordHelp.hidden = canRecord;
recordHelp.textContent = window.isSecureContext ?
  "Recording is unavailable in this browser. You can still upload a file." :
  "To record from your microphone, open this page over HTTPS.";

record.addEventListener("click", async () => {
  if (active) {
    stopCapture(active);
    return;
  }
  if (!canRecord || awaitingMic || uploading) return;
  const request = ++captureRequest;
  let stream;
  awaitingMic = true;
  updateControls();
  showStatus("Allow microphone access to start recording.");
  try {
    stream = await navigator.mediaDevices.getUserMedia({audio: true});
    if (request !== captureRequest) {
      stopTracks(stream);
      return;
    }
    const recorder = createRecorder(stream);
    const capture = {recorder, stream, chunks: [], bytes: 0};
    recorder.addEventListener("dataavailable", ({data}) => {
      if (!data.size || capture.error) return;
      capture.bytes += data.size;
      if (capture.bytes > maxBytes) {
        capture.error = "Recording exceeded the upload size limit. Try a shorter clip.";
        stopCapture(capture);
      } else {
        capture.chunks.push(data);
      }
    });
    recorder.addEventListener("stop", () => finishCapture(capture));
    recorder.addEventListener("error", () => {
      capture.error = "The browser couldn’t finish recording. Try again.";
      finishCapture(capture);
    });
    recorder.start(1000);
    clearRecording();
    active = capture;
    capture.timer = window.setTimeout(() => stopCapture(capture),
      Math.max(250, maxSeconds * 1000 - 300));
    showStatus("Recording… Select Stop recording when you’re done.");
  } catch (error) {
    if (stream) stopTracks(stream);
    if (request === captureRequest) {
      showStatus(error.name === "NotAllowedError" ?
        "Microphone access was blocked. Allow it in your browser’s site settings or upload a file." :
        "Couldn’t start recording. Check your microphone or upload a file.", true);
    }
  } finally {
    if (request === captureRequest) {
      awaitingMic = false;
      updateControls();
    }
  }
});

audio.addEventListener("change", () => {
  if (audio.files[0]) clearRecording();
  showStatus("");
  updateControls();
});

window.addEventListener("pagehide", () => {
  captureRequest++;
  awaitingMic = false;
  if (active) {
    const capture = active;
    active = null;
    clearTimeout(capture.timer);
    stopTracks(capture.stream);
  }
  clearRecording();
  updateControls();
});

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const clip = recordedFile || audio.files[0];
  if (!clip || button.disabled) return;

  const data = new FormData(form);
  data.set("file", clip);
  if (!data.get("language")) data.delete("language");
  const headers = {};
  if (token?.value.trim()) headers.Authorization = "Bearer " + token.value.trim();

  uploading = true;
  updateControls();
  button.textContent = "Transcribing…";
  result.hidden = true;
  transcript.value = "";
  showStatus("Uploading " + clip.name + " and waiting for the transcript…");

  try {
    const response = await fetch(endpoint, {
      method: "POST",
      body: data,
      headers,
      cache: "no-store",
      redirect: "error",
    });
    const payload = await response.json().catch(() => null);
    if (!response.ok) {
      const messages = {
        unauthorized: "The access token is missing or invalid.",
        too_large: "That clip exceeds the upload size or duration limit.",
        no_speech: "No speech was detected in that clip.",
        unsupported_media: "Earshot couldn’t decode that audio file.",
        busy: "Earshot is busy. Please try again shortly.",
      };
      showStatus(messages[payload?.error?.code] ??
        "Transcription failed (HTTP " + response.status + ").", true);
      return;
    }
    if (typeof payload?.text !== "string") {
      showStatus("Earshot returned an invalid transcript response.", true);
      return;
    }
    transcript.value = payload.text;
    details.textContent = (languages?.of(payload.language) ?? payload.language) + " · " +
      payload.duration_s.toFixed(1) + " s of audio";
    result.hidden = false;
    showStatus("Transcription complete.");
  } catch {
    showStatus("Couldn’t reach Earshot. Check your connection and try again.", true);
  } finally {
    uploading = false;
    updateControls();
    button.textContent = "Transcribe";
  }
});
