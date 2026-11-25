package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"earshot/internal/earshot"
	"earshot/internal/mlx"
)

const version = "0.1.0-dev"

var logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))

func main() {
	if err := run(os.Args[1:]); err != nil {
		logger.Error("earshot", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: earshot <serve|fetch-model>")
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Println("usage: earshot <serve|fetch-model>")
		return nil
	case "serve":
		return serve(args[1:])
	case "fetch-model":
		return fetchModel(args[1:])
	default:
		return errors.New("unknown command; use earshot --help")
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := flags.String("addr", envOr("EARSHOT_ADDR", "127.0.0.1:8090"), "HTTP listen address")
	tokenFile := flags.String("tokens-file", os.Getenv("EARSHOT_TOKENS_FILE"), "file containing labeled SHA-256 bearer token hashes")
	maxBytes := flags.String("max-bytes", envOr("EARSHOT_MAX_BYTES", strconv.Itoa(earshot.DefaultMaxBytes)), "maximum audio upload size in bytes")
	maxSeconds := flags.String("max-seconds", envOr("EARSHOT_MAX_SECONDS", strconv.Itoa(earshot.DefaultMaxSeconds)), "maximum audio duration in seconds")
	queueLimit := flags.String("queue", envOr("EARSHOT_QUEUE", strconv.Itoa(earshot.DefaultQueue)), "maximum number of waiting requests")
	noAuth := flags.Bool("insecure-no-auth", false, "disable bearer authentication for local development")
	fake := flags.Bool("fake-engine", false, "use the development stub without running transcription")
	ffmpeg := flags.String("ffmpeg", envOr("EARSHOT_FFMPEG", "ffmpeg"), "path to FFmpeg")
	python := flags.String("python", envOr("EARSHOT_PYTHON", "python3"), "Python interpreter with mlx-whisper installed")
	model, modelDir := modelFlags(flags)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("serve does not accept positional arguments")
	}
	size, err := strconv.ParseInt(*maxBytes, 10, 64)
	if err != nil || size < 1 || size > math.MaxInt64-(1<<20) {
		return errors.New("invalid maximum upload size")
	}
	duration, err := strconv.Atoi(*maxSeconds)
	if err != nil || duration < 1 || duration > math.MaxInt32 {
		return errors.New("invalid maximum duration")
	}
	queue, err := strconv.Atoi(*queueLimit)
	if err != nil || queue < 0 {
		return errors.New("invalid queue limit")
	}
	config := earshot.Config{
		Version: version, MaxBytes: size, MaxSeconds: duration, NoAuth: *noAuth,
		Queue: queue, Logger: logger,
	}
	if !config.NoAuth {
		config.Tokens, err = readTokens(*tokenFile)
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var engine earshot.Engine = fakeEngine{}
	config.Info = earshot.EngineInfo{Model: "fake", Engine: "fake", Languages: earshot.ISO639Languages()}
	if !*fake {
		worker, err := openEngine(ctx, *ffmpeg, *python, *model, *modelDir)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		defer worker.Close()
		engine = worker
		config.Info = worker.Info()
	}
	server := http.Server{
		Addr: *addr, Handler: earshot.NewHandler(config, engine),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 60 * time.Second,
		IdleTimeout: 60 * time.Second,
	}
	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp", *addr)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	logger.Info("listening", "address", listener.Addr().String(), "version", version, "engine", config.Info.Engine)
	return serveHTTP(ctx, &server, listener, 30*time.Second)
}

func modelFlags(flags *flag.FlagSet) (model, directory *string) {
	return flags.String("model", envOr("EARSHOT_MODEL", mlx.DefaultModel), "model name"),
		flags.String("model-dir", os.Getenv("EARSHOT_MODEL_DIR"), "model cache root (default ~/.cache/earshot/models)")
}

func fetch(ctx context.Context, model, directory string) (string, error) {
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		directory = filepath.Join(home, ".cache", "earshot", "models")
	}
	return mlx.Fetch(ctx, directory, model)
}

func fetchModel(args []string) error {
	flags := flag.NewFlagSet("fetch-model", flag.ContinueOnError)
	model, directory := modelFlags(flags)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("fetch-model does not accept positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	path, err := fetch(ctx, *model, *directory)
	if err == nil {
		fmt.Println(path)
	}
	return err
}

func openEngine(ctx context.Context, ffmpeg, python, model, directory string) (*mlx.Engine, error) {
	path, err := fetch(ctx, model, directory)
	if err != nil {
		return nil, err
	}
	startup, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return mlx.Open(startup, ffmpeg, python, model, path)
}

func readTokens(path string) ([][sha256.Size]byte, error) {
	if path == "" {
		return nil, errors.New("set EARSHOT_TOKENS_FILE or pass --tokens-file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open token file: %w", err)
	}
	defer file.Close()

	var tokens [][sha256.Size]byte
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid token entry on line %d", line)
		}
		var hash [sha256.Size]byte
		if len(fields[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid SHA-256 hash on line %d", line)
		}
		if _, err := hex.Decode(hash[:], []byte(fields[0])); err != nil {
			return nil, fmt.Errorf("invalid SHA-256 hash on line %d", line)
		}
		tokens = append(tokens, hash)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}
	if len(tokens) == 0 {
		return nil, errors.New("token file contains no entries")
	}
	return tokens, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type fakeEngine struct{}

func (fakeEngine) Transcribe(_ context.Context, input earshot.Input) (earshot.Result, error) {
	const text = "Development stub"
	language := input.Language
	if language == "" {
		language = "en"
	}
	return earshot.Result{
		Text: text, Language: language, Duration: 1, Model: "fake", Engine: "fake",
		Segments: []earshot.Segment{{Start: 0, End: 1, Text: text}},
	}, nil
}
