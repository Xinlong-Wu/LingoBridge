package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"wechatbox/internal/store"
)

// Client is the common LLM client interface.
type Client interface {
	// Chat sends messages and returns the full response.
	Chat(systemPrompt string, messages []store.Message) (string, error)
	// ChatStream sends messages and streams the response via callback.
	// The callback receives incremental text chunks.
	ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (string, error)
}

// Config holds the LLM client configuration.
type Config struct {
	Provider string // "openai" or "anthropic"
	BaseURL  string
	APIKey   string
	Model    string
	Endpoint string // "chat" (default) or "responses"
}

// NewClient creates an LLM client based on the provider.
func NewClient(cfg Config) Client {
	switch cfg.Provider {
	case "anthropic":
		return &anthropicClient{cfg: cfg}
	default:
		return &openaiClient{cfg: cfg}
	}
}

// --- OpenAI-compatible client ---

type openaiClient struct {
	cfg     Config
	reqURL  string // cached request URL
	useResp bool   // use /v1/responses instead of /v1/chat/completions
}

func (c *openaiClient) initURL() {
	if c.reqURL != "" {
		return
	}
	base := strings.TrimRight(c.cfg.BaseURL, "/")

	if c.cfg.Endpoint == "responses" {
		c.useResp = true
		if strings.HasSuffix(base, "/v1") {
			c.reqURL = base + "/responses"
		} else {
			c.reqURL = base + "/v1/responses"
		}
	} else {
		if strings.HasSuffix(base, "/v1") {
			c.reqURL = base + "/chat/completions"
		} else {
			c.reqURL = base + "/v1/chat/completions"
		}
	}
}

type openaiChatRequest struct {
	Model    string          `json:"model"`
	Messages []store.Message `json:"messages"`
	Stream   bool            `json:"stream"`
}

type openaiChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type openaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// --- Responses API types ---

type responsesRequest struct {
	Model  string          `json:"model"`
	Input  []store.Message `json:"input"`
	Stream bool            `json:"stream"`
}

type responsesOutput struct {
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

type responsesStreamEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

func (c *openaiClient) Chat(systemPrompt string, messages []store.Message) (string, error) {
	c.initURL()

	if c.useResp {
		return c.chatResponses(messages, false, nil)
	}

	reqBody := openaiChatRequest{
		Model:    c.cfg.Model,
		Messages: messages,
		Stream:   false,
	}

	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", c.reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("openai HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var chatResp openaiChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(chatResp.Choices) > 0 {
		return chatResp.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("no choices in response")
}

func (c *openaiClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (string, error) {
	c.initURL()

	if c.useResp {
		return c.chatResponses(messages, true, onChunk)
	}

	reqBody := openaiChatRequest{
		Model:    c.cfg.Model,
		Messages: messages,
		Stream:   true,
	}

	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", c.reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai stream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("openai stream HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	return c.parseStream(resp.Body, onChunk)
}

func (c *openaiClient) parseStream(body io.Reader, onChunk func(chunk string) error) (string, error) {
	var fullText strings.Builder
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 {
			content := chunk.Choices[0].Delta.Content
			if content != "" {
				fullText.WriteString(content)
				if onChunk != nil {
					if err := onChunk(content); err != nil {
						return fullText.String(), err
					}
				}
			}
		}
	}

	return fullText.String(), scanner.Err()
}

func (c *openaiClient) chatResponses(messages []store.Message, stream bool, onChunk func(chunk string) error) (string, error) {
	// Filter out system messages and build input
	var input []store.Message
	for _, m := range messages {
		if m.Role != "system" {
			input = append(input, m)
		}
	}

	reqBody := responsesRequest{
		Model:  c.cfg.Model,
		Input:  input,
		Stream: stream,
	}

	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", c.reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("responses request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("responses HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	if !stream {
		body, _ := io.ReadAll(resp.Body)
		var out responsesOutput
		if err := json.Unmarshal(body, &out); err != nil {
			return "", fmt.Errorf("unmarshal responses: %w", err)
		}
		for _, o := range out.Output {
			if o.Type == "message" {
				for _, c := range o.Content {
					if c.Type == "output_text" {
						return c.Text, nil
					}
				}
			}
		}
		return "", fmt.Errorf("no output_text in responses")
	}

	// Streaming
	var fullText strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		if event.Type == "response.output_text.delta" && event.Delta != "" {
			fullText.WriteString(event.Delta)
			if onChunk != nil {
				if err := onChunk(event.Delta); err != nil {
					return fullText.String(), err
				}
			}
		}
	}

	return fullText.String(), scanner.Err()
}

// --- Anthropic client ---

type anthropicClient struct {
	cfg Config
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessage struct {
	Role    string              `json:"role"`
	Content []anthropicContent  `json:"content,omitempty"`
	Text    string              `json:"text,omitempty"` // for user messages in older format
}

type anthropicRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	Messages  []anthropicMessage  `json:"messages"`
	System    string              `json:"system,omitempty"`
	Stream    bool                `json:"stream"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
	ContentBlock struct {
		Text string `json:"text"`
	} `json:"content_block"`
	Message struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func convertToAnthropicMessages(messages []store.Message, systemPrompt string) ([]anthropicMessage, string) {
	var msgs []anthropicMessage
	system := systemPrompt

	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		msg := anthropicMessage{Role: m.Role}
		if m.Role == "user" {
			msg.Content = []anthropicContent{{Type: "text", Text: m.Content}}
		} else {
			msg.Content = []anthropicContent{{Type: "text", Text: m.Content}}
		}
		msgs = append(msgs, msg)
	}
	return msgs, system
}

func (c *anthropicClient) Chat(systemPrompt string, messages []store.Message) (string, error) {
	anthropicMsgs, system := convertToAnthropicMessages(messages, systemPrompt)

	reqBody := anthropicRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
		System:    system,
		Stream:    false,
	}

	bodyBytes, _ := json.Marshal(reqBody)
	baseURL := strings.TrimRight(c.cfg.BaseURL, "/")

	req, err := http.NewRequest("POST", baseURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("anthropic HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var chatResp anthropicResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(chatResp.Content) > 0 {
		return chatResp.Content[0].Text, nil
	}
	return "", fmt.Errorf("no content in response")
}

func (c *anthropicClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (string, error) {
	anthropicMsgs, system := convertToAnthropicMessages(messages, systemPrompt)

	reqBody := anthropicRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
		System:    system,
		Stream:    true,
	}

	bodyBytes, _ := json.Marshal(reqBody)
	baseURL := strings.TrimRight(c.cfg.BaseURL, "/")

	req, err := http.NewRequest("POST", baseURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic stream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("anthropic stream HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var fullText strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		var text string
		switch event.Type {
		case "content_block_delta":
			text = event.Delta.Text
		case "content_block_start":
			if event.ContentBlock.Text != "" {
				text = event.ContentBlock.Text
			}
		}

		if text != "" {
			fullText.WriteString(text)
			if onChunk != nil {
				if err := onChunk(text); err != nil {
					return fullText.String(), err
				}
			}
		}
	}

	return fullText.String(), scanner.Err()
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Ensure time is used (for potential future timeout configs)
var _ = time.Now
