# Earshot

A local speech-to-text server for Apple Silicon. Earshot uses Whisper to
transcribe audio clips, such as voice messages, for other apps on a private network.

## Run locally

On Apple Silicon, install Go, Python 3.12, and FFmpeg with the
[`fd` protocol](https://ffmpeg.org/ffmpeg-protocols.html#fd) available.

```sh
python3.12 -m venv .venv
.venv/bin/python -m pip install mlx-whisper==0.4.3
export EARSHOT_PYTHON="$PWD/.venv/bin/python"
go run ./cmd/earshot fetch-model
go run ./cmd/earshot serve --insecure-no-auth
```

Open `http://127.0.0.1:8090/ui/` to choose an audio clip or record with your
microphone, then select **Transcribe**. Choose **Korean** to transcribe Korean
speech in Hangul. The last command runs the local demo without a token on the
default loopback address. Add `--fake-engine` to try the UI with a fixed
transcript.
For authenticated use, set `EARSHOT_TOKENS_FILE` and omit
`--insecure-no-auth`. See the [HTTP contract](CONTRACT.md) for token file
format and request examples.

The server verifies the pinned model files at startup and downloads missing
assets. Set `EARSHOT_MODEL_DIR` to override the default cache path
`~/.cache/earshot/models`.

Set `EARSHOT_QUEUE` or pass `--queue` to change the default of four waiting
requests. A queue limit of zero allows one active request and no waiting.
Earshot writes JSON request logs to stderr with status, error code, and
elapsed time. On SIGINT or SIGTERM it closes its listener and gives admitted
requests up to 30 seconds to finish before canceling them.

## Test

```sh
go test ./...
EARSHOT_INTEGRATION=1 go test -count=1 ./internal/mlx
```

The integration suite uses the configured Python interpreter, cached model,
and FFmpeg with Opus encoding support. It requires access to Metal.
See the [macOS deployment guide](docs/deploy.md) for LaunchAgent and proxy setup,
and [PLAN.md](PLAN.md) for remaining work.

For an existing deployment, see the Git-ignored `LOCAL.md` in the provisioning
checkout for instance addresses and how to retrieve its bearer token.
