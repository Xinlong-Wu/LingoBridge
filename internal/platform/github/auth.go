package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	appJWTLifetime     = 9 * time.Minute
	appJWTBackdate     = 60 * time.Second
	tokenRefreshBefore = 5 * time.Minute
)

type tokenSource interface {
	Token(ctx context.Context) (string, error)
}

type appTokenSource struct {
	appID          string
	installationID string
	baseURL        string
	key            *rsa.PrivateKey
	httpClient     *http.Client
	now            func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newAppTokenSourceFromFile(appID, installationID, keyPath, baseURL string, httpClient *http.Client) (*appTokenSource, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read github app private key: %w", err)
	}
	key, err := parsePrivateKeyPEM(data)
	if err != nil {
		return nil, err
	}
	return newAppTokenSource(appID, installationID, baseURL, key, httpClient), nil
}

func newAppTokenSource(appID, installationID, baseURL string, key *rsa.PrivateKey, httpClient *http.Client) *appTokenSource {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &appTokenSource{
		appID:          strings.TrimSpace(appID),
		installationID: strings.TrimSpace(installationID),
		baseURL:        baseURL,
		key:            key,
		httpClient:     httpClient,
		now:            time.Now,
	}
}

func (s *appTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if s.token != "" && now.Add(tokenRefreshBefore).Before(s.expiresAt) {
		return s.token, nil
	}
	token, expiresAt, err := s.refresh(ctx, now)
	if err != nil {
		return "", err
	}
	s.token = token
	s.expiresAt = expiresAt
	return token, nil
}

func (s *appTokenSource) refresh(ctx context.Context, now time.Time) (string, time.Time, error) {
	if s.key == nil {
		return "", time.Time{}, fmt.Errorf("github app private key is required")
	}
	jwt, err := makeAppJWT(s.appID, s.key, now)
	if err != nil {
		return "", time.Time{}, err
	}
	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", s.baseURL, s.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create github installation token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read github installation token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("create github installation token: status=%d body=%s", resp.StatusCode, truncateForError(string(body)))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("parse github installation token response: %w", err)
	}
	if strings.TrimSpace(out.Token) == "" {
		return "", time.Time{}, fmt.Errorf("github installation token response missing token")
	}
	if out.ExpiresAt.IsZero() {
		return "", time.Time{}, fmt.Errorf("github installation token response missing expires_at")
	}
	return out.Token, out.ExpiresAt, nil
}

func parsePrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("parse github app private key: missing PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse github app private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("parse github app private key: key is not RSA")
	}
	return key, nil
}

func makeAppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	if strings.TrimSpace(appID) == "" {
		return "", fmt.Errorf("github app_id is required")
	}
	if key == nil {
		return "", fmt.Errorf("github app private key is required")
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-appJWTBackdate).Unix(),
		"exp": now.Add(appJWTLifetime).Unix(),
		"iss": strings.TrimSpace(appID),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign github app jwt: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func truncateForError(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 512 {
		return text
	}
	return text[:512] + "...[truncated]"
}
