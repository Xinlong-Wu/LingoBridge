package logging

import (
	"fmt"
	"log"
	"strings"
	"sync/atomic"
)

var shared = log.Default()

// Level controls which log messages are emitted.
type Level int32

const (
	Debug Level = iota + 1
	Info
	Warn
	Error
)

var currentLevel atomic.Int32

// Logger is a component-prefixed view over the shared process logger.
type Logger struct {
	component string
	base      *log.Logger
}

func init() {
	currentLevel.Store(int32(Info))
}

// ParseLevel parses a user-facing log level.
func ParseLevel(level string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return Debug, nil
	case "info", "":
		return Info, nil
	case "warn":
		return Warn, nil
	case "error":
		return Error, nil
	default:
		return Info, fmt.Errorf("unknown log level %q", level)
	}
}

// SetLevel sets the process-wide log threshold.
func SetLevel(level Level) {
	if level < Debug || level > Error {
		level = Info
	}
	currentLevel.Store(int32(level))
}

// GetLevel returns the process-wide log threshold.
func GetLevel() Level {
	level := Level(currentLevel.Load())
	if level < Debug || level > Error {
		return Info
	}
	return level
}

func (l Level) String() string {
	switch l {
	case Debug:
		return "debug"
	case Info:
		return "info"
	case Warn:
		return "warn"
	case Error:
		return "error"
	default:
		return "info"
	}
}

// For returns a logger instance for one component, platform, or provider.
func For(component string) Logger {
	return Logger{
		component: strings.Trim(strings.TrimSpace(component), "[]"),
		base:      shared,
	}
}

// Shared returns the underlying process-wide logger used by every component logger.
func Shared() *log.Logger {
	return shared
}

// Debug logs a debug message.
func (l Logger) Debug(format string, args ...any) {
	l.output(Debug, fmt.Sprintf(format, args...))
}

// Info logs an informational message.
func (l Logger) Info(format string, args ...any) {
	l.output(Info, fmt.Sprintf(format, args...))
}

// Warn logs a warning message.
func (l Logger) Warn(format string, args ...any) {
	l.output(Warn, fmt.Sprintf(format, args...))
}

// Error logs an error message.
func (l Logger) Error(format string, args ...any) {
	l.output(Error, fmt.Sprintf(format, args...))
}

func (l Logger) output(level Level, message string) {
	if level < GetLevel() {
		return
	}
	base := l.base
	if base == nil {
		base = shared
	}
	message = strings.TrimRight(message, "\n")
	message = "[" + level.String() + "] " + message
	if l.component != "" {
		message = "[" + l.component + "] " + message
	}
	_ = base.Output(3, message)
}
