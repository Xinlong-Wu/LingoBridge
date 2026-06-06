package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerPrefixesSharedOutput(t *testing.T) {
	base := Shared()
	originalWriter := base.Writer()
	originalFlags := base.Flags()
	originalPrefix := base.Prefix()
	t.Cleanup(func() {
		base.SetOutput(originalWriter)
		base.SetFlags(originalFlags)
		base.SetPrefix(originalPrefix)
	})

	var buf bytes.Buffer
	base.SetOutput(&buf)
	base.SetFlags(0)
	base.SetPrefix("")

	For("[core]").Printf("hello %s", "world")

	got := strings.TrimSpace(buf.String())
	if got != "[core] hello world" {
		t.Fatalf("unexpected log output: %q", got)
	}
}
