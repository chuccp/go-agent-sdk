package log

import (
	"log/slog"
	"os"
	"sync"
)

// Logger is the logging interface. Implementations can wrap any logging
// library (zap, logrus, slog, zerolog, etc.).
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

var (
	mu            sync.RWMutex
	defaultLogger Logger = newSlogLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
)

// slogLogger wraps *slog.Logger to implement Logger.
type slogLogger struct {
	l *slog.Logger
}

func newSlogLogger(l *slog.Logger) *slogLogger { return &slogLogger{l: l} }
func (s *slogLogger) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }
func (s *slogLogger) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s *slogLogger) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s *slogLogger) Error(msg string, args ...any) { s.l.Error(msg, args...) }

// SetLogger replaces the global logger. Pass nil to restore the default slog logger.
func SetLogger(l Logger) {
	mu.Lock()
	defer mu.Unlock()
	if l == nil {
		defaultLogger = newSlogLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	} else {
		defaultLogger = l
	}
}

// GetLogger returns the current global logger.
func GetLogger() Logger {
	mu.RLock()
	defer mu.RUnlock()
	return defaultLogger
}

// Debug logs a debug-level message.
func Debug(msg string, args ...any) {
	mu.RLock()
	l := defaultLogger
	mu.RUnlock()
	l.Debug(msg, args...)
}

// Info logs an informational message.
func Info(msg string, args ...any) {
	mu.RLock()
	l := defaultLogger
	mu.RUnlock()
	l.Info(msg, args...)
}

// Warn logs a warning message.
func Warn(msg string, args ...any) {
	mu.RLock()
	l := defaultLogger
	mu.RUnlock()
	l.Warn(msg, args...)
}

// Error logs an error message.
func Error(msg string, args ...any) {
	mu.RLock()
	l := defaultLogger
	mu.RUnlock()
	l.Error(msg, args...)
}
