package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"

	"wechatbox/internal/config"
)

var (
	// ErrSessionExists is returned when a user already has a session with the requested name.
	ErrSessionExists = errors.New("session already exists")
	// ErrSessionNotFound is returned when a named session cannot be found for a user.
	ErrSessionNotFound = errors.New("session not found")
)

// Store provides SQLite-backed metadata storage.
type Store struct {
	db *sql.DB
	mu sync.Mutex // serializes writes across goroutines
}

// Account represents a WeChat bot account saved during login.
type Account struct {
	ID      string
	Name    string
	Token   string
	BaseURL string
	UserID  string
	Enabled bool
}

// Session represents a named conversation session for a user.
type Session struct {
	ID        string
	UserID    string
	Name      string
	Active    bool
	CreatedAt string
}

// Open creates or opens the SQLite database at ~/.wechatbox/data/wechatbox.db.
func Open() (*Store, error) {
	dataDir, err := config.EnsureDataDir()
	if err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dataDir, "wechatbox.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Enable WAL mode for concurrent reads, and busy timeout for concurrent writes.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			token TEXT NOT NULL,
			base_url TEXT NOT NULL DEFAULT 'https://ilinkai.weixin.qq.com',
			user_id TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS sync_cursors (
			account_id TEXT PRIMARY KEY,
			buf TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT 'default',
			active INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_active ON sessions(user_id, active)`,
	}

	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- Accounts ---

// SaveAccount inserts or updates a bot account.
func (s *Store) SaveAccount(a Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO accounts (id, name, token, base_url, user_id, enabled)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, token=excluded.token, base_url=excluded.base_url,
		   user_id=excluded.user_id, enabled=excluded.enabled`,
		a.ID, a.Name, a.Token, a.BaseURL, a.UserID, boolToInt(a.Enabled),
	)
	return err
}

// GetAccount retrieves an account by ID.
func (s *Store) GetAccount(id string) (Account, error) {
	var a Account
	err := s.db.QueryRow(
		`SELECT id, name, token, base_url, user_id, enabled FROM accounts WHERE id=?`, id,
	).Scan(&a.ID, &a.Name, &a.Token, &a.BaseURL, &a.UserID, &a.Enabled)
	if err != nil {
		return a, err
	}
	return a, nil
}

// ListAccounts returns all accounts.
func (s *Store) ListAccounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT id, name, token, base_url, user_id, enabled FROM accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.Token, &a.BaseURL, &a.UserID, &a.Enabled); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// DeleteAccount removes an account.
func (s *Store) DeleteAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM accounts WHERE id=?`, id)
	return err
}

// --- Sync Cursors ---

// GetSyncBuf returns the sync cursor buffer for an account.
func (s *Store) GetSyncBuf(accountID string) (string, error) {
	var buf string
	err := s.db.QueryRow(`SELECT buf FROM sync_cursors WHERE account_id=?`, accountID).Scan(&buf)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return buf, err
}

// SaveSyncBuf updates the sync cursor buffer for an account.
func (s *Store) SaveSyncBuf(accountID, buf string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO sync_cursors (account_id, buf, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(account_id) DO UPDATE SET buf=excluded.buf, updated_at=excluded.updated_at`,
		accountID, buf,
	)
	return err
}

// --- Sessions ---

// CreateSession creates a new session for a user. If name is empty, generates one.
func (s *Store) CreateSession(userID, name string) (*Session, error) {
	if name == "" {
		name = "default"
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Check for duplicate name while holding the write lock so concurrent
	// session creation cannot race past the read.
	var count int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE user_id=? AND name=?`, userID, name,
	).Scan(&count); err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, fmt.Errorf("%w: %q for user %s", ErrSessionExists, name, userID)
	}

	// Deactivate all other sessions for this user, then insert the new active one
	if _, err := tx.Exec(`UPDATE sessions SET active=0 WHERE user_id=?`, userID); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`INSERT INTO sessions (id, user_id, name, active) VALUES (?, ?, ?, 1)`,
		id, userID, name,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Session{ID: id, UserID: userID, Name: name, Active: true}, nil
}

// GetActiveSession returns the active session for a user, or creates a default one.
func (s *Store) GetActiveSession(userID string) (*Session, error) {
	var sess Session
	err := s.db.QueryRow(
		`SELECT id, user_id, name, active, created_at FROM sessions WHERE user_id=? AND active=1`,
		userID,
	).Scan(&sess.ID, &sess.UserID, &sess.Name, &sess.Active, &sess.CreatedAt)

	if err == sql.ErrNoRows {
		return s.CreateSession(userID, "default")
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// ListSessions returns all sessions for a user.
func (s *Store) ListSessions(userID string) ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, active, created_at FROM sessions WHERE user_id=? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.Name, &sess.Active, &sess.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// SwitchSession deactivates all sessions for a user and activates the given one.
func (s *Store) SwitchSession(userID, sessionName string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE sessions SET active=0 WHERE user_id=?`, userID); err != nil {
		return nil, err
	}

	result, err := tx.Exec(
		`UPDATE sessions SET active=1 WHERE user_id=? AND name=?`,
		userID, sessionName,
	)
	if err != nil {
		return nil, err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("%w: %q for user %s", ErrSessionNotFound, sessionName, userID)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetActiveSession(userID)
}

// ArchiveSession marks a session as inactive (soft delete).
func (s *Store) ArchiveSession(userID, sessionName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE sessions SET active=0 WHERE user_id=? AND name=? AND active=1`,
		userID, sessionName,
	)
	return err
}

// --- Helpers ---

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
