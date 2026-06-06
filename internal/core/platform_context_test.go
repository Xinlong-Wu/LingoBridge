package core

import (
	"strings"
	"testing"

	"lingobridge/internal/config"
	"lingobridge/internal/store"
)

func TestPlatformContextScopesConfigAccess(t *testing.T) {
	cfg := config.DefaultConfig()
	saved := false
	ctx, err := NewPlatformContext(store.PlatformFeishu, &cfg, nil, func(config.Config) error {
		saved = true
		return nil
	})
	if err != nil {
		t.Fatalf("NewPlatformContext returned error: %v", err)
	}

	if err := ctx.SetPlatformConfig(store.PlatformWeChat, map[string]string{"x": "y"}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("SetPlatformConfig cross-platform error = %v, want access denied", err)
	}
	if err := ctx.SetPlatformConfig(store.PlatformFeishu, map[string]string{"x": "y"}); err != nil {
		t.Fatalf("SetPlatformConfig returned error: %v", err)
	}
	if !saved {
		t.Fatal("SetPlatformConfig did not call save")
	}

	var out map[string]string
	if err := ctx.GetPlatformConfig(store.PlatformFeishu, &out); err != nil {
		t.Fatalf("GetPlatformConfig returned error: %v", err)
	}
	if out["x"] != "y" {
		t.Fatalf("platform config = %#v", out)
	}
}
