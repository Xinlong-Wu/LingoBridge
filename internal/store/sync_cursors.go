package store

import "database/sql"

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

// DeleteSyncBuf removes the sync cursor buffer for an account.
func (s *Store) DeleteSyncBuf(accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM sync_cursors WHERE account_id=?`, accountID)
	return err
}
