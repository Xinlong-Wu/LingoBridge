package cdn

import (
	"fmt"
	"io"
	"net/http"
)

// DownloadAndDecrypt downloads a CDN media file and decrypts it with AES-128-ECB.
func DownloadAndDecrypt(encryptQueryParam, aesKeyBase64, fullURL string) ([]byte, error) {
	key, err := ParseAESKey(aesKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("parse aes key: %w", err)
	}

	downloadURL := fullURL
	if downloadURL == "" && encryptQueryParam != "" {
		downloadURL = encryptQueryParam
	}
	if downloadURL == "" {
		return nil, fmt.Errorf("no download URL available")
	}

	resp, err := http.Get(downloadURL)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("CDN download HTTP %d: %s", resp.StatusCode, string(body))
	}

	encrypted, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	decrypted, err := DecryptAESECB(encrypted, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return decrypted, nil
}
