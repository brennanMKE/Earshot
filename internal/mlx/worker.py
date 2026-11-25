"""Single-process MLX Whisper worker. Standard output carries responses only."""

import io
import json
import sys
import wave
from importlib.metadata import version

import mlx.core as mx
import mlx_whisper
import numpy as np
from mlx_whisper.transcribe import ModelHolder
from mlx_whisper.tokenizer import LANGUAGES

SAMPLE_RATE = 16000
SILENCE_PEAK = 32
NO_SPEECH_THRESHOLD = 0.6
model = sys.argv[1]
engine = "mlx-whisper " + version("mlx-whisper")
ModelHolder.get_model(model, mx.float16)
print(json.dumps({"ready": True, "engine": engine, "languages": sorted(LANGUAGES)}), flush=True)

incoming = sys.stdin.buffer
for line in incoming:
    request = json.loads(line)
    wav = incoming.read(request["size"])
    if len(wav) != request["size"]:
        break
    language = request.get("language")
    if language and language not in LANGUAGES:
        print(json.dumps({"error": "bad_language"}), flush=True)
        continue
    try:
        with wave.open(io.BytesIO(wav), "rb") as normalized:
            # Piped WAV carries a placeholder length. Count received samples.
            pcm = normalized.readframes(normalized.getnframes())
        samples = np.frombuffer(pcm, dtype="<i2")
        duration = len(samples) / SAMPLE_RATE
        if duration > request["max_seconds"]:
            print(json.dumps({"error": "too_large"}), flush=True)
            continue
        if samples.max(initial=0) < SILENCE_PEAK and samples.min(initial=0) > -SILENCE_PEAK:
            print(json.dumps({"error": "no_speech"}), flush=True)
            continue
        audio = samples.astype(np.float32) / 32768.0
        result = mlx_whisper.transcribe(
            audio,
            path_or_hf_repo=model,
            language=language or None,
            initial_prompt=request.get("prompt") or None,
            condition_on_previous_text=False,
            verbose=None,
        )
        segments = [
            {"start": segment["start"], "end": segment["end"], "text": segment["text"]}
            for segment in result["segments"]
            if segment["text"].strip() and segment["no_speech_prob"] < NO_SPEECH_THRESHOLD
        ]
        if segments:
            reply = {
                "text": "".join(segment["text"] for segment in segments).strip(),
                "language": result["language"],
                "duration_s": duration,
                "segments": segments,
                "engine": engine,
            }
        else:
            reply = {"error": "no_speech"}
        print(json.dumps(reply, allow_nan=False), flush=True)
    except Exception:
        print(json.dumps({"error": "unavailable"}), flush=True)
