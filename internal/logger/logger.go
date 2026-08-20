// Package logger provides structured logging setup for OpsPulse.
package logger

import (
	"log/slog"
	"os"
)

// Setup initializes the default logger with the specified debug mode.
func Setup(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})
	slog.SetDefault(slog.New(handler))
}
