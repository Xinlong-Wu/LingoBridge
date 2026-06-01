package llm

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"wechatbox/internal/store"
)

// Client is the common LLM client interface.
type Client interface {
	// Chat sends messages and returns the full response.
	Chat(systemPrompt string, messages []store.Message) (Response, error)
	// ChatStream sends messages and streams the response via callback.
	// The callback receives incremental text chunks.
	ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error)
}

// Response is the common LLM response shape across providers.
type Response struct {
	Text   string
	Images []Image
}

// Image is a generated image returned by an LLM provider.
type Image struct {
	Data     []byte
	MIMEType string
	Filename string
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

func postResponsesStream(client *http.Client, reqURL string, headers http.Header, reqBody any, onChunk func(chunk string) error) (Response, error) {
	resp, err := sendJSON(client, reqURL, headers, reqBody)
	if err != nil {
		return Response{}, fmt.Errorf("responses stream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Response{}, fmt.Errorf("responses stream HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	return parseResponsesSSE(resp.Body, onChunk)
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
	if len(line) <= maxSSERawLogLen {
		log.Printf("[llm] SSE rawdata: %s", line)
		return
	}
	log.Printf("[llm] SSE rawdata: %s... (truncated, len=%d)", line[:maxSSERawLogLen], len(line))
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
	Output []responsesOutputItem `json:"output"`
}

type responsesOutputItem struct {
	ID      string                    `json:"id"`
	Type    string                    `json:"type"`
	Status  string                    `json:"status"`
	Content []responsesOutputItemPart `json:"content"`
	Result  string                    `json:"result"`
}

type responsesOutputItemPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesStreamEvent struct {
	Type     string              `json:"type"`
	Delta    string              `json:"delta"`
	Item     responsesOutputItem `json:"item"`
	ItemID   string              `json:"item_id"`
	Result   string              `json:"result"`
	Response responsesOutput     `json:"response"`
}

func (c *openaiClient) Chat(systemPrompt string, messages []store.Message) (Response, error) {
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
		return Response{}, err
	}

	var chatResp openaiChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(chatResp.Choices) > 0 {
		return Response{Text: chatResp.Choices[0].Message.Content}, nil
	}
	return Response{}, fmt.Errorf("no choices in response")
}

func (c *openaiClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error) {
	reqURL, useResponses := c.requestURL()
	if useResponses {
		return c.chatResponses(reqURL, messages, true, onChunk)
	}

	reqBody := openaiChatRequest{
		Model:    c.cfg.Model,
		Messages: messages,
		Stream:   true,
	}

	text, err := postStream(c.httpClient, reqURL, bearerHeaders(c.cfg.APIKey), reqBody, "openai", parseOpenAIStreamEvent, onChunk)
	if err != nil {
		return Response{}, err
	}
	return Response{Text: text}, nil
}

func (c *openaiClient) chatResponses(reqURL string, messages []store.Message, stream bool, onChunk func(chunk string) error) (Response, error) {
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
		return postResponsesStream(c.httpClient, reqURL, bearerHeaders(c.cfg.APIKey), reqBody, onChunk)
	}

	body, err := postJSON(c.httpClient, reqURL, bearerHeaders(c.cfg.APIKey), reqBody, "responses")
	if err != nil {
		return Response{}, err
	}

	var out responsesOutput
	if err := json.Unmarshal(body, &out); err != nil {
		return Response{}, fmt.Errorf("unmarshal responses: %w", err)
	}
	resp, err := parseResponsesOutput(out)
	if err != nil {
		return Response{}, err
	}
	if resp.Text == "" && len(resp.Images) == 0 {
		return Response{}, fmt.Errorf("no output_text or image_generation_call in responses")
	}
	return resp, nil
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

func parseResponsesOutput(out responsesOutput) (Response, error) {
	var resp Response
	seenImages := map[string]bool{}
	for _, item := range out.Output {
		if err := appendResponsesOutputItem(&resp, item, seenImages, true); err != nil {
			return Response{}, err
		}
	}
	return resp, nil
}

func parseResponsesSSE(body io.Reader, onChunk func(chunk string) error) (Response, error) {
	var resp Response
	seenImages := map[string]bool{}
	knownImageItems := map[string]bool{}
	var textBuilder strings.Builder

	err := readSSEData(body, func(data string) (bool, error) {
		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return false, nil
		}

		switch event.Type {
		case "response.output_text.delta":
			if event.Delta == "" {
				return false, nil
			}
			textBuilder.WriteString(event.Delta)
			resp.Text = textBuilder.String()
			if onChunk != nil {
				if err := onChunk(event.Delta); err != nil {
					return false, err
				}
			}
		case "response.output_item.added":
			rememberResponsesImageItem(event.Item, knownImageItems)
		case "response.output_item.done":
			rememberResponsesImageItem(event.Item, knownImageItems)
			if err := appendResponsesOutputItem(&resp, event.Item, seenImages, false); err != nil {
				return false, err
			}
		case "response.image_generation_call.completed":
			if event.ItemID != "" {
				knownImageItems[event.ItemID] = true
			}
			if err := appendResponsesOutputItem(&resp, responsesOutputItem{
				ID:     event.ItemID,
				Type:   "image_generation_call",
				Status: "completed",
				Result: event.Result,
			}, seenImages, false); err != nil {
				return false, err
			}
		case "response.completed":
			includeText := textBuilder.Len() == 0
			for _, item := range event.Response.Output {
				if item.ID != "" && knownImageItems[item.ID] && item.Result != "" {
					if err := appendResponsesImage(&resp, item, seenImages); err != nil {
						return false, err
					}
					continue
				}
				if err := appendResponsesOutputItem(&resp, item, seenImages, includeText); err != nil {
					return false, err
				}
			}
		}
		return false, nil
	})
	if err != nil {
		return Response{}, err
	}
	return resp, nil
}

func rememberResponsesImageItem(item responsesOutputItem, knownImageItems map[string]bool) {
	if item.ID != "" && item.Type == "image_generation_call" {
		knownImageItems[item.ID] = true
	}
}

func appendResponsesOutputItem(resp *Response, item responsesOutputItem, seenImages map[string]bool, includeText bool) error {
	if item.Type == "message" {
		if !includeText {
			return nil
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				resp.Text += part.Text
			}
		}
		return nil
	}

	if isResponsesImageItem(item) {
		if !isFinalResponsesImage(item.Status) {
			return nil
		}
		return appendResponsesImage(resp, item, seenImages)
	}
	return nil
}

func isResponsesImageItem(item responsesOutputItem) bool {
	return item.Type == "image_generation_call"
}

func isFinalResponsesImage(status string) bool {
	return status == "" || status == "completed"
}

func appendResponsesImage(resp *Response, item responsesOutputItem, seenImages map[string]bool) error {
	if item.Result == "" {
		return nil
	}
	imageKey := item.ID
	if imageKey == "" {
		imageKey = item.Result
	}
	if seenImages[imageKey] {
		return nil
	}
	imageData, err := decodeResponseImageResult(item.Result)
	if err != nil {
		return err
	}
	seenImages[imageKey] = true
	resp.Images = append(resp.Images, Image{
		Data:     imageData,
		MIMEType: "image/png",
		Filename: "openai-response-image.png",
	})
	return nil
}

func decodeResponseImageResult(result string) ([]byte, error) {
	raw := result
	if strings.HasPrefix(raw, "data:") {
		if comma := strings.Index(raw, ","); comma >= 0 {
			raw = raw[comma+1:]
		}
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode response image result: %w", err)
	}
	return data, nil
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

func (c *anthropicClient) Chat(systemPrompt string, messages []store.Message) (Response, error) {
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
		return Response{}, err
	}

	var chatResp anthropicResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(chatResp.Content) > 0 {
		return Response{Text: chatResp.Content[0].Text}, nil
	}
	return Response{}, fmt.Errorf("no content in response")
}

func (c *anthropicClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error) {
	anthropicMsgs, system := convertToAnthropicMessages(messages, systemPrompt)

	reqBody := anthropicRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
		System:    system,
		Stream:    true,
	}

	reqURL := c.requestURL()
	text, err := postStream(c.httpClient, reqURL, anthropicHeaders(c.cfg.APIKey), reqBody, "anthropic", parseAnthropicStreamEvent, onChunk)
	if err != nil {
		return Response{}, err
	}
	return Response{Text: text}, nil
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
