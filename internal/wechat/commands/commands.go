package commands

import (
	"errors"
	"fmt"
	"strings"

	"wechatbox/internal/session"
	"wechatbox/internal/store"
)

// SessionManager is the session behavior needed by in-chat commands.
type SessionManager interface {
	CreateSession(userID, name string) (*store.Session, error)
	ListSessions(userID string) ([]store.Session, error)
	SwitchSession(userID, sessionName string) (*store.Session, error)
	ClearSession(userID string) (*store.Session, error)
}

// Handle processes a slash command and returns the response text.
// Returns (response, handled, error).
func Handle(text string, userID string, sm SessionManager) (string, bool, error) {
	text = strings.TrimSpace(text)

	if !strings.HasPrefix(text, "/") {
		return "", false, nil
	}

	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", false, nil
	}

	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "/new":
		return handleNew(userID, args, sm)
	case "/list":
		return handleList(userID, sm)
	case "/switch":
		return handleSwitch(userID, args, sm)
	case "/clear":
		return handleClear(userID, sm)
	default:
		return "", false, nil // Not a recognized slash command
	}
}

func handleNew(userID string, args []string, sm SessionManager) (string, bool, error) {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}

	sess, err := sm.CreateSession(userID, name)
	if err != nil {
		if errors.Is(err, store.ErrSessionExists) {
			return fmt.Sprintf("❌ 会话 %q 已存在", name), true, nil
		}
		return "", true, fmt.Errorf("create session: %w", err)
	}

	return fmt.Sprintf("✅ 已创建新会话：%s", sess.Name), true, nil
}

func handleList(userID string, sm SessionManager) (string, bool, error) {
	sessions, err := sm.ListSessions(userID)
	if err != nil {
		return "", true, fmt.Errorf("list sessions: %w", err)
	}

	if len(sessions) == 0 {
		return "你还没有任何会话。发送消息即可自动创建默认会话。", true, nil
	}

	return session.FormatSessionList(sessions), true, nil
}

func handleSwitch(userID string, args []string, sm SessionManager) (string, bool, error) {
	if len(args) == 0 {
		return "用法：/switch <会话名称>", true, nil
	}

	name := args[0]
	sess, err := sm.SwitchSession(userID, name)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return fmt.Sprintf("❌ 会话 %q 不存在。使用 /list 查看所有会话。", name), true, nil
		}
		return "", true, fmt.Errorf("switch session: %w", err)
	}

	return fmt.Sprintf("✅ 已切换到会话：%s", sess.Name), true, nil
}

func handleClear(userID string, sm SessionManager) (string, bool, error) {
	sess, err := sm.ClearSession(userID)
	if err != nil {
		return "", true, fmt.Errorf("clear session: %w", err)
	}

	return fmt.Sprintf("✅ 已清空当前会话，新会话：%s", sess.Name), true, nil
}
