package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

func uploadOpenAIVisionFile(client *http.Client, baseURL, apiKey, filename string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("file data is empty")
	}
	if filename == "" {
		filename = "wechat-image"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", "vision"); err != nil {
		return "", fmt.Errorf("write purpose: %w", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("create file part: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("write file part: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, openaiFilesURL(baseURL), &body)
	if err != nil {
		return "", err
	}
	req.Header = bearerHeaders(apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai file upload request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("openai file upload read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai file upload HTTP %d: %s", resp.StatusCode, truncateStr(string(respBody), 500))
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("unmarshal file upload response: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("file upload response missing id")
	}
	return out.ID, nil
}

func openaiFilesURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/files"
	}
	return base + "/v1/files"
}
