package logger

import (
	"agent-management/server/internal/config"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
)

// New creates a new slog.Logger based on the provided configuration.
func New(cfg config.LoggingConfig) *slog.Logger {
	var logWriter io.Writer

	// Set up file rotation if a directory is specified
	if cfg.Directory != "" {
		// Ensure the log directory exists
		if err := os.MkdirAll(cfg.Directory, 0755); err != nil {
			slog.Error("failed to create log directory, falling back to stdout", "directory", cfg.Directory, "error", err)
			logWriter = os.Stdout
		} else {
			logPattern := filepath.Join(cfg.Directory, cfg.FilePattern)
			writer, err := rotatelogs.New(
				logPattern,
				rotatelogs.WithMaxAge(time.Duration(cfg.MaxAgeHours)*time.Hour),
				rotatelogs.WithRotationTime(time.Hour),
			)
			if err != nil {
				// Fallback to stdout if rotation setup fails
				slog.Error("failed to initialize log rotation, falling back to stdout", "error", err)
				logWriter = os.Stdout
			} else {
				logWriter = writer
			}
		}
	} else {
		// Default to stdout if no directory is set
		logWriter = os.Stdout
	}

	// Set log level
	var level slog.Level
	switch cfg.LogLevel {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO":
		level = slog.LevelInfo
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	handler := slog.NewJSONHandler(logWriter, opts)
	return slog.New(handler)
}
