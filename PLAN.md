# Earshot: plan

Earshot is a local speech-to-text server for Apple Silicon Macs. It takes an audio
clip and returns its transcript using Whisper. Agora can call multiple instances
over a private network for failover.

## Why

Agora users can record voice messages for translation between Korean and English.
Agora sends uploaded audio to Earshot, stores the transcript with the message, and
shows it below the audio. The translation agent receives the transcript as text.
Agora's integration is separate work. Earshot provides the HTTP contract below.

## Goals

- Transcribe **Korean and English** well. Korean is the priority.
- Accept what browsers record:
  - `audio/mp4` / `.m4a` (Safari, iOS);
  - `audio/webm` with Opus (Chrome, Firefox);
  - `audio/ogg`, `audio/mpeg`, `audio/wav`.
- Return a 30-second clip in a few seconds on an M1 mini.
- Keep the model loaded between requests; don't reload it for every clip.
- Run alongside existing workloads without exhausting host resources.
- Keep audio private:
  - nothing is written to disk except temporary files, which are deleted when the
    request ends;
  - transcripts are never logged.

## Non-goals (for now)

- Streaming or live transcription. Clips are whole files.
- Text-to-speech.
- Translation. Agora delegates that to its translation agent.
- Diarization (who is speaking).
- Agora's UI, upload flow and storage.

## Engine

**Decide in phase 0, by measuring.** Candidates:

1. **whisper.cpp** (`brew install whisper-cpp`), using Metal.
   - It has its own `whisper-server`, which keeps the model loaded.
   - Earshot could be a thin Go service in front of it. Earshot would handle auth,
     limits, format conversion with ffmpeg, our JSON contract and stats, and would
     proxy the transcription to `whisper-server` on 127.0.0.1.
2. **mlx-whisper** (Python, MLX). It is often faster on Apple Silicon, but it
   brings a Python runtime to deploy and keep running.

Models to compare:
- `large-v3-turbo` (about 1.6 GB);
- `large-v3`, if turbo's Korean isn't good enough.

Prefer the leading candidate, whisper.cpp with `large-v3-turbo`, unless phase 0
shows it is clearly worse for Korean or too slow on an M1.

The Go service should build with `CGO_ENABLED=0` and talk to the
engine over HTTP or by running it as a process, not through cgo bindings.

## HTTP contract

Agora depends on a stable HTTP contract. See [CONTRACT.md](CONTRACT.md) for the
client-facing specification.

### `POST /v1/transcribe`

- **Auth:** `Authorization: Bearer <token>`.
- **Body:** `multipart/form-data` with these fields:

| Field | Required | Meaning |
|---|---|---|
| `file` | yes | The audio. Its type comes from the part's `Content-Type`; if that is missing, from sniffing the bytes. |
| `language` | no | An ISO 639-1 code, such as `ko` or `en`. If it is left out, the language is detected. |
| `prompt` | no | Up to 200 characters of context that improve accuracy, such as names or domain terms. |

A successful response is `200` with this JSON:

```json
{
  "text": "안녕하세요, 오늘 날씨가 좋네요.",
  "language": "ko",
  "language_probability": null,
  "duration_s": 4.2,
  "segments": [
    { "start": 0.0, "end": 4.2, "text": "안녕하세요, 오늘 날씨가 좋네요." }
  ],
  "model": "large-v3-turbo",
  "engine": "whisper.cpp 1.x",
  "elapsed_ms": 1830
}
```

An error is JSON shaped as `{"error": {"code": "<code>", "message": "<text>"}}`:

| Status | Code | When |
|---|---|---|
| 400 | `bad_request` | No `file`, or a bad `language`. |
| 401 | `unauthorized` | The token is missing or wrong. |
| 413 | `too_large` | The file is over 25 MiB, or the audio is over 10 minutes. Both limits can be configured. |
| 415 | `unsupported_media` | ffmpeg can't decode the file. |
| 422 | `no_speech` | The clip is silent or has no speech. Agora shows "no speech" rather than an empty transcript. |
| 503 | `busy` | The queue is full; a `Retry-After` header says when to try again. Agora then tries the next server. |
| 503 | `unavailable` | The engine isn't loaded or has crashed. |

### Other endpoints

- `GET /healthz` returns `ok <version>` as `text/plain`, with no auth needed, the
  same way Agora's own `/healthz` works.
- `GET /v1/info` (needs auth) returns:
  - the model and engine;
  - the languages it supports;
  - the maximum size and duration;
  - how busy it is: the job running and the queue depth.
- `GET /stats` (needs auth) returns request counts, error counts by code, and the
  p50 and p95 of `elapsed_ms` and of the real-time factor.

### Concurrency

- Run **one transcription at a time** on each mini, with a short queue of 4 by
  default.
- When the queue is full, answer `503 busy` right away.
- The client is responsible for failing over to the next server.

## Configuration

Set through flags or environment variables:

| Variable | Default | |
|---|---|---|
| `EARSHOT_ADDR` | `127.0.0.1:8090` | Bind to localhost only. A reverse proxy provides private network access. |
| `EARSHOT_TOKENS_FILE` | — | Sha256 hashes of the accepted bearer tokens, one per line, each with a label (`agora`, …). The server refuses to start without this file unless `--insecure-no-auth` is passed. |
| `EARSHOT_MODEL` | `large-v3-turbo` | |
| `EARSHOT_MODEL_DIR` | `~/.cache/earshot/models` | Downloaded at first start or with `earshot fetch-model`, checked against a pinned sha256. |
| `EARSHOT_MAX_BYTES` | `26214400` | |
| `EARSHOT_MAX_SECONDS` | `600` | |
| `EARSHOT_QUEUE` | `4` | |
| `EARSHOT_FFMPEG` | `ffmpeg` on `PATH` | |
| `EARSHOT_PYTHON` | `python3` on `PATH` | Python environment with mlx-whisper installed. |

## Deployment

Expose Earshot through a private-network reverse proxy. Route
`/earshot/v1/transcribe` to `http://127.0.0.1:8090/v1/transcribe`.
Keep instance addresses, access details, and rollout order in the Git-ignored
`LOCAL.md`.

Deliverables:

- A LaunchAgent plist under `deploy/` with `KeepAlive` and logs in
  `~/Library/Logs/earshot/`.
- `deploy/install.sh` to build or install the binary, fetch the model, install the
  LaunchAgent, and print the reverse-proxy route to add.
- `docs/deploy.md` with a generic proxy route, instructions for provisioning an
  Agora token, and a `curl` check from an authorized client.

## Tests

- **Fixtures with no recordings of real people.** Generate Korean speech with
  macOS's own voices: `say -v Yuna "…" -o ko.aiff`, then ffmpeg converts it to
  `.m4a`, `.webm` (Opus), `.mp3` and `.wav`. Make English fixtures with `say -v
  Samantha`. Commit the script that generates them (`testdata/make-fixtures.sh`)
  and the small resulting clips.
- **Unit tests** that don't need the model:
  - contract shapes and error codes;
  - auth;
  - size and duration limits;
  - queue full → 503;
  - temporary files deleted after a request;
  - transcript text never appears in the logs.

  Use a fake engine for these.
- **Integration tests**, behind a build tag or an environment variable, with the real
  model:
  - each fixture format transcribes;
  - the Korean text contains the expected key words. Use a loose match or a
    character error rate under a threshold, not exact equality;
  - `language` is detected as `ko` or `en`.
- **Benchmark** (`make bench`): the real-time factor and memory for 10 s, 30 s and
  2 min clips. Record the results by Apple Silicon chip in `docs/benchmarks.md`.

## Phases

0. **Spike (half a day).**
   - Install whisper.cpp and mlx-whisper on the benchmark host.
   - Run the Korean fixtures through both engines with `large-v3-turbo`.
   - Record accuracy, speed and memory with the existing workload running alongside.
   - Choose the engine, and write the choice and the numbers in
     `docs/decisions.md`.
1. **Server skeleton.**
   - Go module with `cmd/earshot` and the
     `serve` and `fetch-model` subcommands.
   - `/healthz`, auth and config.
   - The contract running against a fake engine, with unit tests.
2. **Engine.**
   - Wire in the chosen engine.
   - Decode input with ffmpeg to 16 kHz mono WAV.
   - Detect duration and no-speech.
   - Add the integration tests.
3. **Operations.** The queue and 503 busy, `/v1/info`, `/stats`, structured logs
   (no transcripts) and graceful shutdown.
4. **Deploy.** Install the LaunchAgent and configure the reverse proxy.
   Verify each instance from an authorized client with `curl`.
5. **Hand off to Agora.** Write `CONTRACT.md` and a short note for the Agora issue:
   the `STT_URL`-style setting and measured speeds. Share instance URLs and the
   bearer token privately.

## Open questions

- Can `large-v3` fit alongside the existing workload on an 8 GiB host?
- Should a `prompt` of room-specific names be passed by default? Agora could send
  the room's member names.
- Should a separate Pi or Linux fallback exist? If all instances are down,
  Agora can post the audio with "transcript unavailable".
