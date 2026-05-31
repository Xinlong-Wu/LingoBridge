package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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
		return &anthropicClient{cfg: cfg, httpClient: http.DefaultClient}
	default:
		return &openaiClient{cfg: cfg, httpClient: http.DefaultClient}
	}
}

type streamParser func(data string) string

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

		chunk := parser(data)
		if chunk == "" {
			continue
		}

		fullText.WriteString(chunk)
		if onChunk != nil {
			if err := onChunk(chunk); err != nil {
				return fullText.String(), err
			}
		}
	}

	return fullText.String(), scanner.Err()
}

func bearerHeaders(apiKey string) http.Header {
	h := http.Header{}
	if apiKey != "" {
		h.Set("Authorization", "Bearer "+apiKey)
	}
	return h
}

// --- OpenAI-compatible client ---

type openaiClient struct {
	cfg        Config
	httpClient *http.Client
}

func (c *openaiClient) requestURL() (string, bool) {
	base := strings.TrimRight(c.cfg.BaseURL, "/")

	if c.cfg.Endpoint == "responses" {
		if strings.HasSuffix(base, "/v1") {
			return base + "/responses", true
		}
		return base + "/v1/responses", true
	}

	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions", false
	}
	return base + "/v1/chat/completions", false
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
	reqURL, useResponses := c.requestURL()
	if useResponses {
		return c.chatResponses(reqURL, messages, false, nil)
	}

	reqBody := openaiChatRequest{
		Model:    c.cfg.Model,
		Messages: messages,
		Stream:   false,
	}

	body, err := postJSON(c.httpClient, reqURL, bearerHeaders(c.cfg.APIKey), reqBody, "openai")
	if err != nil {
		return "", err
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
	reqURL, useResponses := c.requestURL()
	if useResponses {
		return c.chatResponses(reqURL, messages, true, onChunk)
	}

	reqBody := openaiChatRequest{
		Model:    c.cfg.Model,
		Messages: messages,
		Stream:   true,
	}

	return postStream(c.httpClient, reqURL, bearerHeaders(c.cfg.APIKey), reqBody, "openai", parseOpenAIStreamEvent, onChunk)
}

func (c *openaiClient) chatResponses(reqURL string, messages []store.Message, stream bool, onChunk func(chunk string) error) (string, error) {
	input := make([]store.Message, 0, len(messages))
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

	if stream {
		return postStream(c.httpClient, reqURL, bearerHeaders(c.cfg.APIKey), reqBody, "responses", parseResponsesStreamEvent, onChunk)
	}

	body, err := postJSON(c.httpClient, reqURL, bearerHeaders(c.cfg.APIKey), reqBody, "responses")
	if err != nil {
		return "", err
	}

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

func parseOpenAIStreamEvent(data string) string {
	var chunk openaiStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return ""
	}
	if len(chunk.Choices) == 0 {
		return ""
	}
	return chunk.Choices[0].Delta.Content
}

func parseResponsesStreamEvent(data string) string {
	var event responsesStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return ""
	}
	if event.Type != "response.output_text.delta" {
		return ""
	}
	return event.Delta
}

// --- Anthropic client ---

type anthropicClient struct {
	cfg        Config
	httpClient *http.Client
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content,omitempty"`
	Text    string             `json:"text,omitempty"` // for user messages in older format
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	Stream    bool               `json:"stream"`
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
		msgs = append(msgs, anthropicMessage{
			Role:    m.Role,
			Content: []anthropicContent{{Type: "text", Text: m.Content}},
		})
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

	reqURL := c.requestURL()
	body, err := postJSON(c.httpClient, reqURL, anthropicHeaders(c.cfg.APIKey), reqBody, "anthropic")
	if err != nil {
		return "", err
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

	reqURL := c.requestURL()
	return postStream(c.httpClient, reqURL, anthropicHeaders(c.cfg.APIKey), reqBody, "anthropic", parseAnthropicStreamEvent, onChunk)
}

func (c *anthropicClient) requestURL() string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	endpoint := c.cfg.Endpoint
	if endpoint == "" || endpoint == "messages" {
		return base + "/v1/messages"
	}
	if strings.HasPrefix(endpoint, "/") {
		return base + endpoint
	}
	return base + "/v1/" + endpoint
}

func anthropicHeaders(apiKey string) http.Header {
	h := http.Header{}
	if apiKey != "" {
		h.Set("x-api-key", apiKey)
	}
	h.Set("anthropic-version", "2023-06-01")
	return h
}

func parseAnthropicStreamEvent(data string) string {
	var event anthropicStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return ""
	}

	switch event.Type {
	case "content_block_delta":
		return event.Delta.Text
	case "content_block_start":
		return event.ContentBlock.Text
	default:
		return ""
	}
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
