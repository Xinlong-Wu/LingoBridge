package logging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

var shared = log.Default()

// Level controls which log messages are emitted.
type Level int32

const (
	All Level = iota
	Debug
	Info
	Warn
	Error
)

var currentLevel atomic.Int32

// Logger is the shared logging interface used by LingoBridge and SDK adapters.
type Logger interface {
	Debug(context.Context, ...interface{})
	Info(context.Context, ...interface{})
	Warn(context.Context, ...interface{})
	Error(context.Context, ...interface{})
}

// componentLogger is a component-prefixed view over the shared process logger.
type componentLogger struct {
	component string
	base      *log.Logger
}

func init() {
	currentLevel.Store(int32(Info))
}

// ParseLevel parses a user-facing log level.
func ParseLevel(level string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "all":
		return All, nil
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
	if level < All || level > Error {
		level = Info
	}
	currentLevel.Store(int32(level))
}

// GetLevel returns the process-wide log threshold.
func GetLevel() Level {
	level := Level(currentLevel.Load())
	if level < All || level > Error {
		return Info
	}
	return level
}

func (l Level) String() string {
	switch l {
	case All:
		return "all"
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
	return componentLogger{
		component: strings.Trim(strings.TrimSpace(component), "[]"),
		base:      shared,
	}
}

// Shared returns the underlying process-wide logger used by every component logger.
func Shared() *log.Logger {
	return shared
}

// Debug logs a debug message.
func (l componentLogger) Debug(ctx context.Context, args ...interface{}) {
	l.output(Debug, formatArgs(args...))
}

// Info logs an informational message.
func (l componentLogger) Info(ctx context.Context, args ...interface{}) {
	l.output(Info, formatArgs(args...))
}

// Warn logs a warning message.
func (l componentLogger) Warn(ctx context.Context, args ...interface{}) {
	l.output(Warn, formatArgs(args...))
}

// Error logs an error message.
func (l componentLogger) Error(ctx context.Context, args ...interface{}) {
	l.output(Error, formatArgs(args...))
}

func (l componentLogger) output(level Level, message string) {
	if level < GetLevel() {
		return
	}
	base := l.base
	if base == nil {
		base = shared
	}
	message = strings.TrimRight(message, "\n")
	component := l.component
	if component == "" {
		component = "app"
	}
	message = fmt.Sprintf("%s - [%s] - [%s] %s", time.Now().Format(time.RFC3339), strings.ToUpper(level.String()), component, message)
	_ = base.Output(3, message)
}

func formatArgs(args ...interface{}) string {
	if len(args) == 0 {
		return ""
	}
	if format, ok := args[0].(string); ok && len(args) > 1 && hasFormatVerb(format) {
		return fmt.Sprintf(format, args[1:]...)
	}
	return strings.TrimRight(fmt.Sprintln(args...), "\n")
}

func hasFormatVerb(format string) bool {
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		if i >= len(format) {
			return false
		}
		if format[i] == '%' {
			continue
		}
		return true
	}
	return false
}
