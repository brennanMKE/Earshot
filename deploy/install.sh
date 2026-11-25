#!/bin/bash
set -euo pipefail
umask 077

usage() {
    cat <<'HELP'
Usage: deploy/install.sh [--binary PATH] [--tokens-file PATH]
                         [--python PATH] [--ffmpeg PATH] [--model-dir PATH]

Without --binary, build from the Earshot source tree with Go.
An existing installed token file is reused when --tokens-file is omitted.
HELP
}

fail() {
    printf 'earshot install: %s\n' "$*" >&2
    exit 1
}

here=$(cd "$(dirname "$0")" && pwd)
app="$HOME/Library/Application Support/Earshot"
logs="$HOME/Library/Logs/earshot"
agents="$HOME/Library/LaunchAgents"
label=local.earshot.server
domain="gui/$(id -u)"
service="$domain/$label"
plist="$agents/$label.plist"
binary="$app/bin/earshot"
tokens="$app/tokens.txt"
source_binary=
source_tokens="$tokens"
python=${EARSHOT_PYTHON:-"$app/.venv/bin/python"}
ffmpeg=${EARSHOT_FFMPEG:-ffmpeg}
model_dir=${EARSHOT_MODEL_DIR:-"$HOME/.cache/earshot/models"}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --help|-h) usage; exit 0 ;;
        --binary|--tokens-file|--python|--ffmpeg|--model-dir)
            [[ $# -ge 2 && -n "$2" ]] || fail "$1 requires a value"
            case "$1" in
                --binary) source_binary=$2 ;;
                --tokens-file) source_tokens=$2 ;;
                --python) python=$2 ;;
                --ffmpeg) ffmpeg=$2 ;;
                --model-dir) model_dir=$2 ;;
            esac
            shift 2 ;;
        *) fail "unknown option $1" ;;
    esac
done

[[ $(uname -s) == Darwin && $(uname -m) == arm64 ]] || fail "requires Apple Silicon macOS"
launchctl print "$domain" >/dev/null 2>&1 || fail "a logged-in macOS GUI session is required"
python=$(command -v "$python") || fail "Python environment missing; see docs/deploy.md"
ffmpeg=$(command -v "$ffmpeg") || fail "FFmpeg missing"
[[ "$python" == /* && "$ffmpeg" == /* && "$model_dir" == /* ]] ||
    fail "Python, FFmpeg, and model cache paths must be absolute"
"$python" -c 'import mlx_whisper' >/dev/null || fail "Python cannot load mlx-whisper"
"$ffmpeg" -hide_banner -protocols | awk '$1 == "fd" { found=1 } END { exit !found }' ||
    fail "FFmpeg does not support the fd input protocol"
[[ -r "$source_tokens" ]] || fail "provide a bearer hash file via --tokens-file"
awk '
    NF == 0 || $1 ~ /^#/ { next }
    NF != 2 || length($1) != 64 || $1 !~ /^[[:xdigit:]]+$/ { invalid=1 }
    { count++ }
    END { exit (invalid || !count) }
' "$source_tokens" || fail "the bearer hash file has invalid entries or is empty"

mkdir -p "$app/bin" "$logs" "$agents"
stage=$(mktemp -d "$app/.install.XXXXXXXX")
plist_stage=
changed=0
committed=0
loaded=0

bootstrap() {
    local attempt output
    for ((attempt=0; attempt<12; attempt++)); do
        if output=$(launchctl bootstrap "$domain" "$plist" 2>&1); then
            return 0
        fi
        sleep 1
    done
    printf '%s\n' "$output" >&2
    return 1
}

cleanup() {
    rc=$?
    trap - EXIT
    if [[ "$changed" == 1 && "$committed" == 0 ]]; then
        printf 'earshot install: restoring the previous installation\n' >&2
        launchctl bootout "$service" >/dev/null 2>&1 || true
        for name in binary tokens plist; do
            target=${!name}
            if [[ -f "$stage/old-$name" ]]; then
                cp -p "$stage/old-$name" "$target" || rc=1
            else
                rm -f "$target" || rc=1
            fi
        done
        if [[ "$loaded" == 1 ]]; then
            bootstrap || rc=1
        fi
    fi
    [[ -z "$plist_stage" ]] || rm -f "$plist_stage"
    rm -rf "$stage"
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ -n "$source_binary" ]]; then
    install -m 0755 "$source_binary" "$stage/earshot"
else
    (cd "$here/.." && CGO_ENABLED=0 go build -trimpath -o "$stage/earshot" ./cmd/earshot)
fi
"$stage/earshot" fetch-model --model-dir "$model_dir" >/dev/null
install -m 0600 "$source_tokens" "$stage/tokens"
plist_stage=$(mktemp "$agents/.earshot.XXXXXXXX")
cp "$here/$label.plist" "$plist_stage"
/usr/libexec/PlistBuddy -c "Set :ProgramArguments:0 $binary" "$plist_stage"
/usr/libexec/PlistBuddy -c "Set :ProgramArguments:5 $tokens" "$plist_stage"
/usr/libexec/PlistBuddy -c "Set :ProgramArguments:7 $python" "$plist_stage"
/usr/libexec/PlistBuddy -c "Set :ProgramArguments:9 $ffmpeg" "$plist_stage"
/usr/libexec/PlistBuddy -c "Set :ProgramArguments:11 $model_dir" "$plist_stage"
/usr/bin/plutil -replace WorkingDirectory -string "$app" "$plist_stage"
/usr/bin/plutil -replace StandardOutPath -string "$logs/stdout.log" "$plist_stage"
/usr/bin/plutil -replace StandardErrorPath -string "$logs/stderr.log" "$plist_stage"
/usr/bin/plutil -lint "$plist_stage" >/dev/null

if launchctl print "$service" >/dev/null 2>&1; then
    loaded=1
    [[ -f "$plist" ]] || fail "the running job has no installed plist to restore"
elif /usr/sbin/lsof -nP -tiTCP:8090 -sTCP:LISTEN >/dev/null 2>&1; then
    fail "port 8090 already has a listener"
fi
for name in binary tokens plist; do
    target=${!name}
    if [[ -f "$target" ]]; then
        cp -p "$target" "$stage/old-$name"
    fi
done

changed=1
if [[ "$loaded" == 1 ]]; then
    launchctl bootout "$service"
fi
mv -f "$stage/earshot" "$binary"
mv -f "$stage/tokens" "$tokens"
mv -f "$plist_stage" "$plist"
bootstrap

for ((attempt=0; attempt<45; attempt++)); do
    if response=$(curl --noproxy '*' -fsS --max-time 1 http://127.0.0.1:8090/healthz 2>/dev/null) &&
        [[ "$response" == 'ok '* ]]; then
        committed=1
        printf 'Earshot is ready at http://127.0.0.1:8090/healthz\n'
        printf 'Proxy /earshot to http://127.0.0.1:8090 and strip the prefix.\n'
        exit 0
    fi
    sleep 1
done
fail "health check failed; see $logs/stderr.log"
