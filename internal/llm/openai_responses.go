package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"lingobridge/internal/logging"
	"lingobridge/internal/store"
)

var openAILog = logging.For("openai")

type openaiResponsesClient struct {
	openaiBase
}

func (c *openaiResponsesClient) PrepareUserMessage(content string, attachments []InputAttachment) (store.Message, error) {
	if len(attachments) == 0 {
		return store.Message{Role: "user", Content: content}, nil
	}

	msg := store.Message{Role: "user", Content: content}
	for _, attachment := range attachments {
		if attachment.Type != "image" {
			return store.Message{}, fmt.Errorf("%w: %s", ErrUnsupportedAttachment, attachment.Type)
		}
		fileID, err := c.uploadVisionFile(attachment.Filename, attachment.Data)
		if err != nil {
			return store.Message{}, err
		}
		size := attachment.Size
		if size == 0 {
			size = len(attachment.Data)
		}
		msg.Attachments = append(msg.Attachments, store.Attachment{
			Type:        attachment.Type,
			MIMEType:    attachment.MIMEType,
			Filename:    attachment.Filename,
			Size:        size,
			RefProvider: c.refProvider(),
			RefType:     openAIRefTypeFile,
			RefID:       fileID,
			LocalPath:   attachment.LocalPath,
		})
	}
	return msg, nil
}

func (c *openaiResponsesClient) AssistantMessage(resp Response) (store.Message, error) {
	msg := store.Message{Role: "assistant", Content: responseHistoryContentWithoutImageData(resp)}
	for _, image := range resp.Images {
		mimeType, filename := imageHistoryMetadata(image)
		fileID, err := c.uploadVisionFile(filename, image.Data)
		if err != nil {
			openAILog.Warn(context.Background(), "responses image upload failed model=%s filename=%s: %v", c.cfg.Model, filename, err)
			fileID = ""
		}
		msg.Attachments = append(msg.Attachments, store.Attachment{
			Type:        "image",
			MIMEType:    mimeType,
			Filename:    filename,
			Size:        len(image.Data),
			RefProvider: c.refProvider(),
			RefType:     openAIRefTypeFile,
			RefID:       fileID,
			LocalPath:   image.LocalPath,
		})
	}
	return msg, nil
}

type responsesRequest struct {
	Model             string                       `json:"model"`
	Input             []any                        `json:"input"`
	Instructions      string                       `json:"instructions,omitempty"`
	Stream            bool                         `json:"stream"`
	Store             bool                         `json:"store"`
	ContextManagement []responsesContextManagement `json:"context_management,omitempty"`
}

type responsesContextManagement struct {
	Type             string `json:"type"`
	CompactThreshold int    `json:"compact_threshold"`
}

type responsesCompactRequest struct {
	Model        string `json:"model"`
	Input        []any  `json:"input"`
	Instructions string `json:"instructions,omitempty"`
}

type responsesCompactOutput struct {
	Output []json.RawMessage `json:"output"`
}

type responsesInputMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type responsesInputContent struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	FileID string `json:"file_id,omitempty"`
}

type responsesOutput struct {
	Output    []responsesOutputItem `json:"output"`
	RawOutput []json.RawMessage     `json:"-"`
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

func (o *responsesOutput) UnmarshalJSON(data []byte) error {
	var raw struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	o.RawOutput = raw.Output
	o.Output = make([]responsesOutputItem, 0, len(raw.Output))
	for _, itemRaw := range raw.Output {
		var meta struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(itemRaw, &meta); err != nil {
			return err
		}
		if meta.Type == openAIRefTypeCompaction {
			o.Output = append(o.Output, responsesOutputItem{Type: meta.Type})
			continue
		}
		var item responsesOutputItem
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			return err
		}
		o.Output = append(o.Output, item)
	}
	return nil
}

func (c *openaiResponsesClient) Chat(systemPrompt string, messages []store.Message) (Response, error) {
	return c.chatResponses(systemPrompt, messages, store.ProviderContext{}, CompactConfig{}, false, nil)
}

func (c *openaiResponsesClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error) {
	return c.chatResponses(systemPrompt, messages, store.ProviderContext{}, CompactConfig{}, true, onChunk)
}

func (c *openaiResponsesClient) ChatStreamWithContext(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig, onChunk func(chunk string) error) (Response, error) {
	return c.chatResponses(systemPrompt, messages, providerContext, compact, true, onChunk)
}

func (c *openaiResponsesClient) CompactContext(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig) (store.ProviderContext, error) {
	input, err := c.convertToResponsesInputWithContext(messages, providerContext)
	if err != nil {
		return store.ProviderContext{}, err
	}
	if len(input) == 0 {
		return providerContext, nil
	}

	reqBody := responsesCompactRequest{
		Model:        c.cfg.Model,
		Input:        input,
		Instructions: openAIInstructions(systemPrompt, compact.Instructions),
	}
	body, err := postJSON(c.httpClient, c.responsesCompactURL(), bearerHeaders(c.cfg.APIKey), reqBody, "responses compact")
	if err != nil {
		return store.ProviderContext{}, err
	}

	var out responsesCompactOutput
	if err := json.Unmarshal(body, &out); err != nil {
		return store.ProviderContext{}, fmt.Errorf("unmarshal responses compact: %w", err)
	}
	if len(out.Output) == 0 {
		return store.ProviderContext{}, fmt.Errorf("responses compact returned no output")
	}
	return store.ProviderContext{
		Provider: c.refProvider(),
		Endpoint: openAIEndpointResponses,
		Items:    out.Output,
	}, nil
}

func (c *openaiResponsesClient) chatResponses(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig, stream bool, onChunk func(chunk string) error) (Response, error) {
	input, err := c.convertToResponsesInputWithContext(messages, providerContext)
	if err != nil {
		return Response{}, err
	}

	reqBody := responsesRequest{
		Model:             c.cfg.Model,
		Input:             input,
		Instructions:      systemPrompt,
		Stream:            stream,
		Store:             false,
		ContextManagement: openAIContextManagement(compact),
	}

	if stream {
		return postResponsesStream(c.httpClient, c.responsesURL(), bearerHeaders(c.cfg.APIKey), reqBody, onChunk)
	}

	body, err := postJSON(c.httpClient, c.responsesURL(), bearerHeaders(c.cfg.APIKey), reqBody, "responses")
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
	if resp.Text == "" && len(resp.Images) == 0 && resp.ProviderContext.IsEmpty() {
		return Response{}, fmt.Errorf("no output_text or image_generation_call in responses")
	}
	return resp, nil
}

func (c *openaiResponsesClient) convertToResponsesInput(messages []store.Message) ([]any, error) {
	return c.convertToResponsesInputWithContext(messages, store.ProviderContext{})
}

func (c *openaiResponsesClient) convertToResponsesInputWithContext(messages []store.Message, providerContext store.ProviderContext) ([]any, error) {
	input := make([]any, 0, len(messages))
	if providerContext.Provider == c.refProvider() && providerContext.Endpoint == openAIEndpointResponses {
		for _, item := range providerContext.Items {
			if len(item) > 0 {
				input = append(input, item)
			}
		}
	}
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		message, err := c.responsesInputItems(m)
		if err != nil {
			return nil, err
		}
		if message != nil {
			input = append(input, *message)
		}
	}
	return input, nil
}

func openAIInstructions(systemPrompt, compactInstructions string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	compactInstructions = strings.TrimSpace(compactInstructions)
	if systemPrompt == "" {
		return compactInstructions
	}
	if compactInstructions == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\nContext compaction instructions:\n" + compactInstructions
}

func openAIContextManagement(compact CompactConfig) []responsesContextManagement {
	if compact.Mode == "false" || compact.ContextWindow <= 0 || compact.Threshold <= 0 {
		return nil
	}
	return []responsesContextManagement{{
		Type:             openAIRefTypeCompaction,
		CompactThreshold: compactThresholdTokens(compact),
	}}
}

func compactThresholdTokens(compact CompactConfig) int {
	return int(math.Ceil(float64(compact.ContextWindow) * compact.Threshold))
}

func (c *openaiResponsesClient) responsesInputItems(m store.Message) (*responsesInputMessage, error) {
	content, err := c.responsesMessageContent(m)
	if err != nil {
		return nil, err
	}
	if content == nil {
		return nil, nil
	}
	return &responsesInputMessage{Role: m.Role, Content: content}, nil
}

func (c *openaiResponsesClient) responsesMessageContent(m store.Message) (any, error) {
	if len(m.Attachments) == 0 {
		return m.Content, nil
	}

	parts := make([]responsesInputContent, 0, len(m.Attachments)+1)
	if m.Content != "" {
		parts = append(parts, responsesInputContent{Type: responsesTextPartType(m.Role), Text: m.Content})
	}
	for _, attachment := range m.Attachments {
		if attachment.Type != "image" {
			return nil, fmt.Errorf("%w: unsupported attachment type %q", ErrUnsupportedAttachment, attachment.Type)
		}
		if attachment.RefProvider == c.refProvider() && attachment.RefType == openAIRefTypeFile {
			if attachment.RefID != "" {
				parts = append(parts, responsesInputContent{Type: "input_image", FileID: attachment.RefID})
			}
			continue
		}
		if attachment.RefProvider == c.refProvider() && attachment.RefType == openAIRefTypeImageGenerationCall {
			continue
		}
		return nil, fmt.Errorf("%w: image attachment missing %s file reference", ErrUnsupportedAttachment, c.refProvider())
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return parts, nil
}

func responsesTextPartType(role string) string {
	if role == "assistant" {
		return "output_text"
	}
	return "input_text"
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
	for i, item := range out.Output {
		if item.Type == openAIRefTypeCompaction {
			appendResponsesCompaction(&resp, rawResponsesOutputItem(out, i))
			continue
		}
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
				Type:   openAIRefTypeImageGenerationCall,
				Status: "completed",
				Result: event.Result,
			}, seenImages, false); err != nil {
				return false, err
			}
		case "response.completed":
			includeText := textBuilder.Len() == 0
			for i, item := range event.Response.Output {
				if item.Type == openAIRefTypeCompaction {
					appendResponsesCompaction(&resp, rawResponsesOutputItem(event.Response, i))
					continue
				}
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

func rawResponsesOutputItem(out responsesOutput, index int) json.RawMessage {
	if index >= 0 && index < len(out.RawOutput) && len(out.RawOutput[index]) > 0 {
		return out.RawOutput[index]
	}
	if index >= 0 && index < len(out.Output) {
		raw, _ := json.Marshal(out.Output[index])
		return raw
	}
	return nil
}

func appendResponsesCompaction(resp *Response, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	resp.ProviderContext.Provider = openAIRefProvider
	resp.ProviderContext.Endpoint = openAIEndpointResponses
	resp.ProviderContext.Items = append(resp.ProviderContext.Items, raw)
	resp.Compacted = true
}

func rememberResponsesImageItem(item responsesOutputItem, knownImageItems map[string]bool) {
	if item.ID != "" && item.Type == openAIRefTypeImageGenerationCall {
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
	return item.Type == openAIRefTypeImageGenerationCall
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
		Reference: AttachmentRef{
			Provider: openAIRefProvider,
			Type:     openAIRefTypeImageGenerationCall,
			ID:       item.ID,
		},
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
