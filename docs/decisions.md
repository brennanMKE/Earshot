# Phase 0 engine comparison

Choose **mlx-whisper with large-v3-turbo** for the first Earshot
implementation. On the M1 it brought the median for a 30-second Korean passage
from 6.16 s to 3.68 s with the same normalized transcript. The speedup costs
about 0.52 GiB of peak memory footprint on the M1 and adds a Python runtime.

Keep a long-lived Python worker behind the Go service so MLX can reuse the
loaded model. The mlx-whisper 0.4.3 transcription API returns a language code
and per-segment no-speech probabilities. It computes language probabilities
internally and omits them from its result. Settle how the HTTP contract
represents unavailable language confidence before Agora relies on it.

Both benchmark hosts had 8 GiB of unified memory and ran macOS 27.0 (26A428).

## M2 measurements

Both engines used the standard large-v3-turbo weights for their respective
runtimes, with automatic language detection and engine-default decoding.
whisper.cpp 1.9.5 (Homebrew revision 2) used Metal and ran as a loopback HTTP
server. mlx-whisper 0.4.3 ran in a single Python 3.12 process with MLX 0.32.5.

| Engine | English median (5.674 s) | Korean median (5.615 s) | English CER | Korean CER | Peak memory footprint |
| --- | ---: | ---: | ---: | ---: | ---: |
| whisper.cpp | 3.265 s | 3.291 s | 6.4% | 6.5% | 1.77 GiB |
| mlx-whisper | 1.897 s | 1.911 s | 6.4% | 3.2% | 2.22 GiB |

Each median covers three runs after one warm-up for that clip. Timing includes
the engine invocation on already-normalized WAV audio. The whisper.cpp timing
also includes the local HTTP request. Peak memory is the macOS
`/usr/bin/time -l` peak memory footprint over each engine process's entire run.
Both engines detected both languages. Both wrote the spoken time as a digit,
which counts toward CER. whisper.cpp also changed the Korean particle in
“오후 세 시에” from “에” to “의.” These fixtures contain one synthetic voice per
language and are too small to establish a Korean accuracy difference.

## 30-second Korean passage

| Host | Engine | Warm median | Real-time factor | CER | Peak memory footprint |
| --- | --- | ---: | ---: | ---: | ---: |
| M2 | whisper.cpp | 3.845 s | 0.128 | 1.5% | 1.78 GiB |
| M2 | mlx-whisper | 2.415 s | 0.081 | 1.5% | 2.27 GiB |
| M1 | whisper.cpp | 6.156 s | 0.205 | 1.5% | 1.78 GiB |
| M1 | mlx-whisper | 3.680 s | 0.123 | 1.5% | 2.30 GiB |

The passage uses distinct sentences. A first M1 timing used a repeated
sentence and whisper.cpp dropped several repetitions, so that clip was
replaced to measure latency with the full passage transcribed. Existing services were running during the tests. Their latency
under transcription load was not measured.

## Reproduce

Use `testdata/make-fixtures.sh` on macOS with the Yuna and Samantha voices
installed and FFmpeg on PATH. It generates AAC/M4A, Opus/WebM, MP3 and 16 kHz
mono WAV versions. The benchmark uses the WAV files for both engines.

Download the [whisper.cpp model](https://huggingface.co/ggerganov/whisper.cpp/blob/main/ggml-large-v3-turbo.bin).
The tested file's SHA-256 was
`1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69`.

In one terminal start the loopback server. Stop it with Ctrl-C after the
benchmark to get the memory footprint from `time`.

```sh
/usr/bin/time -l whisper-server -m /path/to/ggml-large-v3-turbo.bin --host 127.0.0.1 --port 18090
```

In a second terminal, run the benchmark from the project root. Stop
whisper.cpp before running MLX so both tests run alongside the same background workload without the
other engine consuming memory. Use a Python 3.12 virtual environment with
`mlx-whisper==0.4.3`. Download the
[MLX model](https://huggingface.co/mlx-community/whisper-large-v3-turbo/tree/a4aaeec0636e6fef84abdcbe3544cb2bf7e9f6fb)
before running the benchmark.

```sh
python3 scripts/phase0.py cpp testdata
/usr/bin/time -l .venv/bin/python scripts/phase0.py mlx testdata --mlx-model /path/to/mlx-model
```

Run the same commands with `testdata/long` for the 30-second Korean sample.
The reference is `testdata/long/ko.txt`. To regenerate its WAV:

```sh
say -v Yuna -f testdata/long/ko.txt -o /tmp/earshot-ko-long.aiff
ffmpeg -i /tmp/earshot-ko-long.aiff -af atempo=0.83,apad -t 30 \
  -ar 16000 -ac 1 -c:a pcm_s16le testdata/long/ko.wav
rm /tmp/earshot-ko-long.aiff
```
