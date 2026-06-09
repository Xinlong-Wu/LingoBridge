package store

import "fmt"

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			platform TEXT NOT NULL,
			token TEXT NOT NULL,
			base_url TEXT NOT NULL,
			user_id TEXT NOT NULL,
			credentials_json TEXT NOT NULL,
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
