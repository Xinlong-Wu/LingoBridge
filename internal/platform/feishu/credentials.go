package feishu

import (
	"encoding/json"
	"fmt"
	"strings"

	"wechatbox/internal/store"
)

const DefaultBaseURL = "https://open.feishu.cn"

type Credentials struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
}

func NewAccount(name, appID, appSecret, baseURL string) (store.Account, error) {
	appID = strings.TrimSpace(appID)
	appSecret = strings.TrimSpace(appSecret)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if appID == "" {
		return store.Account{}, fmt.Errorf("feishu app_id is required")
	}
	if appSecret == "" {
		return store.Account{}, fmt.Errorf("feishu app_secret is required")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	creds := Credentials{AppID: appID, AppSecret: appSecret}
	data, err := json.Marshal(creds)
	if err != nil {
		return store.Account{}, fmt.Errorf("marshal feishu credentials: %w", err)
	}
	return store.Account{
		ID:              "feishu:" + appID,
		Name:            name,
		Platform:        store.PlatformFeishu,
		BaseURL:         baseURL,
		UserID:          appID,
		CredentialsJSON: string(data),
		Enabled:         true,
	}, nil
}

func ParseCredentials(acc store.Account) (Credentials, error) {
	var creds Credentials
	if err := json.Unmarshal([]byte(acc.CredentialsJSON), &creds); err != nil {
		return creds, fmt.Errorf("parse feishu credentials: %w", err)
	}
	if strings.TrimSpace(creds.AppID) == "" {
		return creds, fmt.Errorf("feishu app_id is required")
	}
	if strings.TrimSpace(creds.AppSecret) == "" {
		return creds, fmt.Errorf("feishu app_secret is required")
	}
	return creds, nil
}
