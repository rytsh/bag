package semantic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rakunlabs/ok"
)

// FallbackPrompt is the Whisper prompt used when the corpus has no god nodes.
const FallbackPrompt = "Use proper punctuation and paragraph breaks."

// DefaultTranscribeModel is the OpenAI transcription model.
const DefaultTranscribeModel = "whisper-1"

// defaultMaxUpload is OpenAI's /audio/transcriptions upload limit.
const defaultMaxUpload = 25 << 20

// apiAudioExt lists containers the OpenAI transcription endpoint accepts
// directly; anything else needs ffmpeg.
var apiAudioExt = map[string]bool{
	".flac": true, ".m4a": true, ".mp3": true, ".mp4": true, ".mpeg": true,
	".mpga": true, ".oga": true, ".ogg": true, ".wav": true, ".webm": true,
}

// TranscribeConfig configures the speech-to-text backend. Any endpoint
// implementing OpenAI's POST /audio/transcriptions works (OpenAI, Groq,
// LocalAI, faster-whisper-server/speaches, vLLM...).
type TranscribeConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	// Language is an optional ISO-639-1 hint.
	Language string
	// MaxUpload is the per-request size limit in bytes (default 25 MiB).
	MaxUpload int64
	// FFmpeg is the ffmpeg binary used to re-encode unsupported or oversized
	// media ("" = look up on PATH, "-" = never use ffmpeg).
	FFmpeg  string
	Timeout time.Duration
	// OutDir receives <stem>.txt transcripts (graphify-out/transcripts).
	OutDir string
	Force  bool
}

// Transcriber turns video/audio files into text transcripts.
type Transcriber struct {
	cfg  TranscribeConfig
	http *ok.Client
}

// NewTranscriber creates a transcriber. BaseURL defaults to OpenAI.
func NewTranscriber(cfg TranscribeConfig) (*Transcriber, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}

	if cfg.Model == "" {
		cfg.Model = DefaultTranscribeModel
	}

	if cfg.MaxUpload <= 0 {
		cfg.MaxUpload = defaultMaxUpload
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}

	if cfg.OutDir == "" {
		return nil, errors.New("transcript output directory is required")
	}

	opts := []ok.OptionClientFn{
		ok.WithBaseURL(strings.TrimRight(cfg.BaseURL, "/") + "/"),
		ok.WithTimeout(cfg.Timeout),
	}

	if cfg.APIKey != "" {
		opts = append(opts, ok.WithHeaderSet("Authorization", "Bearer "+cfg.APIKey))
	}

	c, err := ok.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create transcription client; %w", err)
	}

	return &Transcriber{cfg: cfg, http: c}, nil
}

// BuildWhisperPrompt builds a domain hint from the corpus god-node labels.
// GRAPHIFY_WHISPER_PROMPT overrides it.
//
// Adapted from Graphify's transcribe.build_whisper_prompt (Apache-2.0).
func BuildWhisperPrompt(godLabels []string) string {
	if len(godLabels) == 0 {
		return FallbackPrompt
	}

	if o := os.Getenv("GRAPHIFY_WHISPER_PROMPT"); o != "" {
		return o
	}

	var labels []string

	for _, l := range godLabels[:min(len(godLabels), 10)] {
		if l != "" {
			labels = append(labels, l)
		}
	}

	if len(labels) == 0 {
		return FallbackPrompt
	}

	return "Technical discussion about " + strings.Join(labels[:min(len(labels), 5)], ", ") +
		". Use proper punctuation and paragraph breaks."
}

// TranscribeAll transcribes every file and returns the transcript paths.
// Failures are logged and skipped.
//
// Adapted from Graphify's transcribe.transcribe_all (Apache-2.0).
func (t *Transcriber) TranscribeAll(ctx context.Context, files []string, prompt string) []string {
	var out []string

	for _, f := range files {
		p, err := t.Transcribe(ctx, f, prompt)
		if err != nil {
			slog.Warn("could not transcribe", "file", f, "error", err)

			continue
		}

		out = append(out, p)
	}

	return out
}

// Transcribe writes <OutDir>/<stem>.txt for path and returns its location.
// An existing transcript is reused unless Force is set.
//
// Adapted from Graphify's transcribe.transcribe (Apache-2.0).
func (t *Transcriber) Transcribe(ctx context.Context, path, prompt string) (string, error) {
	if err := os.MkdirAll(t.cfg.OutDir, 0o755); err != nil {
		return "", fmt.Errorf("create transcript dir; %w", err)
	}

	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	dst := filepath.Join(t.cfg.OutDir, stem+".txt")

	if _, err := os.Stat(dst); err == nil && !t.cfg.Force {
		return dst, nil
	}

	if prompt == "" {
		prompt = FallbackPrompt
	}

	parts, cleanup, err := t.prepare(ctx, path)
	if err != nil {
		return "", err
	}
	defer cleanup()

	slog.Info("transcribing", "file", filepath.Base(path), "model", t.cfg.Model, "parts", len(parts))

	var lines []string

	for _, p := range parts {
		text, err := t.request(ctx, p, prompt)
		if err != nil {
			return "", err
		}

		for _, l := range strings.Split(text, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
	}

	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return "", fmt.Errorf("write transcript; %w", err)
	}

	slog.Info("transcript saved", "path", dst, "segments", len(lines))

	return dst, nil
}

// prepare returns the files to upload. Media the API accepts as-is within
// the size limit is sent directly; otherwise ffmpeg re-encodes it to mono
// 16 kHz MP3 split into segments that fit the limit.
func (t *Transcriber) prepare(ctx context.Context, path string) ([]string, func(), error) {
	noop := func() {}

	st, err := os.Stat(path)
	if err != nil {
		return nil, noop, fmt.Errorf("stat media; %w", err)
	}

	ext := strings.ToLower(filepath.Ext(path))
	if apiAudioExt[ext] && st.Size() <= t.cfg.MaxUpload {
		return []string{path}, noop, nil
	}

	ffmpeg := t.ffmpeg()
	if ffmpeg == "" {
		if !apiAudioExt[ext] {
			return nil, noop, fmt.Errorf("%s is not accepted by the transcription API and ffmpeg is not available", ext)
		}

		return nil, noop, fmt.Errorf("file is %d bytes (limit %d) and ffmpeg is not available to split it", st.Size(), t.cfg.MaxUpload)
	}

	tmp, err := os.MkdirTemp("", "bag-audio-*")
	if err != nil {
		return nil, noop, fmt.Errorf("create temp dir; %w", err)
	}

	cleanup := func() { _ = os.RemoveAll(tmp) }

	// 32 kbit/s ≈ 4 KB/s; keep each segment at ~80% of the upload limit.
	segSeconds := max(int(float64(t.cfg.MaxUpload)*0.8/4000), 60)

	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-i", path, "-vn", "-ac", "1", "-ar", "16000", "-b:a", "32k",
		"-f", "segment", "-segment_time", fmt.Sprint(segSeconds), "-reset_timestamps", "1",
		filepath.Join(tmp, "part_%04d.mp3"))

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		cleanup()

		return nil, noop, fmt.Errorf("ffmpeg; %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	parts, _ := filepath.Glob(filepath.Join(tmp, "part_*.mp3"))
	sort.Strings(parts)

	if len(parts) == 0 {
		cleanup()

		return nil, noop, errors.New("ffmpeg produced no audio (file has no audio stream?)")
	}

	return parts, cleanup, nil
}

func (t *Transcriber) ffmpeg() string {
	switch t.cfg.FFmpeg {
	case "-":
		return ""
	case "":
		p, err := exec.LookPath("ffmpeg")
		if err != nil {
			return ""
		}

		return p
	}

	return t.cfg.FFmpeg
}

type transcriptionResponse struct {
	Text string `json:"text"`
}

func (t *Transcriber) request(ctx context.Context, path, prompt string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open media; %w", err)
	}
	defer f.Close()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(fw, f); err != nil {
		return "", fmt.Errorf("read media; %w", err)
	}

	fields := [][2]string{{"model", t.cfg.Model}, {"prompt", prompt}, {"response_format", "json"}}
	if t.cfg.Language != "" {
		fields = append(fields, [2]string{"language", t.cfg.Language})
	}

	for _, kv := range fields {
		if err := mw.WriteField(kv[0], kv[1]); err != nil {
			return "", err
		}
	}

	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "audio/transcriptions", &body)
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", mw.FormDataContentType())

	var resp transcriptionResponse
	if err := t.http.Do(req, ok.ResponseFuncJSON(&resp)); err != nil {
		return "", fmt.Errorf("transcription request; %w", err)
	}

	return resp.Text, nil
}
