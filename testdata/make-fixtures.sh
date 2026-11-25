#!/bin/bash
set -euo pipefail

output=${1:-$(cd "$(dirname "$0")" && pwd)}
mkdir -p "$output"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for language in ko en; do
    if [[ "$language" == ko ]]; then
        voice=Yuna
        reference='안녕하세요. 오늘 서울의 날씨가 맑습니다. 오후 세 시에 지하철역에서 만나요.'
    else
        voice=Samantha
        reference='Hello. The weather in Seoul is clear today. Meet me at the subway station at three this afternoon.'
    fi

    say -v "$voice" -o "$work/$language.aiff" "$reference"
    frames=$(ffprobe -v error -select_streams a:0 -show_entries stream=duration_ts -of csv=p=0 "$work/$language.aiff")
    [[ "$frames" =~ ^[1-9][0-9]*$ ]] || { echo "No audio generated for $voice" >&2; exit 1; }
    printf '%s\n' "$reference" > "$output/$language.txt"
    ffmpeg -nostdin -loglevel error -y -i "$work/$language.aiff" -ar 16000 -ac 1 -c:a pcm_s16le "$output/$language.wav"
    ffmpeg -nostdin -loglevel error -y -i "$work/$language.aiff" -c:a aac -b:a 64k "$output/$language.m4a"
    ffmpeg -nostdin -loglevel error -y -i "$work/$language.aiff" -c:a libopus -b:a 48k "$output/$language.webm"
    ffmpeg -nostdin -loglevel error -y -i "$work/$language.aiff" -c:a libmp3lame -b:a 64k "$output/$language.mp3"
done
