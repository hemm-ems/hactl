package main

import (
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/hemm-ems/hactl/internal/cmd"
)

func main() {
	configureLogger()

	if err := cmd.Execute(); err != nil {
		code := 1
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			code = ec.ExitCode()
		}
		os.Exit(code)
	}
}

// configureLogger sets the default slog level from HACTL_LOG_LEVEL.
// Accepts debug, info, warn, error (case-insensitive). Defaults to info.
func configureLogger() {
	lvl := slog.LevelInfo
	switch strings.ToLower(os.Getenv("HACTL_LOG_LEVEL")) {
	case "debug":
		lvl = slog.LevelDebug
	case "info", "":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
}
