package earshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

type engineFunc func(context.Context, Input) (Result, error)

func (f engineFunc) Transcribe(ctx context.Context, input Input) (Result, error) {
	return f(ctx, input)
}

func request(t *testing.T, audio, mimeType, language, prompt, token string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if audio != "" {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="file"; filename="clip.wav"`)
		if mimeType != "" {
			header.Set("Content-Type", mimeType)
		}
		part, err := form.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(audio)); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range [][2]string{{"language", language}, {"prompt", prompt}} {
		if field[1] != "" {
			if err := form.WriteField(field[0], field[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/transcribe", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestTranscribe(t *testing.T) {
	const audio = "RIFF\x00\x00\x00\x00WAVEfmt "
	const prompt = "회의"
	handler := NewHandler(Config{
		MaxBytes: 1024, MaxSeconds: 30, Tokens: [][sha256.Size]byte{sha256.Sum256([]byte("test"))},
	}, engineFunc(func(_ context.Context, in Input) (Result, error) {
		if string(in.Audio) != audio || in.ContentType != "audio/wave" || in.Language != "ko" ||
			in.Prompt != prompt || in.MaxSeconds != 30 {
			t.Errorf("unexpected input: %+v", in)
		}
		return Result{
			Text: "안녕하세요", Language: "ko", Duration: 1.5, Model: "fake", Engine: "fake",
			Segments: []Segment{{Start: 0, End: 1.5, Text: "안녕하세요"}},
		}, nil
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request(t, audio, "", "ko", prompt, "test"))
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("got %d: %s", response.Code, response.Body)
	}
	var result Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Text != "안녕하세요" || result.Language != "ko" || result.Duration != 1.5 ||
		result.Model != "fake" || result.Engine != "fake" || len(result.Segments) != 1 ||
		!strings.Contains(response.Body.String(), `"language_probability":null`) ||
		!strings.Contains(response.Body.String(), `"elapsed_ms":`) {
		t.Errorf("unexpected response: %s", response.Body)
	}
}

func TestTranscriptionErrors(t *testing.T) {
	for _, tc := range []struct {
		name, audio, language, prompt, token string
		duration                             float64
		engineError                          error
		status                               int
		code                                 string
	}{
		{"missing auth", "wav", "", "", "", 0, nil, 401, "unauthorized"},
		{"wrong auth", "wav", "", "", "wrong", 0, nil, 401, "unauthorized"},
		{"missing file", "", "", "", "test", 0, nil, 400, "bad_request"},
		{"bad language", "wav", "zz", "", "test", 0, nil, 400, "bad_request"},
		{"long prompt", "wav", "", strings.Repeat("한", 201), "test", 0, nil, 400, "bad_request"},
		{"large file", "over limit", "", "", "test", 0, nil, 413, "too_large"},
		{"long audio", "wav", "", "", "test", 31, nil, 413, "too_large"},
		{"engine duration limit", "wav", "", "", "test", 0, ErrTooLarge, 413, "too_large"},
		{"unsupported format", "wav", "", "", "test", 0, ErrUnsupported, 415, "unsupported_media"},
		{"no speech", "wav", "", "", "test", 0, ErrNoSpeech, 422, "no_speech"},
		{"engine down", "wav", "", "", "test", 0, errors.New("private engine detail"), 503, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := NewHandler(Config{
				MaxBytes: 3, MaxSeconds: 30, Tokens: [][sha256.Size]byte{sha256.Sum256([]byte("test"))},
			}, engineFunc(func(_ context.Context, input Input) (Result, error) {
				called = true
				if input.ContentType != "audio/wav" {
					t.Errorf("declared type not preserved: %q", input.ContentType)
				}
				return Result{Duration: tc.duration}, tc.engineError
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request(t, tc.audio, "audio/wav", tc.language, tc.prompt, tc.token))
			var body struct {
				Error requestError `json:"error"`
			}
			err := json.Unmarshal(response.Body.Bytes(), &body)
			if err != nil || response.Code != tc.status || body.Error.Code != tc.code ||
				strings.Contains(response.Body.String(), "private engine detail") {
				t.Errorf("got %d: %s", response.Code, response.Body)
			}
			wantEngine := tc.duration > 0 || tc.engineError != nil
			if called != wantEngine {
				t.Errorf("engine called = %t, want %t", called, wantEngine)
			}
		})
	}
}

func TestHealthWithoutAuth(t *testing.T) {
	handler := NewHandler(Config{Version: "test"}, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != 200 || response.Body.String() != "ok test\n" ||
		!strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("got %d: %s", response.Code, response.Body)
	}
}
