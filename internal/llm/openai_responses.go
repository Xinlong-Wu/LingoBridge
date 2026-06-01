package llm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"wechatbox/internal/store"
)

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
		})
	}
	return msg, nil
}

func (c *openaiResponsesClient) AssistantMessage(resp Response) store.Message {
	msg := store.Message{Role: "assistant", Content: responseHistoryContentWithoutImageData(resp)}
	for _, image := range resp.Images {
		if !c.isRef(image.Reference, openAIRefTypeImageGenerationCall) {
			continue
		}
		mimeType, filename := imageHistoryMetadata(image)
		msg.Attachments = append(msg.Attachments, store.Attachment{
			Type:        "image",
			MIMEType:    mimeType,
			Filename:    filename,
			Size:        len(image.Data),
			RefProvider: image.Reference.Provider,
			RefType:     image.Reference.Type,
			RefID:       image.Reference.ID,
		})
	}
	return msg
}

type responsesRequest struct {
	Model  string `json:"model"`
	Input  []any  `json:"input"`
	Stream bool   `json:"stream"`
	Store  bool   `json:"store"`
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

type responsesInputImageGenerationCall struct {
	Type string `json:"type"`
	ID   string `json:"id"`
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

func (c *openaiResponsesClient) Chat(systemPrompt string, messages []store.Message) (Response, error) {
	return c.chatResponses(messages, false, nil)
}

func (c *openaiResponsesClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error) {
	return c.chatResponses(messages, true, onChunk)
}

func (c *openaiResponsesClient) chatResponses(messages []store.Message, stream bool, onChunk func(chunk string) error) (Response, error) {
	input, err := c.convertToResponsesInput(messages)
	if err != nil {
		return Response{}, err
	}

	reqBody := responsesRequest{
		Model:  c.cfg.Model,
		Input:  input,
		Stream: stream,
		Store:  true,
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
	if resp.Text == "" && len(resp.Images) == 0 {
		return Response{}, fmt.Errorf("no output_text or image_generation_call in responses")
	}
	return resp, nil
}

func (c *openaiResponsesClient) convertToResponsesInput(messages []store.Message) ([]any, error) {
	input := make([]any, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		message, generationCalls, err := c.responsesInputItems(m)
		if err != nil {
			return nil, err
		}
		if message != nil {
			input = append(input, *message)
		}
		for _, call := range generationCalls {
			input = append(input, call)
		}
	}
	return input, nil
}

func (c *openaiResponsesClient) responsesInputItems(m store.Message) (*responsesInputMessage, []responsesInputImageGenerationCall, error) {
	content, generationCalls, err := c.responsesMessageContent(m)
	if err != nil {
		return nil, nil, err
	}
	if content == nil {
		return nil, generationCalls, nil
	}
	return &responsesInputMessage{Role: m.Role, Content: content}, generationCalls, nil
}

func (c *openaiResponsesClient) responsesMessageContent(m store.Message) (any, []responsesInputImageGenerationCall, error) {
	if len(m.Attachments) == 0 {
		return m.Content, nil, nil
	}

	parts := make([]responsesInputContent, 0, len(m.Attachments)+1)
	var generationCalls []responsesInputImageGenerationCall
	if m.Content != "" {
		parts = append(parts, responsesInputContent{Type: responsesTextPartType(m.Role), Text: m.Content})
	}
	for _, attachment := range m.Attachments {
		if attachment.Type != "image" {
			return nil, nil, fmt.Errorf("%w: unsupported attachment type %q", ErrUnsupportedAttachment, attachment.Type)
		}
		if c.isStoreRef(attachment, openAIRefTypeFile) {
			parts = append(parts, responsesInputContent{Type: "input_image", FileID: attachment.RefID})
			continue
		}
		if c.isStoreRef(attachment, openAIRefTypeImageGenerationCall) {
			generationCalls = append(generationCalls, responsesInputImageGenerationCall{
				Type: openAIRefTypeImageGenerationCall,
				ID:   attachment.RefID,
			})
			continue
		}
		return nil, nil, fmt.Errorf("%w: image attachment missing %s file or image_generation_call reference", ErrUnsupportedAttachment, c.refProvider())
	}
	if len(parts) == 0 && len(generationCalls) == 0 {
		return nil, nil, fmt.Errorf("%w: message has attachments but no supported content", ErrUnsupportedAttachment)
	}
	if len(parts) == 0 {
		return nil, generationCalls, nil
	}
	return parts, generationCalls, nil
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
				Type:   openAIRefTypeImageGenerationCall,
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
