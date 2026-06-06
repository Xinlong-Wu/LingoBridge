package logging

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestLoggerPrefixesSharedOutput(t *testing.T) {
	buf := captureLogs(t)
	SetLevel(Info)

	For("[core]").Info(context.Background(), "hello %s", "world")

	got := strings.TrimSpace(buf.String())
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[^ ]+ - \[INFO\] - \[core\] hello world$`).MatchString(got) {
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

	SetLevel(All)
	log.Debug(context.Background(), "visible debug")
	if got := buf.String(); !strings.Contains(got, "[DEBUG] - [test] visible debug") {
		t.Fatalf("all filtering output = %q", got)
	}

	buf.Reset()
	SetLevel(Info)
	log.Debug(context.Background(), "hidden")
	log.Info(context.Background(), "visible")
	if got := buf.String(); strings.Contains(got, "hidden") || !strings.Contains(got, "[INFO] - [test] visible") {
		t.Fatalf("info filtering output = %q", got)
	}

	buf.Reset()
	SetLevel(Error)
	log.Warn(context.Background(), "hidden")
	log.Error(context.Background(), "visible")
	if got := buf.String(); strings.Contains(got, "hidden") || !strings.Contains(got, "[ERROR] - [test] visible") {
		t.Fatalf("error filtering output = %q", got)
	}
}

func TestParseLevel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Level
	}{
		{in: "all", want: All},
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

func TestLevelStringAll(t *testing.T) {
	if got := All.String(); got != "all" {
		t.Fatalf("All.String() = %q, want all", got)
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
