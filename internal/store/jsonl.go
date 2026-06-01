package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"wechatbox/internal/config"
)

// Attachment represents non-text content associated with a chat message.
type Attachment struct {
	Type        string `json:"type"`
	MIMEType    string `json:"mime_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Size        int    `json:"size,omitempty"`
	RefProvider string `json:"ref_provider,omitempty"`
	RefType     string `json:"ref_type,omitempty"`
	RefID       string `json:"ref_id,omitempty"`
}

// Message represents a single chat message stored in conversation history.
type Message struct {
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Conversation is a snapshot of a full conversation (one JSONL line).
type Conversation struct {
	Messages []Message `json:"messages"`
}

// SessionDir returns the directory for a user's sessions.
func SessionDir(userID string) (string, error) {
	base, err := config.SessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, userID), nil
}

// SessionPath returns the JSONL file path for a specific session.
func SessionPath(userID, sessionID string) (string, error) {
	dir, err := SessionDir(userID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".jsonl"), nil
}

// LoadConversation reads the last line of a JSONL file as the current conversation.
// Returns an empty conversation if the file doesn't exist.
func LoadConversation(userID, sessionID string) (*Conversation, error) {
	path, err := SessionPath(userID, sessionID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Conversation{}, nil
		}
		return nil, fmt.Errorf("read session file: %w", err)
	}

	// Find the last non-empty line
	lines := splitLines(string(data))
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == "" {
			continue
		}
		var conv Conversation
		if err := json.Unmarshal([]byte(lines[i]), &conv); err != nil {
			return nil, fmt.Errorf("parse JSONL line %d: %w", i+1, err)
		}
		return &conv, nil
	}

	return &Conversation{}, nil
}

// AppendConversation appends a conversation snapshot as a new JSONL line.
func AppendConversation(userID, sessionID string, conv *Conversation) error {
	path, err := SessionPath(userID, sessionID)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	line, err := json.Marshal(conv)
	if err != nil {
		return fmt.Errorf("marshal conversation: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}

	return nil
}

// TruncateConversation removes all history for a session (start fresh).
func TruncateConversation(userID, sessionID string) error {
	path, err := SessionPath(userID, sessionID)
	if err != nil {
		return err
	}
	// Just delete the file; next append will recreate it
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("truncate session: %w", err)
	}
	return nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
