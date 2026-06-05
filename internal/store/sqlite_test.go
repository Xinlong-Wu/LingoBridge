package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"wechatbox/internal/config"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	st, err := Open()
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	return st
}

func TestCreateSessionDuplicateName(t *testing.T) {
	st := openTestStore(t)

	if _, err := st.CreateSession("user", "work"); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	_, err := st.CreateSession("user", "work")
	if !errors.Is(err, ErrSessionExists) {
		t.Fatalf("CreateSession duplicate error = %v, want ErrSessionExists", err)
	}
}

func TestSwitchSessionNotFound(t *testing.T) {
	st := openTestStore(t)

	if _, err := st.CreateSession("user", "work"); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	_, err := st.SwitchSession("user", "missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SwitchSession error = %v, want ErrSessionNotFound", err)
	}
}

func TestCurrentSessionUsesUserPreference(t *testing.T) {
	st := openTestStore(t)

	work, err := st.CreateSession("user", "work")
	if err != nil {
		t.Fatalf("CreateSession work returned error: %v", err)
	}
	play, err := st.CreateSession("user", "play")
	if err != nil {
		t.Fatalf("CreateSession play returned error: %v", err)
	}
	if !play.Current {
		t.Fatal("new session is not current")
	}

	current, err := st.SwitchSession("user", "work")
	if err != nil {
		t.Fatalf("SwitchSession returned error: %v", err)
	}
	if current.ID != work.ID || !current.Current {
		t.Fatalf("current session = %#v, want work", current)
	}

	sessions, err := st.ListSessions("user")
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	currentCount := 0
	for _, sess := range sessions {
		if sess.Current {
			currentCount++
			if sess.ID != work.ID {
				t.Fatalf("current session = %s, want %s", sess.ID, work.ID)
			}
		}
	}
	if currentCount != 1 {
		t.Fatalf("current session count = %d, want 1", currentCount)
	}
}

func TestArchiveCurrentSessionFallsBack(t *testing.T) {
	st := openTestStore(t)

	if _, err := st.CreateSession("user", "work"); err != nil {
		t.Fatalf("CreateSession work returned error: %v", err)
	}
	if _, err := st.CreateSession("user", "play"); err != nil {
		t.Fatalf("CreateSession play returned error: %v", err)
	}
	if _, err := st.ArchiveSession("user", "play"); err != nil {
		t.Fatalf("ArchiveSession returned error: %v", err)
	}

	current, err := st.GetCurrentSession("user")
	if err != nil {
		t.Fatalf("GetCurrentSession returned error: %v", err)
	}
	if current.Name != "work" {
		t.Fatalf("current session = %s, want work", current.Name)
	}

	sessions, err := st.ListSessions("user")
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	for _, sess := range sessions {
		if sess.Name == "play" {
			t.Fatalf("archived session appeared in list: %#v", sess)
		}
	}
}

func TestResetUnavailableUserModels(t *testing.T) {
	st := openTestStore(t)

	if err := st.SetUserModelName("user1", "old"); err != nil {
		t.Fatalf("SetUserModelName user1 returned error: %v", err)
	}
	if err := st.SetUserModelName("user2", "deepseek"); err != nil {
		t.Fatalf("SetUserModelName user2 returned error: %v", err)
	}

	count, err := st.ResetUnavailableUserModels("deepseek", []string{"deepseek", "gpt4o"})
	if err != nil {
		t.Fatalf("ResetUnavailableUserModels returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("reset count = %d, want 1", count)
	}

	model, err := st.GetUserModelName("user1")
	if err != nil {
		t.Fatalf("GetUserModelName user1 returned error: %v", err)
	}
	if model != "deepseek" {
		t.Fatalf("user1 model = %q, want deepseek", model)
	}
	model, err = st.GetUserModelName("user2")
	if err != nil {
		t.Fatalf("GetUserModelName user2 returned error: %v", err)
	}
	if model != "deepseek" {
		t.Fatalf("user2 model = %q, want deepseek", model)
	}
}

func TestSaveAccountDefaultsToWeChat(t *testing.T) {
	st := openTestStore(t)

	if err := st.SaveAccount(Account{ID: "a1", Name: "bot", Token: "token", BaseURL: "https://wechat.test", Enabled: true}); err != nil {
		t.Fatalf("SaveAccount returned error: %v", err)
	}
	got, err := st.GetAccount("a1")
	if err != nil {
		t.Fatalf("GetAccount returned error: %v", err)
	}
	if got.Platform != PlatformWeChat {
		t.Fatalf("platform = %q, want %q", got.Platform, PlatformWeChat)
	}
	if got.CredentialsJSON != "{}" {
		t.Fatalf("credentials_json = %q, want {}", got.CredentialsJSON)
	}
}

func TestSaveFeishuAccountCredentials(t *testing.T) {
	st := openTestStore(t)

	account := Account{
		ID:              "feishu:cli_xxx",
		Name:            "fsbot",
		Platform:        PlatformFeishu,
		BaseURL:         "https://open.feishu.cn",
		UserID:          "cli_xxx",
		CredentialsJSON: `{"app_id":"cli_xxx","app_secret":"secret"}`,
		Enabled:         true,
	}
	if err := st.SaveAccount(account); err != nil {
		t.Fatalf("SaveAccount returned error: %v", err)
	}
	got, err := st.GetAccount(account.ID)
	if err != nil {
		t.Fatalf("GetAccount returned error: %v", err)
	}
	if got.Platform != PlatformFeishu || got.CredentialsJSON != account.CredentialsJSON || got.UserID != account.UserID {
		t.Fatalf("account = %#v, want feishu credentials preserved", got)
	}
}

func TestMigrateLegacyAccountsDefaultToWeChat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir, err := config.EnsureDataDir()
	if err != nil {
		t.Fatalf("EnsureDataDir returned error: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "wechatbox.db"))
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE accounts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		token TEXT NOT NULL,
		base_url TEXT NOT NULL DEFAULT 'https://ilinkai.weixin.qq.com',
		user_id TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1
	)`)
	if err != nil {
		t.Fatalf("create old accounts returned error: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, name, token, base_url, user_id, enabled) VALUES ('a1', 'bot', 'token', 'base', 'user', 1)`); err != nil {
		t.Fatalf("insert old account returned error: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close returned error: %v", err)
	}

	st, err := Open()
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	got, err := st.GetAccount("a1")
	if err != nil {
		t.Fatalf("GetAccount returned error: %v", err)
	}
	if got.Platform != PlatformWeChat {
		t.Fatalf("platform = %q, want %q", got.Platform, PlatformWeChat)
	}
	if got.CredentialsJSON != "{}" {
		t.Fatalf("credentials_json = %q, want {}", got.CredentialsJSON)
	}
}

func TestMigrateActiveSessionToUserPreference(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir, err := config.EnsureDataDir()
	if err != nil {
		t.Fatalf("EnsureDataDir returned error: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "wechatbox.db"))
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT 'default',
		active INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		t.Fatalf("create old sessions returned error: %v", err)
	}
	_, err = db.Exec(`INSERT INTO sessions (id, user_id, name, active, created_at) VALUES
		('old-current', 'user', 'current', 1, '2026-01-02 00:00:00'),
		('old-other', 'user', 'other', 0, '2026-01-01 00:00:00')`)
	if err != nil {
		t.Fatalf("insert old sessions returned error: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close returned error: %v", err)
	}

	st, err := Open()
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	current, err := st.GetCurrentSession("user")
	if err != nil {
		t.Fatalf("GetCurrentSession returned error: %v", err)
	}
	if current.ID != "old-current" {
		t.Fatalf("current session = %s, want old-current", current.ID)
	}
	columns, err := tableColumns(st.db, "sessions")
	if err != nil {
		t.Fatalf("tableColumns returned error: %v", err)
	}
	if columns["active"] {
		t.Fatal("sessions.active column still exists after migration")
	}
	if !columns["archived"] {
		t.Fatal("sessions.archived column missing after migration")
	}
}
