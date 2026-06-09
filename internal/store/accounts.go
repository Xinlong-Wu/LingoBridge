package store

import "fmt"

// SaveAccount inserts or updates a bot account.
func (s *Store) SaveAccount(a Account) error {
	if a.Platform != s.platformID {
		return fmt.Errorf("account platform %q does not match store platform %q", a.Platform, s.platformID)
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
	if a.Platform != s.platformID {
		return Account{}, fmt.Errorf("account %q platform %q does not match store platform %q", id, a.Platform, s.platformID)
	}
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
		if a.Platform != s.platformID {
			return nil, fmt.Errorf("account %q platform %q does not match store platform %q", a.ID, a.Platform, s.platformID)
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
