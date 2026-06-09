package store

import "database/sql"

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
