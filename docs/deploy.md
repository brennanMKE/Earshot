# Deploy Earshot on macOS

Earshot runs as a user LaunchAgent on Apple Silicon and listens on
`127.0.0.1:8090`. A private-network reverse proxy exposes its HTTP API. The
account running the LaunchAgent needs a logged-in macOS GUI session with Metal
available. Roll out one instance at a time and verify it before updating the
next instance.

## Prepare the runtime

Install FFmpeg with the `fd` input protocol and Python 3.12. The example below
uses `uv`. A source install also requires Go 1.22 or newer.

```sh
app="$HOME/Library/Application Support/Earshot"
umask 077
mkdir -p "$app"
uv venv --python 3.12 "$app/.venv"
uv pip install --python "$app/.venv/bin/python" mlx-whisper==0.4.3 mlx==0.32.5
```

Generate one strong bearer token for each client and store it in the client's secret
configuration. Put only the SHA-256 hash and label in each instance's token
file. This example saves the raw token in a local Git-ignored file and writes
a hash file that can be copied to the instances.

```sh
umask 077
token=$(openssl rand -hex 32)
(set -C; printf 'EARSHOT_TOKEN=%s\n' "$token" > .env.earshot) || exit 1
printf '%s' "$token" | shasum -a 256 | awk '{print $1, "client"}' > tokens.txt
unset token
```

## Install the LaunchAgent

Run from the source tree. For a prebuilt arm64 binary, supply `--binary
/path/to/earshot` alongside the token file. Subsequent installs can reuse the
installed token file by omitting `--tokens-file`.

```sh
./deploy/install.sh --tokens-file ./tokens.txt
```

The installer verifies or downloads the pinned model, installs the binary
and token hashes under `~/Library/Application Support/Earshot/`, and loads
`local.earshot.server`. Logs are under `~/Library/Logs/earshot/`. Optional
`--python`, `--ffmpeg`, and `--model-dir` arguments override the detected or
default absolute paths. If the service fails its startup health check, the
installer restores any prior installation.

```sh
launchctl print "gui/$(id -u)/local.earshot.server"
curl --fail-with-body http://127.0.0.1:8090/healthz
```

## Add the reverse-proxy route

Add this object to the existing PhotonHTTP configuration's `proxy` array.
Leave the existing routes in the array. The route forwards the bearer header
to Earshot and removes the `/earshot` prefix. Keep automatic proxy retries
off so the client controls failover.

```json
{
  "path": "/earshot",
  "upstream": "http://127.0.0.1:8090",
  "stripPrefix": true,
  "forwardedHeaders": "x-forwarded",
  "timeouts": {"connect": 2, "response": 600, "idle": 60},
  "retries": 0,
  "health": {"failures": 3, "cooldown": 5, "path": "/healthz", "interval": 10}
}
```

Set the top-level PhotonHTTP `maxBodySize` to at least `27262976` bytes
(26 MiB) for the default Earshot limits. This leaves 1 MiB for multipart
framing and fields. The PhotonHTTP setting applies to every route on the
listener. Earshot enforces its own 25 MiB audio file limit.

Validate the changed config and keep a copy of the previous configuration
until verification finishes. Route-only changes can use `reload`.
Changing `maxBodySize` requires a graceful listener `restart`.

```sh
photon check --config "$PROXY_CONFIG"
photon restart --config "$PROXY_CONFIG"
```

## Verify from an authorized client

Run these from the same network context as the client. Set `EARSHOT_BASE` to the
private instance URL including the `/earshot` prefix and load the token
from the client's secret configuration. Repeat for each instance.

```sh
curl --fail-with-body "$EARSHOT_BASE/healthz"
printf 'header = "Authorization: Bearer %s"\n' "$EARSHOT_TOKEN" |
  curl --config - --fail-with-body "$EARSHOT_BASE/v1/info"
printf 'header = "Authorization: Bearer %s"\n' "$EARSHOT_TOKEN" |
  curl --config - --fail-with-body \
    -F 'file=@testdata/ko.m4a;type=audio/mp4' \
    "$EARSHOT_BASE/v1/transcribe"
```

For machine-specific addresses and rollout order, see the local Git-ignored
`LOCAL.md`. Open `<EARSHOT_BASE>/ui/` and enter an Earshot access token to
upload an audio file or make a recording. Microphone recording requires HTTPS
when accessing a remote host. With HTTPS certificates already enabled in the
tailnet, Tailscale Serve can expose just the Earshot route over HTTPS:

```sh
tailscale serve --bg --https=443 --set-path=/earshot http://127.0.0.1:8080/earshot
```

Open the resulting HTTPS URL with `/earshot/ui/` and grant microphone access
when the browser asks. See the [HTTP contract](../CONTRACT.md) for API responses.
