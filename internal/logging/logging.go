package logging

import (
	"fmt"
	"log"
	"strings"
)

var shared = log.Default()

// Logger is a component-prefixed view over the shared process logger.
type Logger struct {
	component string
	base      *log.Logger
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

// Printf logs a formatted component-prefixed message.
func (l Logger) Printf(format string, args ...any) {
	l.output(fmt.Sprintf(format, args...))
}

// Println logs a component-prefixed message.
func (l Logger) Println(args ...any) {
	l.output(fmt.Sprintln(args...))
}

func (l Logger) output(message string) {
	base := l.base
	if base == nil {
		base = shared
	}
	message = strings.TrimRight(message, "\n")
	if l.component != "" {
		message = "[" + l.component + "] " + message
	}
	_ = base.Output(3, message)
}
