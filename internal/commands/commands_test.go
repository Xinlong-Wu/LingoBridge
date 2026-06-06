package commands

import (
	"fmt"
	"strings"
	"testing"

	"lingobridge/internal/session"
	"lingobridge/internal/store"
)

type fakeSessionManager struct {
	createErr    error
	switchErr    error
	renameErr    error
	archiveErr   error
	setModelErr  error
	sessions     []store.Session
	currentModel string
	models       []string
}

func (f *fakeSessionManager) CurrentSession(userID string) (*store.Session, error) {
	return &store.Session{ID: "current", UserID: userID, Name: "default", Current: true}, nil
}

func (f *fakeSessionManager) CreateSession(userID, name string) (*store.Session, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &store.Session{ID: "new", UserID: userID, Name: name, Current: true}, nil
}

func (f *fakeSessionManager) ListSessions(userID string) ([]store.Session, error) {
	return f.sessions, nil
}

func (f *fakeSessionManager) SwitchSession(userID, sessionName string) (*store.Session, error) {
	if f.switchErr != nil {
		return nil, f.switchErr
	}
	return &store.Session{ID: "switched", UserID: userID, Name: sessionName, Current: true}, nil
}

func (f *fakeSessionManager) RenameCurrentSession(userID, newName string) (*store.Session, error) {
	if f.renameErr != nil {
		return nil, f.renameErr
	}
	return &store.Session{ID: "current", UserID: userID, Name: newName, Current: true}, nil
}

func (f *fakeSessionManager) ArchiveSession(userID, sessionName string) (*store.ArchiveResult, error) {
	if f.archiveErr != nil {
		return nil, f.archiveErr
	}
	return &store.ArchiveResult{
		Archived:       store.Session{ID: "current", UserID: userID, Name: "default", Archived: true},
		Current:        &store.Session{ID: "next", UserID: userID, Name: "next", Current: true},
		CurrentChanged: true,
	}, nil
}

func (f *fakeSessionManager) ClearSession(userID string) (*store.Session, error) {
	return &store.Session{ID: "cleared", UserID: userID, Name: "session-1", Current: true}, nil
}

func (f *fakeSessionManager) CurrentModel(userID string) (string, error) {
	if f.currentModel != "" {
		return f.currentModel, nil
	}
	return "deepseek", nil
}

func (f *fakeSessionManager) SetModel(userID, modelName string) error {
	if f.setModelErr != nil {
		return f.setModelErr
	}
	return nil
}

func (f *fakeSessionManager) DefaultModelName() string {
	return "deepseek"
}

func (f *fakeSessionManager) ListModels() []string {
	if len(f.models) > 0 {
		return f.models
	}
	return []string{"deepseek", "gpt4o"}
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
	for _, want := range []string{"/help", "/current", "/new", "/list", "/switch", "/rename", "/archive", "/clear", "/model"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("response = %q, want %s", resp, want)
		}
	}
}

func TestHandleWithPolicyDisabledCommand(t *testing.T) {
	manager := &fakeSessionManager{}
	resp, handled, err := HandleWithPolicy("/model", "user", manager, PolicyWithDisabled("/model"))
	if err != nil {
		t.Fatalf("HandleWithPolicy returned error: %v", err)
	}
	if !handled {
		t.Fatal("HandleWithPolicy did not handle disabled /model")
	}
	if !strings.Contains(resp, "暂不支持 /model") {
		t.Fatalf("response = %q, want unsupported command message", resp)
	}
}

func TestHandleHelpUsesPolicy(t *testing.T) {
	resp, handled, err := HandleWithPolicy("/help", "user", &fakeSessionManager{}, PolicyWithDisabled("/model"))
	if err != nil {
		t.Fatalf("HandleWithPolicy returned error: %v", err)
	}
	if !handled {
		t.Fatal("HandleWithPolicy did not handle /help")
	}
	if strings.Contains(resp, "/model") {
		t.Fatalf("response = %q, want /model hidden", resp)
	}
	if !strings.Contains(resp, "/current") {
		t.Fatalf("response = %q, want other shared commands visible", resp)
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
		sessions: []store.Session{{ID: "1", UserID: "user", Name: "default", Current: true}},
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

func TestHandleCurrent(t *testing.T) {
	resp, handled, err := Handle("/current", "user", &fakeSessionManager{currentModel: "gpt4o"})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /current")
	}
	for _, want := range []string{"default", "gpt4o"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("response = %q, want %s", resp, want)
		}
	}
}

func TestHandleRenameDuplicateSession(t *testing.T) {
	manager := &fakeSessionManager{
		renameErr: fmt.Errorf("%w: work", store.ErrSessionExists),
	}

	resp, handled, err := Handle("/rename work", "user", manager)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /rename")
	}
	if !strings.Contains(resp, "已存在") {
		t.Fatalf("response = %q, want duplicate message", resp)
	}
}

func TestHandleArchiveCurrent(t *testing.T) {
	resp, handled, err := Handle("/archive", "user", &fakeSessionManager{})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /archive")
	}
	for _, want := range []string{"已归档", "next"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("response = %q, want %s", resp, want)
		}
	}
}

func TestHandleModelUnknown(t *testing.T) {
	manager := &fakeSessionManager{
		setModelErr: fmt.Errorf("%w: missing", session.ErrModelNotFound),
	}

	resp, handled, err := Handle("/model missing", "user", manager)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /model")
	}
	if !strings.Contains(resp, "不存在") || !strings.Contains(resp, "deepseek") {
		t.Fatalf("response = %q, want unknown model message", resp)
	}
}

func TestHandleModelShowsCurrentAndAvailable(t *testing.T) {
	resp, handled, err := Handle("/model", "user", &fakeSessionManager{currentModel: "gpt4o"})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /model")
	}
	for _, want := range []string{"gpt4o", "deepseek"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("response = %q, want %s", resp, want)
		}
	}
}
