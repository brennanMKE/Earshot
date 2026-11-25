#!/usr/bin/env python3
"""Compare warm transcription with the same 16 kHz mono WAV inputs."""
import argparse
import json
import re
import statistics
import subprocess
import time
import unicodedata
import wave
from pathlib import Path


def normalize(text):
    return "".join(
        char for char in unicodedata.normalize("NFC", text).casefold()
        if char.isalnum()
    )


def character_error_rate(reference, text):
    reference, text = normalize(reference), normalize(text)
    row = list(range(len(text) + 1))
    for i, expected in enumerate(reference, 1):
        next_row = [i]
        for j, actual in enumerate(text, 1):
            next_row.append(min(row[j] + 1, next_row[-1] + 1,
                                row[j - 1] + (expected != actual)))
        row = next_row
    return row[-1] / len(reference)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", choices=["cpp", "mlx"])
    parser.add_argument("fixtures", type=Path)
    parser.add_argument("--url", default="http://127.0.0.1:18090/inference")
    parser.add_argument("--mlx-model", default="mlx-community/whisper-large-v3-turbo")
    args = parser.parse_args()
    fixtures = sorted(args.fixtures.glob("*.wav"))
    if not fixtures:
        parser.error(f"no WAV fixtures found in {args.fixtures}")

    if args.engine == "mlx":
        import mlx_whisper

        def transcribe(path):
            return mlx_whisper.transcribe(str(path), path_or_hf_repo=args.mlx_model)
    else:
        def transcribe(path):
            return json.loads(subprocess.check_output([
                "curl", "--fail-with-body", "--silent", "--show-error", "--max-time", "180",
                "-F", f"file=@{path}", "-F", "language=auto",
                "-F", "response_format=verbose_json", args.url,
            ]))

    for path in fixtures:
        reference = path.with_suffix(".txt").read_text(encoding="utf-8")
        with wave.open(str(path)) as audio:
            duration = audio.getnframes() / audio.getframerate()
        transcribe(path)
        samples = []
        for _ in range(3):
            start = time.monotonic()
            result = transcribe(path)
            samples.append(time.monotonic() - start)
        median = statistics.median(samples)
        print(json.dumps({
            "fixture": path.stem, "duration_s": round(duration, 3),
            "samples_s": [round(value, 3) for value in samples],
            "median_s": round(median, 3), "rtf": round(median / duration, 3),
            "language": result.get("language"),
            "cer": round(character_error_rate(reference, result["text"]), 3),
            "text": result["text"],
        }, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
