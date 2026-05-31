package session

import (
	"testing"

	"wechatbox/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	st, err := store.Open()
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

func TestClearSessionArchivesCurrentAndCreatesNew(t *testing.T) {
	manager, _ := newTestManager(t)

	first, err := manager.GetOrCreateActiveSession("user")
	if err != nil {
		t.Fatalf("GetOrCreateActiveSession returned error: %v", err)
	}

	next, err := manager.ClearSession("user")
	if err != nil {
		t.Fatalf("ClearSession returned error: %v", err)
	}
	if next.ID == first.ID {
		t.Fatal("ClearSession returned the original session")
	}

	sessions, err := manager.ListSessions("user")
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}

	activeCount := 0
	for _, sess := range sessions {
		if sess.Active {
			activeCount++
			if sess.ID != next.ID {
				t.Fatalf("active session = %s, want %s", sess.ID, next.ID)
			}
		}
	}
	if activeCount != 1 {
		t.Fatalf("active session count = %d, want 1", activeCount)
	}
}
