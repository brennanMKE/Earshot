# Earshot HTTP contract

## POST /v1/transcribe

Send `Authorization: Bearer <token>` and a `multipart/form-data` body.

| Field | Required | Meaning |
| --- | --- | --- |
| `file` | Yes | Nonempty audio. Earshot reads the part's Content-Type when present and sniffs the bytes otherwise. |
| `language` | No | Lowercase ISO 639-1 code supported by the engine. Omit it to request automatic detection. |
| `prompt` | No | UTF-8 context with up to 200 Unicode code points. |

Each recognized field may occur at most once. The supported audio formats are
AAC/MP4 (M4A), Opus/WebM, Ogg, MP3, and WAV. The default limits are 25 MiB of
audio and 600 seconds of decoded audio. The full multipart body has an
additional 1 MiB allowance for fields and framing.

A successful response is `200 OK` with JSON.

```json
{
  "text": "안녕하세요.",
  "language": "ko",
  "language_probability": null,
  "duration_s": 1.8,
  "segments": [
    {"start": 0.0, "end": 1.8, "text": "안녕하세요."}
  ],
  "model": "large-v3-turbo",
  "engine": "mlx-whisper 0.4.3",
  "elapsed_ms": 1542
}
```

`language_probability` is a number from 0 to 1 when available and `null`
otherwise. Clients must accept `null` for MLX Whisper. Segment times and
`duration_s` are in seconds. `elapsed_ms` is server processing time from
request validation through completion of inference.

Errors have the shape `{"error":{"code":"bad_request","message":"..."}}`.
Use the code for program logic.

| Status | Code | When |
| --- | --- | --- |
| 400 | `bad_request` | Invalid multipart body, missing or empty file, or invalid fields. |
| 401 | `unauthorized` | Missing or invalid bearer token. |
| 413 | `too_large` | Request size or decoded duration exceeds the configured limit. |
| 415 | `unsupported_media` | Audio decoding fails. |
| 422 | `no_speech` | No speech was detected. |
| 503 | `busy` | The queue is full. The response includes `Retry-After: 1`. |
| 503 | `unavailable` | The transcription engine is unavailable. |

Clients can fail over to the next instance on a 503. By default one request
runs at a time and four can wait. Earshot reserves a slot for each authorized
request before reading its upload. Admitted requests take their turns in
arrival order. `elapsed_ms` includes any time spent waiting for a turn.

## Other endpoints

- `GET /healthz` returns `ok <version>` followed by a newline as plain text.
  It requires no auth.

### GET /v1/info

Requires bearer auth. Returns JSON with the configured `model`, the loaded
`engine` and version, its supported request language codes in `languages`, and
the configured audio limits `max_bytes` and `max_seconds`. `running` indicates
whether the active slot is occupied. `queue_depth` and `queue_limit` count
waiting requests and exclude the active slot.

### GET /stats

Requires bearer auth. Returns JSON counters for completed `POST /v1/transcribe`
requests since startup. `requests` counts all outcomes, `successes` counts
successful responses, and `errors` maps error codes to counts.

`elapsed_ms` and `real_time_factor` each contain `p50` and `p95` based on the
most recent 1,024 successful transcriptions with positive audio duration.
`samples` is the number currently available. Percentiles are `null` when
there are no samples. Real-time factor is server elapsed seconds divided by
audio duration in seconds. The counts and samples reset when Earshot restarts.

## Local development

Start the server with `go run ./cmd/earshot serve`. To exercise the HTTP
handler with a fixed response, add `--fake-engine`. The server binds to
`127.0.0.1:8090` by default. Set `EARSHOT_TOKENS_FILE` to a file with
one `<64-character SHA-256 hex hash> <label>` entry per line. Empty lines and
lines starting with `#` are allowed. For a local test without a token file,
add `--insecure-no-auth`.

```sh
curl --fail-with-body -H "Authorization: Bearer $EARSHOT_TOKEN" \
  -F 'file=@testdata/ko.m4a;type=audio/mp4' \
  -F 'language=ko' \
  http://127.0.0.1:8090/v1/transcribe
```
