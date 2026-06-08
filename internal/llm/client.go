package llm

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"lingobridge/internal/store"
)

// Client is the common LLM client interface.
type Client interface {
	// PrepareUserMessage converts user input into the provider-specific history shape.
	PrepareUserMessage(content string, attachments []InputAttachment) (store.Message, error)
	// Chat sends messages and returns the full response.
	Chat(systemPrompt string, messages []store.Message) (Response, error)
	// ChatStream sends messages and streams the response via callback.
	// The callback receives incremental text chunks.
	ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error)
	// AssistantMessage converts a provider response into the stored history shape.
	AssistantMessage(resp Response) (store.Message, error)
}

// CompactConfig controls provider-native context compaction for one request.
type CompactConfig struct {
	Mode          string
	ContextWindow int
	Threshold     float64
	Instructions  string
}

// ContextCompactor explicitly compacts a slice of history through the provider.
type ContextCompactor interface {
	CompactContext(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig) (store.ProviderContext, error)
}

// ContextStreamingClient sends a request with provider-native context state.
type ContextStreamingClient interface {
	ChatStreamWithContext(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig, onChunk func(chunk string) error) (Response, error)
}

// Response is the common LLM response shape across providers.
type Response struct {
	Text            string
	Images          []Image
	ProviderContext store.ProviderContext
	Compacted       bool
}

// InputAttachment is non-text user input before provider-specific preparation.
type InputAttachment struct {
	Type      string
	MIMEType  string
	Filename  string
	Size      int
	Data      []byte
	LocalPath string
}

// AttachmentRef identifies provider-owned content that can be referenced later.
type AttachmentRef struct {
	Provider string
	Type     string
	ID       string
}

// Image is a generated image returned by an LLM provider.
type Image struct {
	Data      []byte
	MIMEType  string
	Filename  string
	LocalPath string
	Reference AttachmentRef
}

var (
	// ErrUnsupportedAttachment means the selected model cannot accept the supplied attachment.
	ErrUnsupportedAttachment = errors.New("unsupported attachment")
	// ErrCompactionNotTriggered means the provider accepted a compact request but did not emit compacted context.
	ErrCompactionNotTriggered = errors.New("provider-native compaction was not triggered")
)

// Config holds the LLM client configuration.
type Config struct {
	Provider string // "openai" or "anthropic"
	BaseURL  string
	APIKey   string
	Model    string
	Endpoint string // provider-specific endpoint mode
	Compact  CompactConfig
}

// NewClient creates an LLM client based on the provider.
func NewClient(cfg Config) Client {
	switch cfg.Provider {
	case "anthropic":
		return &anthropicClient{cfg: cfg, httpClient: http.DefaultClient}
	default:
		return newOpenAIClient(cfg)
	}
}

func prepareTextUserMessage(content string, attachments []InputAttachment) (store.Message, error) {
	if len(attachments) > 0 {
		return store.Message{}, ErrUnsupportedAttachment
	}
	return store.Message{Role: "user", Content: content}, nil
}

func defaultAssistantMessage(resp Response) (store.Message, error) {
	return store.Message{Role: "assistant", Content: responseHistoryContent(resp)}, nil
}

func responseHistoryContent(resp Response) string {
	var parts []string
	if resp.Text != "" {
		parts = append(parts, resp.Text)
	}
	for _, image := range resp.Images {
		mimeType, filename := imageHistoryMetadata(image)
		parts = append(parts, fmt.Sprintf("[图片: mime=%s filename=%s base64=%s]", mimeType, filename, base64.StdEncoding.EncodeToString(image.Data)))
	}
	return strings.Join(parts, "\n")
}

func responseHistoryContentWithoutImageData(resp Response) string {
	var parts []string
	if resp.Text != "" {
		parts = append(parts, resp.Text)
	}
	for _, image := range resp.Images {
		mimeType, filename := imageHistoryMetadata(image)
		parts = append(parts, fmt.Sprintf("[图片: mime=%s filename=%s]", mimeType, filename))
	}
	return strings.Join(parts, "\n")
}

func imageHistoryMetadata(image Image) (string, string) {
	mimeType := image.MIMEType
	if mimeType == "" {
		mimeType = "image/png"
	}
	filename := image.Filename
	if filename == "" {
		filename = "image.png"
	}
	return mimeType, filename
}

type streamParser func(data string) string

const maxSSERawLogLen = 4096

func postJSON(client *http.Client, reqURL string, headers http.Header, reqBody any, label string) ([]byte, error) {
	resp, err := sendJSON(client, reqURL, headers, reqBody)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", label, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s read response: %w", label, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d: %s", label, resp.StatusCode, truncateStr(string(body), 500))
	}
	return body, nil
}

func postStream(client *http.Client, reqURL string, headers http.Header, reqBody any, label string, parser streamParser, onChunk func(chunk string) error) (string, error) {
	resp, err := sendJSON(client, reqURL, headers, reqBody)
	if err != nil {
		return "", fmt.Errorf("%s stream request: %w", label, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s stream HTTP %d: %s", label, resp.StatusCode, truncateStr(string(body), 500))
	}

	return parseSSE(resp.Body, parser, onChunk)
}

func sendJSON(client *http.Client, reqURL string, headers http.Header, reqBody any) (*http.Response, error) {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	req.Header.Set("Content-Type", "application/json")

	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

func parseSSE(body io.Reader, parser streamParser, onChunk func(chunk string) error) (string, error) {
	var fullText strings.Builder
	err := readSSEData(body, func(data string) (bool, error) {
		chunk := parser(data)
		if chunk == "" {
			return false, nil
		}

		fullText.WriteString(chunk)
		if onChunk != nil {
			if err := onChunk(chunk); err != nil {
				return false, err
			}
		}
		return false, nil
	})
	return fullText.String(), err
}

func readSSEData(body io.Reader, handle func(data string) (done bool, err error)) error {
	reader := bufio.NewReader(body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			logSSERawData(line)
			if !strings.HasPrefix(line, "data: ") {
				if err == io.EOF {
					return nil
				}
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return nil
			}

			done, handleErr := handle(data)
			if handleErr != nil {
				return handleErr
			}
			if done {
				return nil
			}
		}

		if err == io.EOF {
			return nil
		}
	}
}

func logSSERawData(line string) {
}

func bearerHeaders(apiKey string) http.Header {
	h := http.Header{}
	if apiKey != "" {
		h.Set("Authorization", "Bearer "+apiKey)
	}
	return h
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
