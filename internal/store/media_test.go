package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lingobridge/internal/config"
)

func TestSaveMediaFileSeparatesUserAndSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := SaveMediaFile("user@im.wechat", "session/one", "user", 0, "image/png", []byte("first"))
	if err != nil {
		t.Fatalf("SaveMediaFile first returned error: %v", err)
	}
	second, err := SaveMediaFile("user@im.wechat", "session/two", "assistant", 1, "image/jpeg", []byte("second"))
	if err != nil {
		t.Fatalf("SaveMediaFile second returned error: %v", err)
	}

	if first.RelativePath == second.RelativePath {
		t.Fatalf("relative paths are equal: %q", first.RelativePath)
	}
	if !strings.HasPrefix(first.RelativePath, "media/user_im.wechat-") {
		t.Fatalf("first relative path = %q, want media/user_im.wechat-*", first.RelativePath)
	}
	if !strings.Contains(first.RelativePath, "/session_one-") {
		t.Fatalf("first relative path = %q, want safe session component", first.RelativePath)
	}
	if strings.Contains(first.RelativePath, "\\") || strings.Contains(first.RelativePath, "@") {
		t.Fatalf("first relative path is not portable/safe: %q", first.RelativePath)
	}
	if !strings.HasSuffix(first.RelativePath, ".png") {
		t.Fatalf("first relative path = %q, want .png", first.RelativePath)
	}
	if !strings.HasSuffix(second.RelativePath, ".jpg") {
		t.Fatalf("second relative path = %q, want .jpg", second.RelativePath)
	}

	dataDir, err := config.DataDir()
	if err != nil {
		t.Fatalf("DataDir returned error: %v", err)
	}
	firstData, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(first.RelativePath)))
	if err != nil {
		t.Fatalf("read first media file: %v", err)
	}
	if string(firstData) != "first" {
		t.Fatalf("first media file = %q, want first", firstData)
	}
}
