package mlx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"earshot/internal/earshot"
)

func TestMLXIntegration(t *testing.T) {
	if os.Getenv("EARSHOT_INTEGRATION") != "1" {
		t.Skip("set EARSHOT_INTEGRATION=1 to run the real-engine tests")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("MLX integration requires an Apple Silicon Mac")
	}

	root := os.Getenv("EARSHOT_MODEL_DIR")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		root = filepath.Join(home, ".cache", "earshot", "models")
	}
	ffmpeg := os.Getenv("EARSHOT_FFMPEG")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	python := os.Getenv("EARSHOT_PYTHON")
	if python == "" {
		python = "python3"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir, err := filepath.Abs(filepath.Join(root, DefaultModel))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := Open(ctx, ffmpeg, python, DefaultModel, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	fixtures := filepath.Join("..", "..", "testdata")
	handler := earshot.NewHandler(earshot.Config{
		MaxBytes: earshot.DefaultMaxBytes, MaxSeconds: 10, NoAuth: true,
	}, engine)
	firstWorker := engine.worker
	var validInput earshot.Input
	for _, language := range []string{"ko", "en"} {
		for _, format := range []string{"m4a", "webm", "mp3", "wav", "ogg"} {
			t.Run(language+"/"+format, func(t *testing.T) {
				filename := language + "." + format
				var audio []byte
				var err error
				if format == "ogg" {
					audio, err = exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i",
						filepath.Join(fixtures, language+".wav"), "-c:a", "libopus", "-f", "ogg", "pipe:1").Output()
				} else {
					audio, err = os.ReadFile(filepath.Join(fixtures, filename))
				}
				if err != nil {
					t.Fatal(err)
				}
				validInput = earshot.Input{Audio: audio, MaxSeconds: 10}
				result := postAudio(t, ctx, handler, filename, audio, http.StatusOK, "")
				if result.Language != language || result.LanguageProbability != nil ||
					result.Model != DefaultModel || !strings.HasPrefix(result.Engine, "mlx-whisper ") ||
					result.Duration < 5 || result.Duration > 7 || len(result.Segments) == 0 {
					t.Fatalf("unexpected result: %+v", result)
				}
				keywords := []string{"서울", "날씨", "지하철"}
				if language == "en" {
					keywords = []string{"Seoul", "weather", "subway"}
				}
				for _, word := range keywords {
					if !strings.Contains(result.Text, word) {
						t.Errorf("transcript missing %q: %q", word, result.Text)
					}
				}
				if engine.worker != firstWorker {
					t.Fatal("worker restarted between requests")
				}
			})
		}
	}
	t.Run("silence", func(t *testing.T) {
		audio, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi",
			"-i", "anullsrc=r=16000:cl=mono", "-t", "1", "-f", "wav", "pipe:1").Output()
		if err != nil {
			t.Fatal(err)
		}
		postAudio(t, ctx, handler, "silence.wav", audio, 422, "no_speech")
	})
	t.Run("duration limit", func(t *testing.T) {
		audio, err := os.ReadFile(filepath.Join(fixtures, "long", "ko.wav"))
		if err != nil {
			t.Fatal(err)
		}
		postAudio(t, ctx, handler, "long.wav", audio, 413, "too_large")
	})
	t.Run("invalid audio", func(t *testing.T) {
		postAudio(t, ctx, handler, "invalid.wav", []byte("invalid audio"), 415, "unsupported_media")
	})
	t.Run("unsupported language", func(t *testing.T) {
		input := validInput
		input.Language = "aa"
		_, err := engine.Transcribe(ctx, input)
		if !errors.Is(err, earshot.ErrBadLanguage) {
			t.Fatalf("unsupported language returned %v", err)
		}
	})
	t.Run("worker restart", func(t *testing.T) {
		if err := firstWorker.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		postAudio(t, ctx, handler, "retry.ogg", validInput.Audio, 503, "unavailable")
		postAudio(t, ctx, handler, "retry.ogg", validInput.Audio, http.StatusOK, "")
		if engine.worker == firstWorker {
			t.Fatal("worker did not restart after exiting")
		}
	})
}

func postAudio(t *testing.T, ctx context.Context, handler http.Handler, filename string, audio []byte, status int, code string) earshot.Result {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(audio); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/transcribe", &body).WithContext(ctx)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s returned %d, want %d: %s", filename, response.Code, status, response.Body)
	}
	var result struct {
		earshot.Result
		Error struct{ Code string }
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error.Code != code {
		t.Fatalf("error code = %q, want %q", result.Error.Code, code)
	}
	return result.Result
}
