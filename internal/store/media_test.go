package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveMediaFileSeparatesUserAndSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := Open(PlatformWeChat)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	first, err := st.SaveMediaFile("user@im.wechat", "session/one", "user", 0, "image/png", []byte("first"))
	if err != nil {
		t.Fatalf("SaveMediaFile first returned error: %v", err)
	}
	second, err := st.SaveMediaFile("user@im.wechat", "session/two", "assistant", 1, "image/jpeg", []byte("second"))
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

	firstData, err := os.ReadFile(filepath.Join(st.DataDir(), filepath.FromSlash(first.RelativePath)))
	if err != nil {
		t.Fatalf("read first media file: %v", err)
	}
	if string(firstData) != "first" {
		t.Fatalf("first media file = %q, want first", firstData)
	}

	feishu, err := Open(PlatformFeishu)
	if err != nil {
		t.Fatalf("Open feishu returned error: %v", err)
	}
	defer feishu.Close()
	if st.DataDir() == feishu.DataDir() {
		t.Fatalf("platform media data dirs should differ: %q", st.DataDir())
	}
}
