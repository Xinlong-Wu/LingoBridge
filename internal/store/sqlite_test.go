package store

import (
	"errors"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	st, err := Open()
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	return st
}

func TestCreateSessionDuplicateName(t *testing.T) {
	st := openTestStore(t)

	if _, err := st.CreateSession("user", "work"); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	_, err := st.CreateSession("user", "work")
	if !errors.Is(err, ErrSessionExists) {
		t.Fatalf("CreateSession duplicate error = %v, want ErrSessionExists", err)
	}
}

func TestSwitchSessionNotFound(t *testing.T) {
	st := openTestStore(t)

	if _, err := st.CreateSession("user", "work"); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	_, err := st.SwitchSession("user", "missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SwitchSession error = %v, want ErrSessionNotFound", err)
	}
}
