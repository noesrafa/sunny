package logger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/log/v2"
)

// maxLogBytes is the rotation threshold. When sunnytui.log grows past this,
// Setup() moves it to sunnytui.log.1 (overwriting any existing backup) and
// starts fresh. One generation of backup is plenty for post-mortem debugging
// and caps disk usage at ~2× this value.
const maxLogBytes = 5 * 1024 * 1024 // 5 MB

// Setup opens (or creates) ~/.sunnytui/sunnytui.log for append and returns a
// configured logger plus an io.Closer for the underlying file. Failures fall
// back to a logger writing to io.Discard so callers can keep going.
//
// Default log level is Info — Debug used to be the default and was the main
// reason the file grew unbounded. Set SUNNYTUI_LOG_LEVEL=debug (or warn /
// error) to override at startup.
func Setup(prefix string) (*log.Logger, io.Closer) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Discard(prefix), noopCloser{}
	}
	dir := filepath.Join(home, ".sunnytui")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Discard(prefix), noopCloser{}
	}
	logPath := filepath.Join(dir, "sunnytui.log")
	rotateIfBig(logPath)
	f, err := os.OpenFile(logPath,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Discard(prefix), noopCloser{}
	}
	logger := log.NewWithOptions(f, log.Options{
		ReportTimestamp: true,
		TimeFormat:      time.RFC3339Nano,
		Level:           levelFromEnv(),
		Prefix:          prefix,
	})
	logger.Info("logger started", "pid", os.Getpid())
	return logger, f
}

// rotateIfBig moves logPath → logPath+".1" when the current log is over the
// threshold. Best-effort: any error here is silent because the next OpenFile
// call will succeed regardless and we'd rather lose old logs than fail to
// boot the TUI.
func rotateIfBig(logPath string) {
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < maxLogBytes {
		return
	}
	_ = os.Rename(logPath, logPath+".1")
}

// levelFromEnv reads SUNNYTUI_LOG_LEVEL ("debug" / "info" / "warn" / "error").
// Anything else (including empty) yields Info.
func levelFromEnv() log.Level {
	switch strings.ToLower(os.Getenv("SUNNYTUI_LOG_LEVEL")) {
	case "debug":
		return log.DebugLevel
	case "warn":
		return log.WarnLevel
	case "error":
		return log.ErrorLevel
	default:
		return log.InfoLevel
	}
}

func LogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".sunnytui", "sunnytui.log")
}

// Discard returns a no-op logger that writes to io.Discard. Use it as a
// non-nil default so callers can drop the `if logger != nil` checks.
func Discard(prefix string) *log.Logger {
	return log.NewWithOptions(io.Discard, log.Options{Prefix: prefix})
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }
