package feishu

import (
	"fmt"
	"strings"

	"lingobridge/internal/config"
	"lingobridge/internal/store"
)

const DefaultBaseURL = config.DefaultFeishuBaseURL

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

	return store.Account{
		ID:              "feishu:" + appID,
		Name:            name,
		Platform:        store.PlatformFeishu,
		BaseURL:         baseURL,
		UserID:          appID,
		CredentialsJSON: "{}",
		Enabled:         true,
	}, nil
}

func CredentialsFromConfig(account config.FeishuAccountConfig) Credentials {
	return Credentials{
		AppID:     strings.TrimSpace(account.AppID),
		AppSecret: strings.TrimSpace(account.AppSecret),
	}
}
