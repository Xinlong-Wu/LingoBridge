package commands

import (
	"fmt"
	"strings"
	"testing"

	"wechatbox/internal/store"
)

type fakeSessionManager struct {
	createErr error
	switchErr error
	sessions  []store.Session
}

func (f *fakeSessionManager) CreateSession(userID, name string) (*store.Session, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &store.Session{ID: "new", UserID: userID, Name: name, Active: true}, nil
}

func (f *fakeSessionManager) ListSessions(userID string) ([]store.Session, error) {
	return f.sessions, nil
}

func (f *fakeSessionManager) SwitchSession(userID, sessionName string) (*store.Session, error) {
	if f.switchErr != nil {
		return nil, f.switchErr
	}
	return &store.Session{ID: "switched", UserID: userID, Name: sessionName, Active: true}, nil
}

func (f *fakeSessionManager) ClearSession(userID string) (*store.Session, error) {
	return &store.Session{ID: "cleared", UserID: userID, Name: "session-1", Active: true}, nil
}

func TestHandleNewDuplicateSession(t *testing.T) {
	manager := &fakeSessionManager{
		createErr: fmt.Errorf("%w: work", store.ErrSessionExists),
	}

	resp, handled, err := Handle("/new work", "user", manager)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /new")
	}
	if !strings.Contains(resp, "已存在") {
		t.Fatalf("response = %q, want duplicate message", resp)
	}
}

func TestHandleHelp(t *testing.T) {
	resp, handled, err := Handle("/help", "user", &fakeSessionManager{})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /help")
	}
	for _, want := range []string{"/help", "/new", "/list", "/switch", "/clear"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("response = %q, want %s", resp, want)
		}
	}
}

func TestHandleSwitchMissingSession(t *testing.T) {
	manager := &fakeSessionManager{
		switchErr: fmt.Errorf("%w: missing", store.ErrSessionNotFound),
	}

	resp, handled, err := Handle("/switch missing", "user", manager)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /switch")
	}
	if !strings.Contains(resp, "不存在") {
		t.Fatalf("response = %q, want not found message", resp)
	}
}

func TestHandleListSessions(t *testing.T) {
	manager := &fakeSessionManager{
		sessions: []store.Session{{ID: "1", UserID: "user", Name: "default", Active: true}},
	}

	resp, handled, err := Handle("/list", "user", manager)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /list")
	}
	if !strings.Contains(resp, "default") {
		t.Fatalf("response = %q, want session name", resp)
	}
}
