// The microphone button. A press asks the Bridge whether speech to text
// can run, then records; a second press, or 60 seconds, stops. The page
// turns the recording into a 16 kHz mono WAV file, the Bridge hands it to
// merud's transcribe op, and the words land in the question box for the
// user to read, fix and send. Nothing is sent without the user's press of
// Send, and no recording is kept: the bytes live in this page's memory
// until the call returns. See ARCHITECTURE.md, "Recording in Meru.app".

import { bridge, errorText } from "./api.js";

// MAX_SECONDS is the longest recording, the most merud takes.
const MAX_SECONDS = 60;

// RATE is the sample rate Whisper reads; recording at the microphone's own
// rate and resampling once at the end keeps the recorder simple.
const RATE = 16000;

// The words for each way the microphone can fail, by the error's name as
// getUserMedia gives it.
const MIC_ERRORS = {
  NotFoundError: "This Mac has no microphone. Connect one, such as AirPods or a webcam.",
  NotAllowedError: "Meru can't use the microphone. Turn it on in System Settings, Privacy & Security, Microphone, then press the microphone again.",
  NotReadableError: "Another app holds the microphone. Close it, then press the microphone again.",
};

// setupVoice wires the button to the question box. notice shows one line
// above the composer, as the rest of the page does.
export function setupVoice(button, box, notice) {
  // phase is "idle", "recording" or "transcribing".
  let phase = "idle";
  let recorder = null;
  let stream = null;
  let timer = null;
  let started = 0;

  const show = (next, label) => {
    phase = next;
    button.classList.toggle("recording", next === "recording");
    button.disabled = next === "transcribing";
    button.setAttribute("aria-pressed", next === "recording" ? "true" : "false");
    button.setAttribute("aria-label", label);
    button.title = label;
  };
  show("idle", "Speak a question");

  // release lets go of the microphone, so macOS's indicator goes out.
  const release = () => {
    clearInterval(timer);
    timer = null;
    if (stream) stream.getTracks().forEach((t) => t.stop());
    stream = null;
  };

  const tick = () => {
    const secs = Math.floor((Date.now() - started) / 1000);
    if (secs >= MAX_SECONDS) {
      stop();
      return;
    }
    const left = MAX_SECONDS - secs;
    notice(`Listening… ${secs} s. Press the microphone again to stop${left <= 10 ? `; it stops in ${left} s` : ""}.`);
  };

  const stop = () => {
    if (phase === "recording" && recorder && recorder.state !== "inactive") recorder.stop();
  };

  const start = async () => {
    let state;
    try {
      state = await bridge.speechStatus();
    } catch (err) {
      notice(errorText(err));
      return;
    }
    if (!state.ready) {
      notice(state.sentence);
      return;
    }
    if (!navigator.mediaDevices || !window.MediaRecorder) {
      notice("This window can't record sound.");
      return;
    }
    try {
      // The browser's echo and noise cancelling help Whisper in a room.
      stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
      });
    } catch (err) {
      release();
      notice(MIC_ERRORS[err.name] || "Meru can't use the microphone: " + (err.message || err.name));
      return;
    }
    const chunks = [];
    recorder = new MediaRecorder(stream);
    recorder.addEventListener("dataavailable", (e) => {
      if (e.data.size > 0) chunks.push(e.data);
    });
    recorder.addEventListener("stop", () => finish(chunks, recorder.mimeType));
    recorder.start();
    started = Date.now();
    show("recording", "Stop recording");
    tick();
    timer = setInterval(tick, 500);
  };

  // finish turns the recording into text and puts it in the box.
  const finish = async (chunks, type) => {
    release();
    show("transcribing", "Turning speech into text");
    notice("Turning speech into text…");
    try {
      const blob = new Blob(chunks, { type });
      const wav = await toWAV(await blob.arrayBuffer());
      const text = await bridge.transcribe(toBase64(wav));
      if (!text) {
        notice("Meru heard nothing. Try again closer to the microphone.");
        return;
      }
      insert(box, text);
      notice("Check the words, then press Send.");
    } catch (err) {
      notice(errorText(err));
    } finally {
      recorder = null;
      show("idle", "Speak a question");
    }
  };

  button.addEventListener("click", () => {
    if (phase === "idle") start();
    else if (phase === "recording") stop();
  });
}

// insert puts text in the box after what is there, with a space between,
// moves the cursor to the end and tells the box it changed, so it grows
// to fit.
function insert(box, text) {
  const before = box.value.trimEnd();
  box.value = before ? before + " " + text : text;
  box.focus();
  box.setSelectionRange(box.value.length, box.value.length);
  box.dispatchEvent(new Event("input"));
}

// toWAV decodes a recording in whatever format MediaRecorder made, AAC in
// a WebView on macOS, resamples it to RATE in one channel, and writes a
// 16-bit PCM WAV file. OfflineAudioContext does the mixing and the
// resampling: it renders the sound as fast as it can, not in real time.
async function toWAV(encoded) {
  const ctx = new AudioContext();
  let decoded;
  try {
    decoded = await ctx.decodeAudioData(encoded);
  } finally {
    ctx.close();
  }
  const length = Math.max(1, Math.ceil(decoded.duration * RATE));
  const offline = new OfflineAudioContext(1, length, RATE);
  const source = offline.createBufferSource();
  source.buffer = decoded;
  source.connect(offline.destination);
  source.start();
  const rendered = await offline.startRendering();
  return pcmWAV(rendered.getChannelData(0), RATE);
}

// pcmWAV writes samples, floats from -1 to 1, as a mono 16-bit PCM WAV
// file at rate: a 44-byte RIFF header, then the samples, lowest byte
// first.
function pcmWAV(samples, rate) {
  const data = samples.length * 2;
  const buf = new ArrayBuffer(44 + data);
  const v = new DataView(buf);
  const text = (at, s) => [...s].forEach((c, i) => v.setUint8(at + i, c.charCodeAt(0)));
  text(0, "RIFF");
  v.setUint32(4, 36 + data, true);
  text(8, "WAVE");
  text(12, "fmt ");
  v.setUint32(16, 16, true); // the fmt chunk's size
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // one channel
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true); // bytes per second
  v.setUint16(32, 2, true); // bytes per sample
  v.setUint16(34, 16, true); // bits per sample
  text(36, "data");
  v.setUint32(40, data, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    v.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Uint8Array(buf);
}

// toBase64 encodes bytes for the Bridge call. btoa takes a string of
// bytes, built in slices so a long recording doesn't overflow the call
// stack.
function toBase64(bytes) {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(s);
}
