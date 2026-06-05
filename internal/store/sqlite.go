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

const (
	// PlatformWeChat identifies accounts backed by the WeChat bot API.
	PlatformWeChat = "wechat"
	// PlatformFeishu identifies accounts backed by the Feishu Open Platform.
	PlatformFeishu = "feishu"
)

// Account represents a bot account saved during login or configuration.
type Account struct {
	ID              string
	Name            string
	Platform        string
	Token           string
	BaseURL         string
	UserID          string
	CredentialsJSON string
	Enabled         bool
}

func (a *Account) applyDefaults() {
	if a.Platform == "" {
		a.Platform = PlatformWeChat
	}
	if a.CredentialsJSON == "" {
		a.CredentialsJSON = "{}"
	}
}

// Session represents a named conversation session for a user.
type Session struct {
	ID        string
	UserID    string
	Name      string
	Archived  bool
	Current   bool
	CreatedAt string
}

// ArchiveResult describes the result of archiving a session.
type ArchiveResult struct {
	Archived       Session
	Current        *Session
	CurrentChanged bool
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
			platform TEXT NOT NULL DEFAULT 'wechat',
			token TEXT NOT NULL,
			base_url TEXT NOT NULL DEFAULT 'https://ilinkai.weixin.qq.com',
			user_id TEXT NOT NULL DEFAULT '',
			credentials_json TEXT NOT NULL DEFAULT '{}',
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
			archived INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS user_preferences (
			user_id TEXT PRIMARY KEY,
			current_session_id TEXT NOT NULL DEFAULT '',
			model_name TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}

	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := s.migrateAccounts(); err != nil {
		return err
	}
	if err := s.migrateSessions(); err != nil {
		return err
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_archived ON sessions(user_id, archived)`,
	}
	for _, q := range indexes {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate indexes: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateAccounts() error {
	columns, err := tableColumns(s.db, "accounts")
	if err != nil {
		return err
	}
	if !columns["platform"] {
		if _, err := s.db.Exec(`ALTER TABLE accounts ADD COLUMN platform TEXT NOT NULL DEFAULT 'wechat'`); err != nil {
			return fmt.Errorf("add accounts platform column: %w", err)
		}
	}
	if !columns["credentials_json"] {
		if _, err := s.db.Exec(`ALTER TABLE accounts ADD COLUMN credentials_json TEXT NOT NULL DEFAULT '{}'`); err != nil {
			return fmt.Errorf("add accounts credentials_json column: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateSessions() error {
	columns, err := tableColumns(s.db, "sessions")
	if err != nil {
		return err
	}
	if !columns["active"] {
		if !columns["archived"] {
			if _, err := s.db.Exec(`ALTER TABLE sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("add archived column: %w", err)
			}
		}
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT user_id, id FROM sessions WHERE active=1 ORDER BY user_id, created_at DESC, id DESC`)
	if err != nil {
		return fmt.Errorf("query active sessions: %w", err)
	}
	activeByUser := map[string]string{}
	for rows.Next() {
		var userID, sessionID string
		if err := rows.Scan(&userID, &sessionID); err != nil {
			rows.Close()
			return fmt.Errorf("scan active session: %w", err)
		}
		if _, ok := activeByUser[userID]; !ok {
			activeByUser[userID] = sessionID
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for userID, sessionID := range activeByUser {
		if err := upsertCurrentSessionTx(tx, userID, sessionID); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_sessions_active`); err != nil {
		return fmt.Errorf("drop active index: %w", err)
	}
	if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_sessions_user`); err != nil {
		return fmt.Errorf("drop sessions user index: %w", err)
	}
	if _, err := tx.Exec(`CREATE TABLE sessions_new (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT 'default',
		archived INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create sessions_new: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO sessions_new (id, user_id, name, archived, created_at)
		SELECT id, user_id, name, 0, created_at FROM sessions`); err != nil {
		return fmt.Errorf("copy sessions: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE sessions`); err != nil {
		return fmt.Errorf("drop old sessions: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE sessions_new RENAME TO sessions`); err != nil {
		return fmt.Errorf("rename sessions_new: %w", err)
	}
	return tx.Commit()
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- Accounts ---

// SaveAccount inserts or updates a bot account.
func (s *Store) SaveAccount(a Account) error {
	if a.Platform == "" {
		a.Platform = PlatformWeChat
	}
	if a.CredentialsJSON == "" {
		a.CredentialsJSON = "{}"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO accounts (id, name, platform, token, base_url, user_id, credentials_json, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, platform=excluded.platform, token=excluded.token, base_url=excluded.base_url,
		   user_id=excluded.user_id, credentials_json=excluded.credentials_json, enabled=excluded.enabled`,
		a.ID, a.Name, a.Platform, a.Token, a.BaseURL, a.UserID, a.CredentialsJSON, boolToInt(a.Enabled),
	)
	return err
}

// GetAccount retrieves an account by ID.
func (s *Store) GetAccount(id string) (Account, error) {
	var a Account
	err := s.db.QueryRow(
		`SELECT id, name, platform, token, base_url, user_id, credentials_json, enabled FROM accounts WHERE id=?`, id,
	).Scan(&a.ID, &a.Name, &a.Platform, &a.Token, &a.BaseURL, &a.UserID, &a.CredentialsJSON, &a.Enabled)
	if err != nil {
		return a, err
	}
	a.applyDefaults()
	return a, nil
}

// ListAccounts returns all accounts.
func (s *Store) ListAccounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT id, name, platform, token, base_url, user_id, credentials_json, enabled FROM accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.Platform, &a.Token, &a.BaseURL, &a.UserID, &a.CredentialsJSON, &a.Enabled); err != nil {
			return nil, err
		}
		a.applyDefaults()
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

// CreateSession creates a new session for a user and sets it as current.
func (s *Store) CreateSession(userID, name string) (*Session, error) {
	if name == "" {
		name = "default"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	sess, err := createSessionTx(tx, userID, name)
	if err != nil {
		return nil, err
	}
	if err := upsertCurrentSessionTx(tx, userID, sess.ID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	sess.Current = true
	return sess, nil
}

// GetCurrentSession returns the current session for a user, or creates a default one.
func (s *Store) GetCurrentSession(userID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	sess, err := currentSessionTx(tx, userID)
	if errors.Is(err, ErrSessionNotFound) {
		sess, err = ensureCurrentSessionTx(tx, userID)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return sess, nil
}

// ListSessions returns all unarchived sessions for a user.
func (s *Store) ListSessions(userID string) ([]Session, error) {
	currentID, err := s.getCurrentSessionID(userID)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(
		`SELECT id, user_id, name, archived, created_at FROM sessions
		 WHERE user_id=? AND archived=0 ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.Name, &sess.Archived, &sess.CreatedAt); err != nil {
			return nil, err
		}
		sess.Current = sess.ID == currentID
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// SwitchSession sets an unarchived session as current.
func (s *Store) SwitchSession(userID, sessionName string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	sess, err := sessionByNameTx(tx, userID, sessionName)
	if err != nil {
		return nil, err
	}
	if err := upsertCurrentSessionTx(tx, userID, sess.ID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sess.Current = true
	return sess, nil
}

// RenameSession renames an unarchived session.
func (s *Store) RenameSession(userID, sessionID, newName string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	sess, err := sessionByIDTx(tx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if err := checkSessionNameAvailableTx(tx, userID, newName, sessionID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE sessions SET name=? WHERE id=? AND user_id=?`, newName, sessionID, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sess.Name = newName
	currentID, err := s.getCurrentSessionID(userID)
	if err != nil {
		return nil, err
	}
	sess.Current = sess.ID == currentID
	return sess, nil
}

// ArchiveSession marks an unarchived session as archived and clears it as current.
func (s *Store) ArchiveSession(userID, sessionName string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	sess, err := sessionByNameTx(tx, userID, sessionName)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE sessions SET archived=1 WHERE id=? AND user_id=?`, sess.ID, userID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE user_preferences SET current_session_id='', updated_at=datetime('now')
		 WHERE user_id=? AND current_session_id=?`,
		userID, sess.ID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sess.Archived = true
	sess.Current = false
	return sess, nil
}

// GetUserModelName returns the stored model preference for a user.
func (s *Store) GetUserModelName(userID string) (string, error) {
	var modelName string
	err := s.db.QueryRow(`SELECT model_name FROM user_preferences WHERE user_id=?`, userID).Scan(&modelName)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return modelName, err
}

// SetUserModelName saves a model preference for a user.
func (s *Store) SetUserModelName(userID, modelName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO user_preferences (user_id, model_name, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(user_id) DO UPDATE SET model_name=excluded.model_name, updated_at=excluded.updated_at`,
		userID, modelName,
	)
	return err
}

// ResetUnavailableUserModels resets model preferences not present in validModels to defaultModel.
func (s *Store) ResetUnavailableUserModels(defaultModel string, validModels []string) (int, error) {
	valid := map[string]bool{}
	for _, name := range validModels {
		valid[name] = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT user_id, model_name FROM user_preferences WHERE model_name<>''`)
	if err != nil {
		return 0, err
	}
	var users []string
	for rows.Next() {
		var userID, modelName string
		if err := rows.Scan(&userID, &modelName); err != nil {
			rows.Close()
			return 0, err
		}
		if !valid[modelName] {
			users = append(users, userID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, userID := range users {
		if _, err := tx.Exec(
			`UPDATE user_preferences SET model_name=?, updated_at=datetime('now') WHERE user_id=?`,
			defaultModel, userID,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(users), nil
}

// --- Helpers ---

func createSessionTx(tx *sql.Tx, userID, name string) (*Session, error) {
	if err := checkSessionNameAvailableTx(tx, userID, name, ""); err != nil {
		return nil, err
	}
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO sessions (id, user_id, name, archived) VALUES (?, ?, ?, 0)`,
		id, userID, name,
	); err != nil {
		return nil, err
	}
	return sessionByIDTx(tx, userID, id)
}

func checkSessionNameAvailableTx(tx *sql.Tx, userID, name, exceptID string) error {
	var count int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE user_id=? AND name=? AND archived=0 AND id<>?`,
		userID, name, exceptID,
	).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w: %q for user %s", ErrSessionExists, name, userID)
	}
	return nil
}

func currentSessionTx(tx *sql.Tx, userID string) (*Session, error) {
	var currentID string
	err := tx.QueryRow(`SELECT current_session_id FROM user_preferences WHERE user_id=?`, userID).Scan(&currentID)
	if err == sql.ErrNoRows || currentID == "" {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	sess, err := sessionByIDTx(tx, userID, currentID)
	if err != nil {
		return nil, err
	}
	sess.Current = true
	return sess, nil
}

func ensureCurrentSessionTx(tx *sql.Tx, userID string) (*Session, error) {
	sess, err := latestUnarchivedSessionTx(tx, userID)
	if errors.Is(err, ErrSessionNotFound) {
		sess, err = createSessionTx(tx, userID, "default")
	}
	if err != nil {
		return nil, err
	}
	if err := upsertCurrentSessionTx(tx, userID, sess.ID); err != nil {
		return nil, err
	}
	sess.Current = true
	return sess, nil
}

func latestUnarchivedSessionTx(tx *sql.Tx, userID string) (*Session, error) {
	var sess Session
	err := tx.QueryRow(
		`SELECT id, user_id, name, archived, created_at FROM sessions
		 WHERE user_id=? AND archived=0 ORDER BY created_at DESC, id DESC LIMIT 1`,
		userID,
	).Scan(&sess.ID, &sess.UserID, &sess.Name, &sess.Archived, &sess.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrSessionNotFound
	}
	return &sess, err
}

func sessionByNameTx(tx *sql.Tx, userID, name string) (*Session, error) {
	var sess Session
	err := tx.QueryRow(
		`SELECT id, user_id, name, archived, created_at FROM sessions
		 WHERE user_id=? AND name=? AND archived=0`,
		userID, name,
	).Scan(&sess.ID, &sess.UserID, &sess.Name, &sess.Archived, &sess.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %q for user %s", ErrSessionNotFound, name, userID)
	}
	return &sess, err
}

func sessionByIDTx(tx *sql.Tx, userID, sessionID string) (*Session, error) {
	var sess Session
	err := tx.QueryRow(
		`SELECT id, user_id, name, archived, created_at FROM sessions
		 WHERE user_id=? AND id=? AND archived=0`,
		userID, sessionID,
	).Scan(&sess.ID, &sess.UserID, &sess.Name, &sess.Archived, &sess.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrSessionNotFound
	}
	return &sess, err
}

func upsertCurrentSessionTx(tx *sql.Tx, userID, sessionID string) error {
	_, err := tx.Exec(
		`INSERT INTO user_preferences (user_id, current_session_id, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(user_id) DO UPDATE SET current_session_id=excluded.current_session_id, updated_at=excluded.updated_at`,
		userID, sessionID,
	)
	return err
}

func (s *Store) getCurrentSessionID(userID string) (string, error) {
	var currentID string
	err := s.db.QueryRow(`SELECT current_session_id FROM user_preferences WHERE user_id=?`, userID).Scan(&currentID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return currentID, err
}

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
