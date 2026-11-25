package mlx

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"earshot/internal/earshot"
)

//go:embed worker.py
var workerSource string

const sampleRate = 16000

type Engine struct {
	ffmpeg string
	python string
	model  string
	dir    string
	info   earshot.EngineInfo // set before Open returns
	slot   chan struct{}
	worker *worker // guarded by slot
	closed bool
}

type worker struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
	info   earshot.EngineInfo
}

type reply struct {
	earshot.Result
	Ready     bool     `json:"ready"`
	Error     string   `json:"error"`
	Languages []string `json:"languages"`
}

// Open starts the worker and waits until its model has loaded.
func Open(ctx context.Context, ffmpeg, python, model, dir string) (*Engine, error) {
	ffmpeg, err := exec.LookPath(ffmpeg)
	if err != nil {
		return nil, fmt.Errorf("find FFmpeg: %w", err)
	}
	python, err = exec.LookPath(python)
	if err != nil {
		return nil, fmt.Errorf("find MLX Python: %w", err)
	}
	engine := &Engine{
		ffmpeg: ffmpeg, python: python, model: model, dir: dir,
		slot: make(chan struct{}, 1),
	}
	if err := engine.start(ctx); err != nil {
		return nil, err
	}
	engine.info = engine.worker.info
	return engine, nil
}

func (e *Engine) Info() earshot.EngineInfo {
	return e.info
}

func (e *Engine) start(ctx context.Context) error {
	cmd := exec.Command(e.python, "-u", "-c", workerSource, e.dir)
	// Terminal interrupts should reach the server without stopping active inference.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return err
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 8192), 4<<20)
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return err
	}
	current := &worker{cmd: cmd, input: input, output: scanner}
	stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	message, err := current.read()
	stop()
	if ctx.Err() != nil || err != nil || !message.Ready {
		current.close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("MLX worker could not load the model; check the Python environment and model files")
	}
	e.worker = current
	current.info = earshot.EngineInfo{Model: e.model, Engine: message.Engine, Languages: message.Languages}
	return nil
}

func (e *Engine) Close() {
	e.slot <- struct{}{}
	defer func() { <-e.slot }()
	e.closed = true
	e.reset()
}

func (e *Engine) reset() {
	if e.worker != nil {
		e.worker.close()
		e.worker = nil
	}
}

func (w *worker) close() {
	_ = w.cmd.Process.Kill()
	_ = w.input.Close()
	_ = w.cmd.Wait()
}

func (w *worker) read() (reply, error) {
	var result reply
	if !w.output.Scan() {
		if err := w.output.Err(); err != nil {
			return result, err
		}
		return result, io.ErrUnexpectedEOF
	}
	return result, json.Unmarshal(w.output.Bytes(), &result)
}

func (e *Engine) Transcribe(ctx context.Context, input earshot.Input) (earshot.Result, error) {
	select {
	case e.slot <- struct{}{}:
		defer func() { <-e.slot }()
	case <-ctx.Done():
		return earshot.Result{}, ctx.Err()
	}
	if e.closed {
		return earshot.Result{}, errors.New("MLX worker is closed")
	}

	decodeContext, cancelDecode := context.WithTimeout(ctx, time.Minute)
	wav, err := decode(decodeContext, e.ffmpeg, input)
	cancelDecode()
	if err != nil {
		return earshot.Result{}, err
	}
	if e.worker == nil {
		startup, cancelStartup := context.WithTimeout(ctx, 2*time.Minute)
		err := e.start(startup)
		cancelStartup()
		if err != nil {
			return earshot.Result{}, err
		}
	}
	current := e.worker
	stop := context.AfterFunc(ctx, func() { _ = current.cmd.Process.Kill() })
	message, err := current.transcribe(wav, input)
	stop()
	if ctx.Err() != nil || err != nil {
		e.reset()
		if ctx.Err() != nil {
			return earshot.Result{}, ctx.Err()
		}
		return earshot.Result{}, err
	}
	switch message.Error {
	case "":
		message.Result.Model = e.model
		return message.Result, nil
	case "no_speech":
		return earshot.Result{}, earshot.ErrNoSpeech
	case "too_large":
		return earshot.Result{}, earshot.ErrTooLarge
	case "bad_language":
		return earshot.Result{}, earshot.ErrBadLanguage
	default:
		return earshot.Result{}, errors.New("MLX inference failed")
	}
}

func (w *worker) transcribe(wav []byte, input earshot.Input) (reply, error) {
	header := struct {
		Size       int    `json:"size"`
		Language   string `json:"language"`
		Prompt     string `json:"prompt"`
		MaxSeconds int    `json:"max_seconds"`
	}{len(wav), input.Language, input.Prompt, input.MaxSeconds}
	if err := json.NewEncoder(w.input).Encode(header); err != nil {
		return reply{}, err
	}
	if _, err := w.input.Write(wav); err != nil {
		return reply{}, err
	}
	return w.read()
}

func decode(ctx context.Context, ffmpeg string, input earshot.Input) ([]byte, error) {
	// Some MP4 files require seeking. Give FFmpeg a seekable descriptor without
	// letting the demuxer open arbitrary paths or reach the network.
	file, err := os.CreateTemp("", "earshot-audio-*")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := os.Remove(file.Name()); err != nil {
		return nil, err
	}
	if _, err := file.Write(input.Audio); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-v", "error", "-nostdin", "-protocol_whitelist", "fd",
		"-format_whitelist", "mov,mp3,wav,matroska,webm,ogg,aac",
		"-i", "fd:", "-map", "0:a:0",
		"-t", strconv.FormatFloat(float64(input.MaxSeconds)+1, 'f', 0, 64),
		"-ac", "1", "-ar", strconv.Itoa(sampleRate), "-c:a", "pcm_s16le", "-f", "wav", "pipe:1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = file
	wav, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, earshot.ErrUnsupported
		}
		return nil, err
	}
	return wav, nil
}
