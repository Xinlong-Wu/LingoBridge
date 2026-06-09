package session

import (
	"testing"

	"lingobridge/internal/config"
	"lingobridge/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	st, err := store.Open(store.PlatformWeChat)
	if err != nil {
		t.Fatalf("store.Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	return NewManager(st), st
}

func TestArchiveCurrentSessionSelectsFallback(t *testing.T) {
	manager, _ := newTestManager(t)

	if _, err := manager.CreateSession("user", "work"); err != nil {
		t.Fatalf("CreateSession work returned error: %v", err)
	}
	if _, err := manager.CreateSession("user", "play"); err != nil {
		t.Fatalf("CreateSession play returned error: %v", err)
	}

	result, err := manager.ArchiveSession("user", "")
	if err != nil {
		t.Fatalf("ArchiveSession returned error: %v", err)
	}
	if result.Archived.Name != "play" {
		t.Fatalf("archived session = %s, want play", result.Archived.Name)
	}
	if !result.CurrentChanged || result.Current == nil || result.Current.Name != "work" {
		t.Fatalf("archive result = %#v, want fallback to work", result)
	}
}

func TestCurrentModelFallsBackToDefault(t *testing.T) {
	_, st := newTestManager(t)
	manager := NewManager(st, config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {},
			"gpt4o":    {},
		},
	})
	if err := st.SetUserModelName("user", "missing"); err != nil {
		t.Fatalf("SetUserModelName returned error: %v", err)
	}

	model, err := manager.CurrentModel("user")
	if err != nil {
		t.Fatalf("CurrentModel returned error: %v", err)
	}
	if model != "deepseek" {
		t.Fatalf("CurrentModel = %q, want deepseek", model)
	}
}
