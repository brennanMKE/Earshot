package earshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueueAndOperations(t *testing.T) {
	var logs bytes.Buffer
	started := make(chan string, 4)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := NewHandler(Config{
		Info:     EngineInfo{Model: "test-model", Engine: "test-engine", Languages: []string{"en", "ko", "yue"}},
		MaxBytes: 1024, MaxSeconds: 20, Queue: 2,
		Tokens: [][sha256.Size]byte{sha256.Sum256([]byte("secret-token"))},
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}, engineFunc(func(ctx context.Context, input Input) (Result, error) {
		started <- string(input.Audio)
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-release:
			return Result{Text: "private-transcript", Duration: 1}, nil
		}
	}))

	for _, path := range []string{"/v1/info", "/stats"} {
		response := get(t, handler, path, "")
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("unauthenticated %s: %d", path, response.Code)
		}
	}
	idle := get(t, handler, "/v1/info", "secret-token")
	for _, expected := range []string{
		`"model":"test-model"`, `"engine":"test-engine"`, `"languages":["en","ko"]`,
		`"max_bytes":1024`, `"max_seconds":20`, `"running":false`, `"queue_limit":2`,
	} {
		if !strings.Contains(idle.Body.String(), expected) {
			t.Fatalf("info missing %s: %s", expected, idle.Body)
		}
	}

	launch := func(ctx context.Context, audio string) <-chan *httptest.ResponseRecorder {
		req := request(t, audio, "", "ko", "private-prompt", "secret-token").WithContext(ctx)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			done <- response
		}()
		return done
	}
	first := launch(ctx, "private-audio-1")
	assertStarted(t, started, "private-audio-1")
	queuedContext, cancelQueued := context.WithCancel(ctx)
	defer cancelQueued()
	canceled := launch(queuedContext, "private-audio-canceled")
	waitDepth(t, handler, 1)
	second := launch(ctx, "private-audio-2")
	waitDepth(t, handler, 2)

	overflow := request(t, "private-audio-overflow", "", "", "", "secret-token")
	overflow.Body = io.NopCloser(unreadable{t})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, overflow)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" ||
		!strings.Contains(response.Body.String(), `"code":"busy"`) {
		t.Fatalf("overflow got %d: %s", response.Code, response.Body)
	}

	cancelQueued()
	assertResponse(t, canceled, http.StatusServiceUnavailable)
	waitDepth(t, handler, 1)
	third := launch(ctx, "private-audio-3")
	waitDepth(t, handler, 2)
	select {
	case next := <-started:
		t.Fatalf("concurrent engine request: %q", next)
	default:
	}
	for _, item := range []struct {
		done <-chan *httptest.ResponseRecorder
		next string
	}{{first, "private-audio-2"}, {second, "private-audio-3"}, {third, ""}} {
		select {
		case release <- struct{}{}:
		case <-time.After(5 * time.Second):
			t.Fatal("engine did not receive release")
		}
		assertResponse(t, item.done, http.StatusOK)
		if item.next != "" {
			assertStarted(t, started, item.next)
		}
	}

	response = get(t, handler, "/stats", "secret-token")
	var counts stats
	if err := json.Unmarshal(response.Body.Bytes(), &counts); err != nil {
		t.Fatal(err)
	}
	if counts.Requests != 5 || counts.Successes != 3 || counts.Samples != 3 ||
		counts.Errors["busy"] != 1 || counts.Errors["unavailable"] != 1 ||
		counts.ElapsedMS.P50 == nil || counts.RealTimeFactor.P95 == nil {
		t.Fatalf("unexpected stats: %s", response.Body)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry["msg"] != "transcribe" {
			t.Fatalf("invalid log line %q", line)
		}
		for _, private := range []string{"private-transcript", "private-audio", "private-prompt", "secret-token"} {
			if strings.Contains(line, private) {
				t.Fatalf("log included private data %q", private)
			}
		}
	}
}

type unreadable struct{ t *testing.T }

func (r unreadable) Read([]byte) (int, error) {
	r.t.Fatal("overflow request body was read")
	return 0, io.EOF
}

func get(t *testing.T, handler http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func waitDepth(t *testing.T, handler http.Handler, depth int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := get(t, handler, "/v1/info", "secret-token")
		var info struct {
			Running bool
			Depth   int `json:"queue_depth"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		if info.Running && info.Depth == depth {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not reach depth %d", depth)
}

func assertStarted(t *testing.T, started <-chan string, want string) {
	t.Helper()
	select {
	case next := <-started:
		if next != want {
			t.Fatalf("started %q, want %q", next, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%q did not start", want)
	}
}

func assertResponse(t *testing.T, done <-chan *httptest.ResponseRecorder, status int) {
	t.Helper()
	select {
	case response := <-done:
		if response.Code != status {
			t.Fatalf("got %d, want %d: %s", response.Code, status, response.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no response with status %d", status)
	}
}

func TestStatsWindow(t *testing.T) {
	m := metrics{errors: make(map[string]uint64)}
	empty := m.snapshot()
	if empty.ElapsedMS.P50 != nil || empty.RealTimeFactor.P95 != nil {
		t.Fatalf("empty stats = %+v", empty)
	}
	for i := 1; i <= sampleWindow*2; i++ {
		m.record(nil, Result{ElapsedMS: int64(i), Duration: 2})
	}
	m.record(noSpeech, Result{})
	report := m.snapshot()
	wantP50 := float64(sampleWindow + sampleWindow/2)
	wantP95 := float64(sampleWindow + (95*sampleWindow+99)/100)
	if report.Samples != sampleWindow || report.Requests != sampleWindow*2+1 ||
		report.Errors["no_speech"] != 1 || *report.ElapsedMS.P50 != wantP50 ||
		*report.ElapsedMS.P95 != wantP95 || *report.RealTimeFactor.P95 != wantP95/1000/2 {
		t.Fatalf("unexpected sample window: %+v", report)
	}
}
