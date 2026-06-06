package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestReloadHandlerTriggersReload(t *testing.T) {
	var reloads int32
	handler := newHandler(func(context.Context) error {
		atomic.AddInt32(&reloads, 1)
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := atomic.LoadInt32(&reloads); got != 1 {
		t.Fatalf("reload count = %d, want 1", got)
	}
}

func TestReloadHandlerRejectsNonPost(t *testing.T) {
	handler := newHandler(func(context.Context) error {
		t.Fatal("reload should not be called")
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/reload", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestNotifyReloadMissingSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	err := NotifyReloadAt(context.Background(), socketPath)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NotifyReloadAt error = %v, want ErrUnavailable", err)
	}
}

func TestPrepareSocketDetectsActiveSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "lingobridge.sock")
	if err := os.WriteFile(socketPath, []byte("socket placeholder"), 0600); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}

	original := socketActive
	socketActive = func(string) bool { return true }
	t.Cleanup(func() { socketActive = original })

	err := prepareSocket(socketPath)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("prepareSocket error = %v, want ErrAlreadyRunning", err)
	}
}

func TestPrepareSocketRemovesStaleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "lingobridge.sock")
	if err := os.WriteFile(socketPath, []byte("stale"), 0600); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}

	original := socketActive
	socketActive = func(string) bool { return false }
	t.Cleanup(func() { socketActive = original })

	if err := prepareSocket(socketPath); err != nil {
		t.Fatalf("prepareSocket returned error: %v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket placeholder still exists, stat err=%v", err)
	}
}
