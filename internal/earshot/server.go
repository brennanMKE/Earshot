package earshot

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultMaxBytes   = 25 << 20
	DefaultMaxSeconds = 600
	formOverhead      = 1 << 20
	maxFieldBytes     = 4 * 200
)

var (
	ErrTooLarge    = errors.New("audio exceeds the duration limit")
	ErrUnsupported = errors.New("audio could not be decoded")
	ErrBadLanguage = errors.New("language unsupported by the engine")
	ErrNoSpeech    = errors.New("audio contains no speech")
)

type Input struct {
	Audio       []byte
	ContentType string
	Language    string
	Prompt      string
	MaxSeconds  int
}

type Engine interface {
	Transcribe(context.Context, Input) (Result, error)
}

type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type Result struct {
	Text                string    `json:"text"`
	Language            string    `json:"language"`
	LanguageProbability *float64  `json:"language_probability"`
	Duration            float64   `json:"duration_s"`
	Segments            []Segment `json:"segments"`
	Model               string    `json:"model"`
	Engine              string    `json:"engine"`
	ElapsedMS           int64     `json:"elapsed_ms"`
}

type EngineInfo struct {
	Model     string   `json:"model"`
	Engine    string   `json:"engine"`
	Languages []string `json:"languages"`
}

type Config struct {
	Version    string
	MaxBytes   int64
	MaxSeconds int
	Tokens     [][sha256.Size]byte
	NoAuth     bool
	Queue      int
	Info       EngineInfo
	Logger     *slog.Logger
}

func NewHandler(config Config, engine Engine) http.Handler {
	queue := queue{limit: config.Queue}
	metrics := metrics{errors: make(map[string]uint64)}
	info := config.Info
	info.Languages = slices.DeleteFunc(append([]string{}, info.Languages...), func(code string) bool {
		return !validLanguage(code)
	})
	mux := http.NewServeMux()
	registerUI(mux, config)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok "+config.Version+"\n")
	})
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		if !config.checkAuth(w, r) {
			writeError(w, unauthorized)
			return
		}
		running, depth := queue.state()
		writeJSON(w, http.StatusOK, struct {
			EngineInfo
			MaxBytes   int64 `json:"max_bytes"`
			MaxSeconds int   `json:"max_seconds"`
			Running    bool  `json:"running"`
			QueueDepth int   `json:"queue_depth"`
			QueueLimit int   `json:"queue_limit"`
		}{info, config.MaxBytes, config.MaxSeconds, running, depth, config.Queue})
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		if !config.checkAuth(w, r) {
			writeError(w, unauthorized)
			return
		}
		writeJSON(w, http.StatusOK, metrics.snapshot())
	})
	mux.HandleFunc("POST /v1/transcribe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		start := time.Now()
		result, problem := transcribe(w, r, config, engine, &queue)
		result.ElapsedMS = time.Since(start).Milliseconds()
		metrics.record(problem, result)
		status, code := http.StatusOK, "ok"
		if problem != nil {
			status, code = problem.Status, problem.Code
		}
		if config.Logger != nil {
			config.Logger.Info("transcribe", "status", status, "code", code, "elapsed_ms", result.ElapsedMS)
		}
		if problem != nil {
			writeError(w, problem)
		} else {
			writeJSON(w, http.StatusOK, result)
		}
	})
	return mux
}

func (config Config) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if config.NoAuth || authorized(r.Header.Get("Authorization"), config.Tokens) {
		return true
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	return false
}

func transcribe(w http.ResponseWriter, r *http.Request, config Config, engine Engine, q *queue) (Result, *requestError) {
	if !config.checkAuth(w, r) {
		return Result{}, unauthorized
	}
	ticket, err := q.admit(r.Context())
	if err != nil {
		if errors.Is(err, errQueueFull) {
			w.Header().Set("Retry-After", "1")
			if r.ProtoMajor == 1 {
				w.Header().Set("Connection", "close")
			}
			return Result{}, busy
		}
		return Result{}, unavailable
	}
	defer q.release(ticket)
	r.Body = http.MaxBytesReader(w, r.Body, config.MaxBytes+formOverhead)
	input, problem := parseInput(r, config)
	if problem != nil {
		return Result{}, problem
	}
	if err := ticket.wait(r.Context()); err != nil {
		return Result{}, unavailable
	}
	result, err := engine.Transcribe(r.Context(), input)
	switch {
	case errors.Is(err, ErrTooLarge) || result.Duration > float64(config.MaxSeconds):
		return Result{}, tooLarge
	case errors.Is(err, ErrBadLanguage):
		return Result{}, badRequest
	case errors.Is(err, ErrUnsupported):
		return Result{}, unsupported
	case errors.Is(err, ErrNoSpeech):
		return Result{}, noSpeech
	case err != nil:
		return Result{}, unavailable
	default:
		if result.Segments == nil {
			result.Segments = []Segment{}
		}
		return result, nil
	}
}

func authorized(header string, tokens [][sha256.Size]byte) bool {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return false
	}
	hash := sha256.Sum256([]byte(fields[1]))
	match := 0
	for _, token := range tokens {
		match |= subtle.ConstantTimeCompare(hash[:], token[:])
	}
	return match == 1
}

func parseInput(r *http.Request, config Config) (Input, *requestError) {
	input := Input{MaxSeconds: config.MaxSeconds}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return input, badRequest
	}
	form, err := r.MultipartReader()
	if err != nil {
		return input, badRequest
	}
	seen := make(map[string]bool)
	for {
		part, err := form.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return input, readError(err)
		}

		name := part.FormName()
		switch name {
		case "file", "language", "prompt":
			if seen[name] {
				return input, badRequest
			}
			seen[name] = true
		default:
			continue
		}

		limit := int64(maxFieldBytes)
		if name == "file" {
			limit = config.MaxBytes
		}
		value, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil {
			return input, readError(err)
		}
		if int64(len(value)) > limit {
			if name == "file" {
				return input, tooLarge
			}
			return input, badRequest
		}
		switch name {
		case "file":
			input.Audio = value
			input.ContentType = part.Header.Get("Content-Type")
			if input.ContentType == "" {
				input.ContentType = http.DetectContentType(value)
			}
		case "language":
			if !validLanguage(string(value)) {
				return input, badRequest
			}
			input.Language = string(value)
		case "prompt":
			if !utf8.Valid(value) || utf8.RuneCount(value) > 200 {
				return input, badRequest
			}
			input.Prompt = string(value)
		}
	}
	if len(input.Audio) == 0 {
		return input, badRequest
	}
	// Account for any multipart epilogue when enforcing the request body limit.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return input, readError(err)
	}
	return input, nil
}

const iso639Codes = " aa ab ae af ak am an ar as av ay az ba be bg bh bi bm bn bo br bs ca ce ch co cr cs cu cv cy da de dv dz ee el en eo es et eu fa ff fi fj fo fr fy ga gd gl gn gu gv ha he hi ho hr ht hu hy hz ia id ie ig ii ik io is it iu ja jv ka kg ki kj kk kl km kn ko kr ks ku kv kw ky la lb lg li ln lo lt lu lv mg mh mi mk ml mn mr ms mt my na nb nd ne ng nl nn no nr nv ny oc oj om or os pa pi pl ps pt qu rm rn ro ru rw sa sc sd se sg si sk sl sm sn so sq sr ss st su sv sw ta te tg th ti tk tl tn to tr ts tt tw ty ug uk ur uz ve vi vo wa wo xh yi yo za zh zu "

func ISO639Languages() []string {
	return strings.Fields(iso639Codes)
}

func validLanguage(language string) bool {
	return len(language) == 2 && strings.Contains(iso639Codes, " "+language+" ")
}

type requestError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

var (
	badRequest   = &requestError{400, "bad_request", "Invalid multipart request, file, language, or prompt."}
	unauthorized = &requestError{401, "unauthorized", "A valid bearer token is required."}
	tooLarge     = &requestError{413, "too_large", "Audio exceeds a configured limit."}
	unsupported  = &requestError{415, "unsupported_media", "Audio could not be decoded."}
	noSpeech     = &requestError{422, "no_speech", "Audio contains no speech."}
	busy         = &requestError{503, "busy", "The transcription queue is full."}
	unavailable  = &requestError{503, "unavailable", "Transcription engine unavailable."}
)

func readError(err error) *requestError {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return tooLarge
	}
	return badRequest
}

func writeError(w http.ResponseWriter, problem *requestError) {
	writeJSON(w, problem.Status, struct {
		Error *requestError `json:"error"`
	}{problem})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
