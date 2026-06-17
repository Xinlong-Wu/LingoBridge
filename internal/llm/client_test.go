package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lingobridge/internal/store"
	tooltypes "lingobridge/internal/tools"
)

type retryableNetError struct{}

func (retryableNetError) Error() string   { return "temporary network timeout" }
func (retryableNetError) Timeout() bool   { return true }
func (retryableNetError) Temporary() bool { return false }

func TestParseSSE(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		parser streamParser
		want   string
	}{
		{
			name: "openai chat stream",
			input: strings.Join([]string{
				"data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}",
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}",
				"data: [DONE]",
			}, "\n"),
			parser: parseOpenAIStreamEvent,
			want:   "hello",
		},
		{
			name: "responses stream",
			input: strings.Join([]string{
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}",
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\" there\"}",
				"data: [DONE]",
			}, "\n"),
			parser: parseResponsesStreamEvent,
			want:   "hi there",
		},
		{
			name: "anthropic stream",
			input: strings.Join([]string{
				"data: {\"type\":\"content_block_start\",\"content_block\":{\"text\":\"he\"}}",
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"llo\"}}",
			}, "\n"),
			parser: parseAnthropicStreamEvent,
			want:   "hello",
		},
		{
			name: "malformed event ignored",
			input: strings.Join([]string{
				"data: {not-json",
				"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}",
			}, "\n"),
			parser: parseOpenAIStreamEvent,
			want:   "ok",
		},
		{
			name:   "last line without newline",
			input:  "data: {\"choices\":[{\"delta\":{\"content\":\"tail\"}}]}",
			parser: parseOpenAIStreamEvent,
			want:   "tail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSSE(strings.NewReader(tt.input), tt.parser, nil)
			if err != nil {
				t.Fatalf("parseSSE returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseSSE = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenAIResponsesHTTPErrorIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
		w.Write([]byte(`<html>gateway timeout</html>`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	_, err := client.Chat("system", []store.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("Chat returned nil error, want HTTP 504")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T %v, want HTTPError", err, err)
	}
	if httpErr.Label != "responses" || httpErr.StatusCode != http.StatusGatewayTimeout || !strings.Contains(httpErr.Body, "gateway timeout") {
		t.Fatalf("HTTPError = %#v, want responses 504 body", httpErr)
	}
	if !IsRetryableError(err) {
		t.Fatalf("IsRetryableError(%v) = false, want true", err)
	}
	if !strings.Contains(err.Error(), "responses HTTP 504") {
		t.Fatalf("error string = %q, want responses HTTP 504", err.Error())
	}
}

func TestIsRetryableErrorClassifiesHTTPAndNetworkErrors(t *testing.T) {
	retryableStatuses := []int{408, 409, 425, 429, 500, 502, 503, 504}
	for _, status := range retryableStatuses {
		err := &HTTPError{Label: "responses", StatusCode: status, Body: "transient"}
		if !IsRetryableError(err) {
			t.Fatalf("status %d retryable = false, want true", status)
		}
	}
	nonRetryableStatuses := []int{400, 401, 403, 404, 422}
	for _, status := range nonRetryableStatuses {
		err := &HTTPError{Label: "responses", StatusCode: status, Body: "bad request"}
		if IsRetryableError(err) {
			t.Fatalf("status %d retryable = true, want false", status)
		}
	}
	if !IsRetryableError(fmt.Errorf("responses request: %w", retryableNetError{})) {
		t.Fatal("network timeout retryable = false, want true")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if IsRetryableError(err) {
			t.Fatalf("%v retryable = true, want false", err)
		}
	}
}

func TestParseSSECallbackError(t *testing.T) {
	errStop := errors.New("stop")
	input := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}",
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}",
	}, "\n")

	got, err := parseSSE(strings.NewReader(input), parseOpenAIStreamEvent, func(chunk string) error {
		if chunk == "two" {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("parseSSE error = %v, want %v", err, errStop)
	}
	if got != "onetwo" {
		t.Fatalf("parseSSE partial text = %q, want %q", got, "onetwo")
	}
}

func TestParseSSELongLine(t *testing.T) {
	input := "data: {\"type\":\"keepalive\",\"blob\":\"" + strings.Repeat("x", 2*1024*1024) + "\"}\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}"

	got, err := parseSSE(strings.NewReader(input), parseResponsesStreamEvent, nil)
	if err != nil {
		t.Fatalf("parseSSE returned error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("parseSSE = %q, want ok", got)
	}
}

func TestConvertToResponsesInputWithImageFileID(t *testing.T) {
	client := &openaiResponsesClient{}
	input, err := client.convertToResponsesInput([]store.Message{
		{Role: "system", Content: "system"},
		{
			Role:    "user",
			Content: "what is this?",
			Attachments: []store.Attachment{
				{Type: "image", RefProvider: "openai", RefType: "file", RefID: "file_123", MIMEType: "image/png"},
			},
		},
	})
	if err != nil {
		t.Fatalf("convertToResponsesInput returned error: %v", err)
	}
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1", len(input))
	}
	msg, ok := input[0].(responsesInputMessage)
	if !ok {
		t.Fatalf("input[0] type = %T, want responsesInputMessage", input[0])
	}
	parts, ok := msg.Content.([]responsesInputContent)
	if !ok {
		t.Fatalf("content type = %T, want []responsesInputContent", msg.Content)
	}
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(parts))
	}
	if parts[0].Type != "input_text" || parts[0].Text != "what is this?" {
		t.Fatalf("text part = %#v", parts[0])
	}
	if parts[1].Type != "input_image" || parts[1].FileID != "file_123" {
		t.Fatalf("image part = %#v", parts[1])
	}
}

func TestConvertToResponsesInputSkipsLegacyImageGenerationRef(t *testing.T) {
	client := &openaiResponsesClient{}
	input, err := client.convertToResponsesInput([]store.Message{
		{
			Role:    "assistant",
			Content: "[图片: mime=image/png filename=image.png]",
			Attachments: []store.Attachment{
				{Type: "image", RefProvider: "openai", RefType: "image_generation_call", RefID: "ig_123", MIMEType: "image/png"},
			},
		},
		{Role: "user", Content: "make it brighter"},
	})
	if err != nil {
		t.Fatalf("convertToResponsesInput returned error: %v", err)
	}
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2", len(input))
	}
	if _, ok := input[0].(responsesInputMessage); !ok {
		t.Fatalf("input[0] type = %T, want responsesInputMessage", input[0])
	}
	msg := input[0].(responsesInputMessage)
	parts, ok := msg.Content.([]responsesInputContent)
	if !ok {
		t.Fatalf("assistant content type = %T, want []responsesInputContent", msg.Content)
	}
	if len(parts) != 1 || parts[0].Type != "output_text" {
		t.Fatalf("assistant text parts = %#v, want output_text", parts)
	}
	if msg, ok := input[1].(responsesInputMessage); !ok || msg.Role != "user" {
		t.Fatalf("input[1] = %#v, want user message", input[1])
	}
}

func TestConvertToResponsesInputRequiresImageFileID(t *testing.T) {
	client := &openaiResponsesClient{}
	_, err := client.convertToResponsesInput([]store.Message{
		{Role: "user", Attachments: []store.Attachment{{Type: "image"}}},
	})
	if !errors.Is(err, ErrUnsupportedAttachment) || !strings.Contains(err.Error(), "missing openai file reference") {
		t.Fatalf("convertToResponsesInput error = %v, want missing image reference", err)
	}
}

func TestConvertToResponsesInputSkipsEmptyImageFileID(t *testing.T) {
	client := &openaiResponsesClient{}
	input, err := client.convertToResponsesInput([]store.Message{
		{
			Role:    "assistant",
			Content: "[图片: mime=image/png filename=assistant-1.png]",
			Attachments: []store.Attachment{
				{Type: "image", RefProvider: "openai", RefType: "file", RefID: "", MIMEType: "image/png", LocalPath: "media/user/session/assistant-1.png"},
			},
		},
		{Role: "user", Content: "describe it"},
	})
	if err != nil {
		t.Fatalf("convertToResponsesInput returned error: %v", err)
	}
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2", len(input))
	}
	msg, ok := input[0].(responsesInputMessage)
	if !ok {
		t.Fatalf("input[0] type = %T, want responsesInputMessage", input[0])
	}
	parts, ok := msg.Content.([]responsesInputContent)
	if !ok {
		t.Fatalf("assistant content type = %T, want []responsesInputContent", msg.Content)
	}
	if len(parts) != 1 || parts[0].Type != "output_text" {
		t.Fatalf("assistant parts = %#v, want only output_text", parts)
	}
}

func TestConvertToOpenAIChatMessagesRejectsAttachments(t *testing.T) {
	_, err := convertToOpenAIChatMessages([]store.Message{
		{Role: "user", Content: "hi", Attachments: []store.Attachment{{Type: "image", RefProvider: "openai", RefType: "file", RefID: "file_123"}}},
	})
	if !errors.Is(err, ErrUnsupportedAttachment) {
		t.Fatalf("convertToOpenAIChatMessages error = %v, want ErrUnsupportedAttachment", err)
	}
}

func TestUploadOpenAIVisionFile(t *testing.T) {
	var gotPurpose, gotFilename string
	var gotFile []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			t.Fatalf("path = %q, want /v1/files", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotPurpose = r.FormValue("purpose")
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		gotFilename = header.Filename
		gotFile, err = io.ReadAll(file)
		if err != nil {
			t.Fatalf("ReadAll file: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"file_abc"}`))
	}))
	defer server.Close()

	fileID, err := uploadOpenAIVisionFile(server.Client(), server.URL, "test-key", "image.png", []byte("image-bytes"))
	if err != nil {
		t.Fatalf("uploadOpenAIVisionFile returned error: %v", err)
	}
	if fileID != "file_abc" {
		t.Fatalf("fileID = %q, want file_abc", fileID)
	}
	if gotPurpose != "vision" {
		t.Fatalf("purpose = %q, want vision", gotPurpose)
	}
	if gotFilename != "image.png" || string(gotFile) != "image-bytes" {
		t.Fatalf("uploaded file = %q %q, want image.png image-bytes", gotFilename, gotFile)
	}
}

func TestOpenAIResponsesPrepareUserMessageUploadsImage(t *testing.T) {
	var gotFilename string
	var gotFile []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			t.Fatalf("path = %q, want /v1/files", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if got := r.FormValue("purpose"); got != "vision" {
			t.Fatalf("purpose = %q, want vision", got)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		gotFilename = header.Filename
		gotFile, err = io.ReadAll(file)
		if err != nil {
			t.Fatalf("ReadAll file: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"file_abc"}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "test-key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	msg, err := client.PrepareUserMessage("what is this?", []InputAttachment{{
		Type:      "image",
		MIMEType:  "image/png",
		Filename:  "image.png",
		Size:      len("image-bytes"),
		Data:      []byte("image-bytes"),
		LocalPath: "media/user/session/user-1.png",
	}})
	if err != nil {
		t.Fatalf("PrepareUserMessage returned error: %v", err)
	}
	if gotFilename != "image.png" || string(gotFile) != "image-bytes" {
		t.Fatalf("uploaded file = %q %q, want image.png image-bytes", gotFilename, gotFile)
	}
	if msg.Role != "user" || msg.Content != "what is this?" {
		t.Fatalf("message = %#v, want user message", msg)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one attachment", msg.Attachments)
	}
	attachment := msg.Attachments[0]
	if attachment.Type != "image" || attachment.MIMEType != "image/png" || attachment.Filename != "image.png" || attachment.Size != len("image-bytes") {
		t.Fatalf("attachment metadata = %#v", attachment)
	}
	if attachment.RefProvider != "openai" || attachment.RefType != "file" || attachment.RefID != "file_abc" {
		t.Fatalf("attachment ref = %#v, want openai file file_abc", attachment)
	}
	if attachment.LocalPath != "media/user/session/user-1.png" {
		t.Fatalf("attachment local path = %q, want persisted path", attachment.LocalPath)
	}
}

func TestOpenAIResponsesAssistantMessageUploadsGeneratedImage(t *testing.T) {
	var gotPurpose, gotFilename string
	var gotFile []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			t.Fatalf("path = %q, want /v1/files", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotPurpose = r.FormValue("purpose")
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		gotFilename = header.Filename
		gotFile, err = io.ReadAll(file)
		if err != nil {
			t.Fatalf("ReadAll file: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"file_generated"}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "test-key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	msg, err := client.AssistantMessage(Response{
		Text: "caption",
		Images: []Image{{
			Data:      []byte("image-bytes"),
			MIMEType:  "image/png",
			Filename:  "assistant-1.png",
			LocalPath: "media/user/session/assistant-1.png",
			Reference: AttachmentRef{
				Provider: "openai",
				Type:     "image_generation_call",
				ID:       "ig_123",
			},
		}},
	})
	if err != nil {
		t.Fatalf("AssistantMessage returned error: %v", err)
	}

	if strings.Contains(msg.Content, "base64=") {
		t.Fatalf("content contains base64: %q", msg.Content)
	}
	if gotPurpose != "vision" || gotFilename != "assistant-1.png" || string(gotFile) != "image-bytes" {
		t.Fatalf("uploaded file purpose=%q filename=%q data=%q", gotPurpose, gotFilename, gotFile)
	}
	if got := msg.Content; got != "caption\n[图片: mime=image/png filename=assistant-1.png]" {
		t.Fatalf("content = %q", got)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one image attachment", msg.Attachments)
	}
	attachment := msg.Attachments[0]
	if attachment.RefProvider != "openai" || attachment.RefType != "file" || attachment.RefID != "file_generated" {
		t.Fatalf("attachment ref = %#v, want openai file file_generated", attachment)
	}
	if attachment.LocalPath != "media/user/session/assistant-1.png" {
		t.Fatalf("attachment local path = %q, want persisted path", attachment.LocalPath)
	}
}

func TestOpenAIResponsesAssistantMessageKeepsImageWhenUploadFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			t.Fatalf("path = %q, want /v1/files", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upload failed"}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "test-key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	msg, err := client.AssistantMessage(Response{
		Text: "caption",
		Images: []Image{{
			Data:      []byte("image-bytes"),
			MIMEType:  "image/png",
			Filename:  "assistant-1.png",
			LocalPath: "media/user/session/assistant-1.png",
		}},
	})
	if err != nil {
		t.Fatalf("AssistantMessage returned error: %v", err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one image attachment", msg.Attachments)
	}
	attachment := msg.Attachments[0]
	if attachment.RefProvider != "openai" || attachment.RefType != "file" || attachment.RefID != "" {
		t.Fatalf("attachment ref = %#v, want openai file with empty ref id", attachment)
	}
	if attachment.LocalPath != "media/user/session/assistant-1.png" {
		t.Fatalf("attachment local path = %q, want persisted path", attachment.LocalPath)
	}
}

func TestPrepareUserMessageRejectsAttachmentsWithoutProviderSupport(t *testing.T) {
	attachment := []InputAttachment{{Type: "image", Data: []byte("image-bytes")}}
	if _, err := (&openaiChatClient{}).PrepareUserMessage("hi", attachment); !errors.Is(err, ErrUnsupportedAttachment) {
		t.Fatalf("openai chat PrepareUserMessage error = %v, want ErrUnsupportedAttachment", err)
	}
	if _, err := (&anthropicClient{}).PrepareUserMessage("hi", attachment); !errors.Is(err, ErrUnsupportedAttachment) {
		t.Fatalf("anthropic PrepareUserMessage error = %v, want ErrUnsupportedAttachment", err)
	}
}

func TestOpenAIResponsesRequestDisablesStore(t *testing.T) {
	var reqBody responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	resp, err := client.Chat("", []store.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("response text = %q, want ok", resp.Text)
	}
	if reqBody.Store {
		t.Fatal("responses request store = true, want false")
	}
}

func TestOpenAIResponsesRequestIncludesInstructions(t *testing.T) {
	var reqBody responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	if _, err := client.Chat("system prompt", []store.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if reqBody.Instructions != "system prompt" {
		t.Fatalf("instructions = %q, want system prompt", reqBody.Instructions)
	}
}

func TestOpenAIResponsesToolCallingRoundTrip(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, req)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			w.Write([]byte(`{"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"feishu_docs_search","arguments":"{\"query\":\"roadmap\"}"}]}`))
			return
		}
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{openaiBase: openaiBase{cfg: Config{BaseURL: server.URL, APIKey: "key", Model: "gpt-test"}, httpClient: server.Client()}}
	tools := []tooltypes.Spec{{Name: "feishu_docs_search", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}}
	first, err := client.ChatStreamWithTools("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{}, tools, ToolState{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithTools first returned error: %v", err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_1" || first.ToolCalls[0].Name != "feishu_docs_search" {
		t.Fatalf("tool calls = %#v, want one feishu_docs_search call", first.ToolCalls)
	}
	if len(bodies) != 1 || len(bodies[0]["tools"].([]any)) != 1 {
		t.Fatalf("request bodies = %#v, want tools in first request", bodies)
	}

	second, err := client.ChatStreamWithTools("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{}, tools, first.ToolState, []tooltypes.Result{{CallID: "call_1", Name: "feishu_docs_search", Content: `{"ok":true}`}}, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithTools second returned error: %v", err)
	}
	if second.Text != "done" {
		t.Fatalf("second text = %q, want done", second.Text)
	}
	input := bodies[1]["input"].([]any)
	foundOutput := false
	for _, item := range input {
		m, _ := item.(map[string]any)
		if m["type"] == "function_call_output" && m["call_id"] == "call_1" {
			foundOutput = true
		}
	}
	if !foundOutput {
		t.Fatalf("second input = %#v, want function_call_output", input)
	}
}

func TestOpenAIResponsesStreamingToolStateOmitsMessageContent(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, req)
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"feishu_docs_search\",\"arguments\":\"{\\\"query\\\":\\\"roadmap\\\"}\"}}\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{openaiBase: openaiBase{cfg: Config{BaseURL: server.URL, APIKey: "key", Model: "gpt-test"}, httpClient: server.Client()}}
	tools := []tooltypes.Spec{{Name: "feishu_docs_search", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}}
	first, err := client.ChatStreamWithTools("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{}, tools, ToolState{}, nil, func(string) error { return nil })
	if err != nil {
		t.Fatalf("ChatStreamWithTools first returned error: %v", err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %#v, want call_1", first.ToolCalls)
	}

	if _, err := client.ChatStreamWithTools("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{}, tools, first.ToolState, []tooltypes.Result{{CallID: "call_1", Name: "feishu_docs_search", Content: `{"ok":true}`}}, nil); err != nil {
		t.Fatalf("ChatStreamWithTools second returned error: %v", err)
	}
	input := bodies[1]["input"].([]any)
	var functionCall map[string]any
	for _, item := range input {
		m, _ := item.(map[string]any)
		if m["type"] == "function_call" && m["call_id"] == "call_1" {
			functionCall = m
			break
		}
	}
	if functionCall == nil {
		t.Fatalf("second input = %#v, want previous function_call item", input)
	}
	if _, ok := functionCall["content"]; ok {
		t.Fatalf("function_call input = %#v, did not want content field", functionCall)
	}
}

func TestOpenAIChatToolCallingRoundTrip(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, req)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"feishu_docs_search","arguments":"{\"query\":\"roadmap\"}"}}]}}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	client := &openaiChatClient{openaiBase: openaiBase{cfg: Config{BaseURL: server.URL, APIKey: "key", Model: "gpt-test"}, httpClient: server.Client()}}
	tools := []tooltypes.Spec{{Name: "feishu_docs_search", Parameters: json.RawMessage(`{"type":"object"}`)}}
	first, err := client.ChatStreamWithTools("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{}, tools, ToolState{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithTools first returned error: %v", err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %#v, want call_1", first.ToolCalls)
	}
	if len(bodies[0]["tools"].([]any)) != 1 {
		t.Fatalf("first body = %#v, want tools", bodies[0])
	}
	second, err := client.ChatStreamWithTools("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{}, tools, first.ToolState, []tooltypes.Result{{CallID: "call_1", Content: "ok"}}, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithTools second returned error: %v", err)
	}
	if second.Text != "done" {
		t.Fatalf("second text = %q, want done", second.Text)
	}
	messages := bodies[1]["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
		t.Fatalf("last message = %#v, want tool result", last)
	}
}

func TestAnthropicToolCallingRoundTrip(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, req)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			w.Write([]byte(`{"content":[{"type":"tool_use","id":"toolu_1","name":"feishu_docs_read","input":{"token":"dox_123"}}]}`))
			return
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"done"}]}`))
	}))
	defer server.Close()

	client := &anthropicClient{cfg: Config{BaseURL: server.URL, APIKey: "key", Model: "claude-test"}, httpClient: server.Client()}
	tools := []tooltypes.Spec{{Name: "feishu_docs_read", Parameters: json.RawMessage(`{"type":"object"}`)}}
	first, err := client.ChatStreamWithTools("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{}, tools, ToolState{}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithTools first returned error: %v", err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "toolu_1" || first.ToolCalls[0].Name != "feishu_docs_read" {
		t.Fatalf("tool calls = %#v, want toolu_1", first.ToolCalls)
	}
	if len(bodies[0]["tools"].([]any)) != 1 {
		t.Fatalf("first body = %#v, want tools", bodies[0])
	}
	second, err := client.ChatStreamWithTools("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{}, tools, first.ToolState, []tooltypes.Result{{CallID: "toolu_1", Content: "ok"}}, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithTools second returned error: %v", err)
	}
	if second.Text != "done" {
		t.Fatalf("second text = %q, want done", second.Text)
	}
	messages := bodies[1]["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	content := last["content"].([]any)
	result := content[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool result = %#v, want anthropic tool_result", result)
	}
}

func TestOpenAIResponsesRequestIncludesContextManagement(t *testing.T) {
	var reqBody responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	_, err := client.ChatStreamWithContext("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{
		Mode:          "auto",
		ContextWindow: 100,
		Threshold:     0.9,
	}, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithContext returned error: %v", err)
	}
	if len(reqBody.ContextManagement) != 1 {
		t.Fatalf("context_management = %#v, want one compact entry", reqBody.ContextManagement)
	}
	entry := reqBody.ContextManagement[0]
	if entry.Type != "compaction" || entry.CompactThreshold != 90 {
		t.Fatalf("context_management entry = %#v, want compaction threshold 90", entry)
	}
}

func TestOpenAIResponsesCompactContextRoundTripsOutput(t *testing.T) {
	var compactReq map[string]any
	var responseReq map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses/compact":
			if err := json.NewDecoder(r.Body).Decode(&compactReq); err != nil {
				t.Fatalf("decode compact request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"output":[{"type":"compaction","content":"old summary"}]}`))
		case "/v1/responses":
			if err := json.NewDecoder(r.Body).Decode(&responseReq); err != nil {
				t.Fatalf("decode responses request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	ctx, err := client.CompactContext("system", []store.Message{{Role: "user", Content: "old"}}, store.ProviderContext{}, CompactConfig{Instructions: "preserve decisions"})
	if err != nil {
		t.Fatalf("CompactContext returned error: %v", err)
	}
	if ctx.Provider != "openai" || ctx.Endpoint != "responses" || len(ctx.Items) != 1 {
		t.Fatalf("provider context = %#v, want one openai responses item", ctx)
	}
	if got := compactReq["instructions"].(string); !strings.Contains(got, "system") || !strings.Contains(got, "preserve decisions") {
		t.Fatalf("compact instructions = %q", got)
	}

	resp, err := client.chatResponses("system", []store.Message{{Role: "user", Content: "new"}}, ctx, CompactConfig{}, false, nil)
	if err != nil {
		t.Fatalf("chatResponses returned error: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("response text = %q, want ok", resp.Text)
	}
	input := responseReq["input"].([]any)
	first := input[0].(map[string]any)
	if first["type"] != "compaction" || first["content"] != "old summary" {
		t.Fatalf("first input = %#v, want compaction item", first)
	}
}

func TestOpenAIResponsesStreamWithContextRoundTripsOutput(t *testing.T) {
	var responseReq map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&responseReq); err != nil {
			t.Fatalf("decode responses request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	ctx := store.ProviderContext{
		Provider: "openai",
		Endpoint: "responses",
		Items:    []json.RawMessage{json.RawMessage(`{"type":"compaction","content":"old summary"}`)},
	}
	resp, err := client.ChatStreamWithContext("system", []store.Message{{Role: "user", Content: "new"}}, ctx, CompactConfig{}, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithContext returned error: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("response text = %q, want ok", resp.Text)
	}
	input := responseReq["input"].([]any)
	first := input[0].(map[string]any)
	if first["type"] != "compaction" || first["content"] != "old summary" {
		t.Fatalf("first input = %#v, want compaction item", first)
	}
}

func TestOpenAIResponsesParsesCompactionOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]},{"type":"compaction","content":"summary"}]}`))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	resp, err := client.Chat("system", []store.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Text != "ok" || !resp.Compacted || resp.ProviderContext.IsEmpty() {
		t.Fatalf("response = %#v, want text and compaction context", resp)
	}
}

func TestOpenAIResponsesStreamParsesCompletedCompactionOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n"))
		w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"compaction\",\"content\":\"summary\"}]}}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		openaiBase: openaiBase{
			cfg: Config{
				BaseURL: server.URL,
				APIKey:  "key",
				Model:   "gpt-test",
			},
			httpClient: server.Client(),
		},
	}
	resp, err := client.ChatStream("system", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if resp.Text != "ok" || !resp.Compacted || resp.ProviderContext.IsEmpty() {
		t.Fatalf("response = %#v, want text and compaction context", resp)
	}
}

func TestAnthropicChatParsesCompactionBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"},{"type":"compaction","content":"summary"}]}`))
	}))
	defer server.Close()

	client := &anthropicClient{
		cfg: Config{
			BaseURL: server.URL,
			APIKey:  "key",
			Model:   "claude-test",
		},
		httpClient: server.Client(),
	}
	resp, err := client.Chat("system", []store.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Text != "ok" || !resp.Compacted || resp.ProviderContext.IsEmpty() {
		t.Fatalf("response = %#v, want text and compaction context", resp)
	}
}

func TestAnthropicChatIgnoresEmptyCompactionBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"},{"type":"compaction","content":null},{"type":"compaction"}]}`))
	}))
	defer server.Close()

	client := &anthropicClient{
		cfg: Config{
			BaseURL: server.URL,
			APIKey:  "key",
			Model:   "claude-test",
		},
		httpClient: server.Client(),
	}
	resp, err := client.Chat("system", []store.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Text != "ok" || resp.Compacted || !resp.ProviderContext.IsEmpty() {
		t.Fatalf("response = %#v, want text and no empty compaction context", resp)
	}
}

func TestAnthropicCompactContextSendsBetaAndRoundTripsCompaction(t *testing.T) {
	var gotBeta string
	var reqBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"compaction","content":"new summary"}]}`))
	}))
	defer server.Close()

	client := &anthropicClient{
		cfg: Config{
			BaseURL: server.URL,
			APIKey:  "key",
			Model:   "claude-test",
		},
		httpClient: server.Client(),
	}
	ctx := store.ProviderContext{
		Provider: "anthropic",
		Endpoint: "messages",
		Items:    []json.RawMessage{json.RawMessage(`{"type":"compaction","content":"old summary"}`)},
	}
	compacted, err := client.CompactContext("system", []store.Message{{Role: "user", Content: "hi"}}, ctx, CompactConfig{
		Mode:          "auto",
		ContextWindow: 100,
		Threshold:     0.9,
		Instructions:  "keep decisions",
	})
	if err != nil {
		t.Fatalf("CompactContext returned error: %v", err)
	}
	if gotBeta != anthropicCompactBeta {
		t.Fatalf("anthropic-beta = %q, want %q", gotBeta, anthropicCompactBeta)
	}
	contextManagement := reqBody["context_management"].(map[string]any)
	edits := contextManagement["edits"].([]any)
	edit := edits[0].(map[string]any)
	if edit["type"] != "compact_20260112" || edit["instructions"] != "keep decisions" {
		t.Fatalf("context edit = %#v, want compact edit", edit)
	}
	if edit["pause_after_compaction"] != true {
		t.Fatalf("context edit = %#v, want pause_after_compaction true", edit)
	}
	trigger := edit["trigger"].(map[string]any)
	if trigger["type"] != "input_tokens" || int(trigger["value"].(float64)) != anthropicMinCompactTriggerTokens {
		t.Fatalf("trigger = %#v, want input_tokens minimum threshold", trigger)
	}
	messages := reqBody["messages"].([]any)
	firstMessage := messages[0].(map[string]any)
	firstContent := firstMessage["content"].([]any)[0].(map[string]any)
	if firstMessage["role"] != "assistant" || firstContent["type"] != "compaction" || firstContent["content"] != "old summary" {
		t.Fatalf("first message = %#v, want assistant compaction context", firstMessage)
	}
	if compacted.Provider != "anthropic" || compacted.Endpoint != "messages" || len(compacted.Items) != 1 {
		t.Fatalf("compacted context = %#v, want one anthropic compaction item", compacted)
	}
}

func TestAnthropicCompactContextReturnsNotTriggeredWithoutCompaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"not compacted"}]}`))
	}))
	defer server.Close()

	client := &anthropicClient{
		cfg: Config{
			BaseURL: server.URL,
			APIKey:  "key",
			Model:   "claude-test",
		},
		httpClient: server.Client(),
	}
	_, err := client.CompactContext("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{
		Mode:          "auto",
		ContextWindow: 100,
		Threshold:     0.9,
	})
	if !errors.Is(err, ErrCompactionNotTriggered) {
		t.Fatalf("CompactContext error = %v, want ErrCompactionNotTriggered", err)
	}
}

func TestAnthropicChatStreamWithContextSendsCompactionManagementAndBeta(t *testing.T) {
	var gotBeta string
	var reqBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"ok\"}}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	client := &anthropicClient{
		cfg: Config{
			BaseURL: server.URL,
			APIKey:  "key",
			Model:   "claude-test",
		},
		httpClient: server.Client(),
	}
	resp, err := client.ChatStreamWithContext("system", []store.Message{{Role: "user", Content: "hi"}}, store.ProviderContext{}, CompactConfig{
		Mode:          "auto",
		ContextWindow: 100,
		Threshold:     0.9,
	}, nil)
	if err != nil {
		t.Fatalf("ChatStreamWithContext returned error: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("response text = %q, want ok", resp.Text)
	}
	if gotBeta != anthropicCompactBeta {
		t.Fatalf("anthropic-beta = %q, want %q", gotBeta, anthropicCompactBeta)
	}
	contextManagement := reqBody["context_management"].(map[string]any)
	edits := contextManagement["edits"].([]any)
	edit := edits[0].(map[string]any)
	if edit["type"] != "compact_20260112" {
		t.Fatalf("context edit = %#v, want compact edit", edit)
	}
	if _, ok := edit["pause_after_compaction"]; ok {
		t.Fatalf("context edit = %#v, want no pause_after_compaction for normal stream", edit)
	}
	trigger := edit["trigger"].(map[string]any)
	if trigger["type"] != "input_tokens" || int(trigger["value"].(float64)) != anthropicMinCompactTriggerTokens {
		t.Fatalf("trigger = %#v, want input_tokens minimum threshold", trigger)
	}
}

func TestAnthropicChatStreamWithoutContextOmitsCompactionManagement(t *testing.T) {
	var gotBeta string
	var reqBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"ok\"}}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	client := &anthropicClient{
		cfg: Config{
			BaseURL: server.URL,
			APIKey:  "key",
			Model:   "claude-test",
		},
		httpClient: server.Client(),
	}
	resp, err := client.ChatStream("system", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("response text = %q, want ok", resp.Text)
	}
	if gotBeta != "" {
		t.Fatalf("anthropic-beta = %q, want empty", gotBeta)
	}
	if _, ok := reqBody["context_management"]; ok {
		t.Fatalf("request body = %#v, want no context_management", reqBody)
	}
}

func TestAnthropicStreamParsesCompactionDelta(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"content_block_start","content_block":{"type":"compaction","content":null}}`,
		`data: {"type":"content_block_delta","delta":{"type":"compaction_delta","content":"summary "}}`,
		`data: {"type":"content_block_delta","delta":{"type":"compaction_delta","content":"part"}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseAnthropicSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseAnthropicSSE returned error: %v", err)
	}
	if !resp.Compacted || resp.ProviderContext.Provider != "anthropic" || len(resp.ProviderContext.Items) != 1 {
		t.Fatalf("response = %#v, want one compaction context", resp)
	}
	var block map[string]any
	if err := json.Unmarshal(resp.ProviderContext.Items[0], &block); err != nil {
		t.Fatalf("unmarshal compaction block: %v", err)
	}
	if block["type"] != "compaction" || block["content"] != "summary part" {
		t.Fatalf("compaction block = %#v, want combined content", block)
	}
}

func TestAnthropicStreamIgnoresEmptyCompactionStart(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"content_block_start","content_block":{"type":"compaction","content":null}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseAnthropicSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseAnthropicSSE returned error: %v", err)
	}
	if resp.Compacted || !resp.ProviderContext.IsEmpty() {
		t.Fatalf("response = %#v, want no context from empty compaction start", resp)
	}
}

func TestParseResponsesOutputWithImage(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	resp, err := parseResponsesOutput(responsesOutput{Output: []responsesOutputItem{
		{
			Type: "message",
			Content: []responsesOutputItemPart{
				{Type: "output_text", Text: "done"},
			},
		},
		{
			ID:     "ig_1",
			Type:   "image_generation_call",
			Result: imageB64,
		},
	}})
	if err != nil {
		t.Fatalf("parseResponsesOutput returned error: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("response text = %q, want done", resp.Text)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "image-bytes" {
		t.Fatalf("response images = %#v, want decoded image", resp.Images)
	}
	if resp.Images[0].Reference.Provider != "openai" || resp.Images[0].Reference.Type != "image_generation_call" || resp.Images[0].Reference.ID != "ig_1" {
		t.Fatalf("image reference = %#v, want openai image_generation_call ig_1", resp.Images[0].Reference)
	}
	if resp.Images[0].MIMEType != "image/png" {
		t.Fatalf("image MIME type = %q, want image/png", resp.Images[0].MIMEType)
	}
}

func TestParseResponsesSSEOutputItemDoneImage(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"done"}`,
		`data: {"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","result":"` + imageB64 + `"}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("response text = %q, want done", resp.Text)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "image-bytes" {
		t.Fatalf("response images = %#v, want decoded image", resp.Images)
	}
	if resp.Images[0].Reference.Provider != "openai" || resp.Images[0].Reference.Type != "image_generation_call" || resp.Images[0].Reference.ID != "ig_1" {
		t.Fatalf("image reference = %#v, want openai image_generation_call ig_1", resp.Images[0].Reference)
	}
}

func TestParseResponsesSSECompletedImage(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := `data: {"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","result":"` + imageB64 + `"}]}}`

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "image-bytes" {
		t.Fatalf("response images = %#v, want decoded image", resp.Images)
	}
	if resp.Images[0].Reference.Provider != "openai" || resp.Images[0].Reference.Type != "image_generation_call" || resp.Images[0].Reference.ID != "ig_1" {
		t.Fatalf("image reference = %#v, want openai image_generation_call ig_1", resp.Images[0].Reference)
	}
}

func TestParseResponsesSSECompletedImageFromKnownItemWithoutType(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"image_item_1","type":"image_generation_call","status":"in_progress"}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"image_item_1","result":"` + imageB64 + `"}]}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "image-bytes" {
		t.Fatalf("response images = %#v, want decoded image", resp.Images)
	}
	if resp.Images[0].Reference.Provider != "openai" || resp.Images[0].Reference.Type != "image_generation_call" || resp.Images[0].Reference.ID != "image_item_1" {
		t.Fatalf("image reference = %#v, want openai image_generation_call image_item_1", resp.Images[0].Reference)
	}
}

func TestParseResponsesSSEIgnoresUntypedImageResultWithoutKnownItem(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := `data: {"type":"response.completed","response":{"output":[{"id":"image_item_1","result":"` + imageB64 + `"}]}}`

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 0 {
		t.Fatalf("response images = %#v, want no image without a known image_generation_call item", resp.Images)
	}
}

func TestParseResponsesSSEIgnoresIgPrefixWithoutKnownItem(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := `data: {"type":"response.completed","response":{"output":[{"id":"ig_1","result":"` + imageB64 + `"}]}}`

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 0 {
		t.Fatalf("response images = %#v, want no image from ig_ prefix alone", resp.Images)
	}
}

func TestParseResponsesSSEUsesCompletedImageOverGeneratingItem(t *testing.T) {
	generatingB64 := base64.StdEncoding.EncodeToString([]byte("gray-preview"))
	finalB64 := base64.StdEncoding.EncodeToString([]byte("final-image"))
	input := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","status":"generating","result":"` + generatingB64 + `"}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":"` + finalB64 + `"}]}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "final-image" {
		t.Fatalf("response images = %#v, want final image only", resp.Images)
	}
	if resp.Images[0].Reference.Provider != "openai" || resp.Images[0].Reference.Type != "image_generation_call" || resp.Images[0].Reference.ID != "ig_1" {
		t.Fatalf("image reference = %#v, want openai image_generation_call ig_1", resp.Images[0].Reference)
	}
}

func TestParseResponsesSSECompletedImageEventDedupesResponseOutput(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("final-image"))
	input := strings.Join([]string{
		`data: {"type":"response.image_generation_call.completed","item_id":"ig_1","result":"` + imageB64 + `"}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":"` + imageB64 + `"}]}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "final-image" {
		t.Fatalf("response images = %#v, want one final image", resp.Images)
	}
	if resp.Images[0].Reference.Provider != "openai" || resp.Images[0].Reference.Type != "image_generation_call" || resp.Images[0].Reference.ID != "ig_1" {
		t.Fatalf("image reference = %#v, want openai image_generation_call ig_1", resp.Images[0].Reference)
	}
}

func TestParseResponsesSSEIgnoresPartialImage(t *testing.T) {
	partialB64 := base64.StdEncoding.EncodeToString([]byte("partial-image"))
	input := strings.Join([]string{
		`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_1","partial_image_b64":"` + partialB64 + `"}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 0 {
		t.Fatalf("response images = %#v, want no partial images", resp.Images)
	}
}

func TestParseResponsesImageMalformedBase64(t *testing.T) {
	_, err := parseResponsesOutput(responsesOutput{Output: []responsesOutputItem{
		{ID: "ig_1", Type: "image_generation_call", Result: "not-base64"},
	}})
	if err == nil || !strings.Contains(err.Error(), "decode response image result") {
		t.Fatalf("parseResponsesOutput error = %v, want decode response image result", err)
	}
}
