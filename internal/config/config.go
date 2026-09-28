// Package config loads bag configuration with chu.
package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/rakunlabs/chu"
	"github.com/rakunlabs/chu/loader"
	"github.com/rakunlabs/chu/loader/loaderenv"
	"github.com/rakunlabs/chu/loader/loaderfile"
	"github.com/rakunlabs/logi"
	"github.com/rakunlabs/logi/logadapter"
)

// ServiceName is the config name (bag.yaml / bag.toml / BAG_ env prefix).
var ServiceName = "bag"

// Config is the full bag configuration.
type Config struct {
	LogLevel string `cfg:"log_level" default:"info"`

	// OutDir is the output directory relative to the corpus root.
	OutDir string `cfg:"out_dir" default:"graphify-out"`
	// Workers is the AST extraction parallelism (0 = NumCPU).
	Workers int `cfg:"workers"`

	LLM        LLM        `cfg:"llm"`
	Transcribe Transcribe `cfg:"transcribe"`
	Server     Server     `cfg:"server"`
}

// Transcribe configures video/audio transcription for --semantic. Any
// OpenAI-compatible /audio/transcriptions endpoint works (OpenAI, Groq,
// LocalAI, speaches/faster-whisper-server...). BaseURL and APIKey fall back
// to the LLM settings.
type Transcribe struct {
	BaseURL  string `cfg:"base_url"`
	APIKey   string `cfg:"api_key" log:"-"`
	Model    string `cfg:"model" default:"whisper-1"`
	Language string `cfg:"language"`
	// MaxUploadMB is the per-request upload limit; larger files are split
	// with ffmpeg.
	MaxUploadMB int `cfg:"max_upload_mb" default:"25"`
	// FFmpeg is the ffmpeg binary ("" = PATH lookup, "-" = disabled).
	FFmpeg  string `cfg:"ffmpeg"`
	Timeout string `cfg:"timeout" default:"600s"`
}

// LLM configures the optional semantic extraction backend. Any
// OpenAI-compatible endpoint works (OpenAI, Ollama, vLLM, LM Studio...).
type LLM struct {
	BaseURL     string  `cfg:"base_url"`
	APIKey      string  `cfg:"api_key" log:"-"`
	Model       string  `cfg:"model"`
	Temperature float64 `cfg:"temperature"`
	TokenBudget int     `cfg:"token_budget" default:"60000"`
	Concurrency int     `cfg:"concurrency" default:"4"`
	Timeout     string  `cfg:"timeout" default:"600s"`
}

// Server configures `bag serve`.
type Server struct {
	Host   string `cfg:"host" default:"127.0.0.1"`
	Port   string `cfg:"port" default:"8080"`
	APIKey string `cfg:"api_key" log:"-"`
	Path   string `cfg:"path" default:"/mcp"`
}

// Load loads configuration from defaults, the first matching configuration
// file, and BAG_* environment variables. File lookup order is the current
// directory, os.UserConfigDir()/bag, /etc/bag, then /etc. CONFIG_FILE_BAG or
// CONFIG_FILE selects an explicit file before this lookup.
func Load(ctx context.Context) (*Config, error) {
	var cfg Config
	if err := chu.Load(ctx, ServiceName, &cfg,
		chu.WithLoaderOption(loaderfile.New(loaderfile.WithFolders(configFolders()...))),
		chu.WithLoaderOption(loaderenv.New(loaderenv.WithPrefix("BAG_"))),
		chu.WithDisableLoader(loader.NameHTTP),
		chu.WithLogger(logadapter.Noop{}),
	); err != nil {
		return nil, fmt.Errorf("load config; %w", err)
	}

	if err := logi.SetLogLevel(cfg.LogLevel); err != nil {
		return nil, fmt.Errorf("set log level %s; %w", cfg.LogLevel, err)
	}

	slog.Debug("loaded configuration", "config", chu.MarshalMap(cfg))

	return &cfg, nil
}

func configFolders() []string {
	folders := make([]string, 0, 3)
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		folders = append(folders, filepath.Join(dir, ServiceName))
	}

	// Keep /etc/bag as the conventional app directory and /etc itself for
	// compatibility with chu's previous /etc/bag.yaml lookup.
	return append(folders, filepath.Join(string(filepath.Separator), "etc", ServiceName), filepath.Join(string(filepath.Separator), "etc"))
}

type ctxKey struct{}

// WithContext stores cfg in ctx.
func WithContext(ctx context.Context, cfg *Config) context.Context {
	return context.WithValue(ctx, ctxKey{}, cfg)
}

// From returns the config stored in ctx (or defaults).
func From(ctx context.Context) *Config {
	if c, ok := ctx.Value(ctxKey{}).(*Config); ok {
		return c
	}

	return &Config{OutDir: "graphify-out"}
}
