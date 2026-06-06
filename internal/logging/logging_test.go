package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerPrefixesSharedOutput(t *testing.T) {
	buf := captureLogs(t)
	SetLevel(Info)

	For("[core]").Info("hello %s", "world")

	got := strings.TrimSpace(buf.String())
	if got != "[core] [info] hello world" {
		t.Fatalf("unexpected log output: %q", got)
	}
}

func TestDefaultLevelIsInfo(t *testing.T) {
	SetLevel(Info)
	if got := GetLevel(); got != Info {
		t.Fatalf("GetLevel = %v, want info", got)
	}
}

func TestLevelFiltering(t *testing.T) {
	buf := captureLogs(t)
	log := For("test")

	SetLevel(Info)
	log.Debug("hidden")
	log.Info("visible")
	if got := buf.String(); strings.Contains(got, "hidden") || !strings.Contains(got, "[info] visible") {
		t.Fatalf("info filtering output = %q", got)
	}

	buf.Reset()
	SetLevel(Error)
	log.Warn("hidden")
	log.Error("visible")
	if got := buf.String(); strings.Contains(got, "hidden") || !strings.Contains(got, "[error] visible") {
		t.Fatalf("error filtering output = %q", got)
	}
}

func TestParseLevel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Level
	}{
		{in: "debug", want: Debug},
		{in: "INFO", want: Info},
		{in: "warn", want: Warn},
		{in: "error", want: Error},
	} {
		got, err := ParseLevel(tc.in)
		if err != nil {
			t.Fatalf("ParseLevel(%q) returned error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	if _, err := ParseLevel("noisy"); err == nil {
		t.Fatal("ParseLevel returned nil error for invalid level")
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	base := Shared()
	originalWriter := base.Writer()
	originalFlags := base.Flags()
	originalPrefix := base.Prefix()
	originalLevel := GetLevel()
	t.Cleanup(func() {
		base.SetOutput(originalWriter)
		base.SetFlags(originalFlags)
		base.SetPrefix(originalPrefix)
		SetLevel(originalLevel)
	})

	var buf bytes.Buffer
	base.SetOutput(&buf)
	base.SetFlags(0)
	base.SetPrefix("")
	return &buf
}
