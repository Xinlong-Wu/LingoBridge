package session

import (
	"fmt"
	"strings"
	"time"

	"wechatbox/internal/store"
)

// Manager handles multi-user, multi-session conversation management.
type Manager struct {
	store *store.Store
}

// NewManager creates a new session manager.
func NewManager(st *store.Store) *Manager {
	return &Manager{store: st}
}

// GetOrCreateActiveSession returns the active session for a user.
// Creates a default session if none exists.
func (m *Manager) GetOrCreateActiveSession(userID string) (*store.Session, error) {
	return m.store.GetActiveSession(userID)
}

// LoadHistory loads the conversation history for a session.
func (m *Manager) LoadHistory(userID, sessionID string) (*store.Conversation, error) {
	return store.LoadConversation(userID, sessionID)
}

// SaveHistory saves a conversation snapshot for a session.
func (m *Manager) SaveHistory(userID, sessionID string, conv *store.Conversation) error {
	return store.AppendConversation(userID, sessionID, conv)
}

// CreateSession creates a new session for a user and sets it as active.
func (m *Manager) CreateSession(userID, name string) (*store.Session, error) {
	if name == "" {
		name = fmt.Sprintf("session-%d", time.Now().Unix())
	}
	return m.store.CreateSession(userID, name)
}

// ListSessions returns all sessions for a user.
func (m *Manager) ListSessions(userID string) ([]store.Session, error) {
	return m.store.ListSessions(userID)
}

// SwitchSession switches the active session for a user.
func (m *Manager) SwitchSession(userID, sessionName string) (*store.Session, error) {
	return m.store.SwitchSession(userID, sessionName)
}

// ClearSession archives the current session and creates a new one.
func (m *Manager) ClearSession(userID string) (*store.Session, error) {
	active, err := m.GetOrCreateActiveSession(userID)
	if err != nil {
		return nil, err
	}

	if err := m.store.ArchiveSession(userID, active.Name); err != nil {
		return nil, fmt.Errorf("archive session: %w", err)
	}

	return m.CreateSession(userID, "")
}

// FormatSessionList formats sessions for display.
func FormatSessionList(sessions []store.Session) string {
	var sb strings.Builder
	sb.WriteString("你的会话列表：\n")
	for _, s := range sessions {
		marker := "  "
		if s.Active {
			marker = "▶ "
		}
		sb.WriteString(fmt.Sprintf("%s%s (创建于 %s)\n", marker, s.Name, s.CreatedAt))
	}
	return sb.String()
}
