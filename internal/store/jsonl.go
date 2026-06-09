package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	LocalPath   string `json:"local_path,omitempty"`
}

// Message represents a single chat message stored in conversation history.
type Message struct {
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ToolTraces  []ToolTrace  `json:"tool_traces,omitempty"`
}

// ToolTrace is a compact audit record for tool use during one assistant turn.
type ToolTrace struct {
	CallID         string `json:"call_id,omitempty"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	Arguments      string `json:"arguments,omitempty"`
	Result         string `json:"result,omitempty"`
	Error          string `json:"error,omitempty"`
	DurationMillis int64  `json:"duration_ms,omitempty"`
}

// ProviderContext stores opaque provider-native context items for one model profile.
type ProviderContext struct {
	Provider string            `json:"provider,omitempty"`
	Endpoint string            `json:"endpoint,omitempty"`
	Items    []json.RawMessage `json:"items,omitempty"`
}

// IsEmpty reports whether the context has no provider-owned items to round-trip.
func (c ProviderContext) IsEmpty() bool {
	return len(c.Items) == 0
}

// Conversation is a snapshot of a full conversation (one JSONL line).
type Conversation struct {
	Messages         []Message                  `json:"messages"`
	ProviderContexts map[string]ProviderContext `json:"provider_contexts,omitempty"`
}

// SessionDir returns the directory for a user's sessions in this platform store.
func (s *Store) SessionDir(userID string) string {
	return filepath.Join(s.dataDir, "sessions", userID)
}

// SessionPath returns the JSONL file path for a specific session in this platform store.
func (s *Store) SessionPath(userID, sessionID string) string {
	return filepath.Join(s.SessionDir(userID), sessionID+".jsonl")
}

// LoadConversation reads the last line of a JSONL file as the current conversation.
// Returns an empty conversation if the file doesn't exist.
func (s *Store) LoadConversation(userID, sessionID string) (*Conversation, error) {
	path := s.SessionPath(userID, sessionID)

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
func (s *Store) AppendConversation(userID, sessionID string, conv *Conversation) error {
	path := s.SessionPath(userID, sessionID)

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

// TruncateConversation removes all history for a session in this platform store.
func (s *Store) TruncateConversation(userID, sessionID string) error {
	path := s.SessionPath(userID, sessionID)
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
