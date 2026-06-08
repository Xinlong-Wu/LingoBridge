package monitor

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestMarshalRichTextContentBuildsPostContent(t *testing.T) {
	body, err := marshalRichTextContent("hello\n\nworld")
	if err != nil {
		t.Fatalf("marshalRichTextContent returned error: %v", err)
	}

	var content richTextContent
	if err := json.Unmarshal([]byte(body), &content); err != nil {
		t.Fatalf("unmarshal rich text content: %v", err)
	}

	lines := content.ZhCN.Content
	if len(lines) != 3 {
		t.Fatalf("content lines = %d, want 3", len(lines))
	}
	tests := []struct {
		line int
		text string
	}{
		{line: 0, text: "hello"},
		{line: 1, text: " "},
		{line: 2, text: "world"},
	}
	for _, tc := range tests {
		if len(lines[tc.line]) != 1 {
			t.Fatalf("line %d elements = %d, want 1", tc.line, len(lines[tc.line]))
		}
		element := lines[tc.line][0]
		if element.Tag != "text" || element.Text != tc.text {
			t.Fatalf("line %d element = %#v, want text %q", tc.line, element, tc.text)
		}
	}
}

func TestBuildRichTextContentNormalizesCRLF(t *testing.T) {
	content := buildRichTextContent("one\r\ntwo\rthree")

	lines := content.ZhCN.Content
	if len(lines) != 3 {
		t.Fatalf("content lines = %d, want 3", len(lines))
	}
	if lines[0][0].Text != "one" || lines[1][0].Text != "two" || lines[2][0].Text != "three" {
		t.Fatalf("content = %#v, want normalized line endings", lines)
	}
}

func TestSDKSenderSendsPostRichTextMessage(t *testing.T) {
	var messageRequest struct {
		ReceiveID string `json:"receive_id"`
		MsgType   string `json:"msg_type"`
		Content   string `json:"content"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v3/token":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/im/v1/messages":
			if r.URL.Query().Get("receive_id_type") != "chat_id" {
				t.Fatalf("receive_id_type = %q, want chat_id", r.URL.Query().Get("receive_id_type"))
			}
			if err := json.NewDecoder(r.Body).Decode(&messageRequest); err != nil {
				t.Fatalf("decode message request: %v", err)
			}
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient("cli_xxx", "secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
		lark.WithHttpClient(server.Client()),
	)
	sender := &sdkSender{client: client}
	if err := sender.SendText(t.Context(), "oc_chat", "hello\nworld"); err != nil {
		t.Fatalf("SendText returned error: %v", err)
	}

	if messageRequest.ReceiveID != "oc_chat" {
		t.Fatalf("receive_id = %q, want oc_chat", messageRequest.ReceiveID)
	}
	if messageRequest.MsgType != "post" {
		t.Fatalf("msg_type = %q, want post", messageRequest.MsgType)
	}
	var content richTextContent
	if err := json.Unmarshal([]byte(messageRequest.Content), &content); err != nil {
		t.Fatalf("unmarshal request content: %v", err)
	}
	lines := content.ZhCN.Content
	if len(lines) != 2 || lines[0][0].Text != "hello" || lines[1][0].Text != "world" {
		t.Fatalf("request content = %#v, want rich text lines", lines)
	}
}

func TestSDKSenderCreatesTextAndReturnsMessageID(t *testing.T) {
	var messageRequest struct {
		ReceiveID string `json:"receive_id"`
		MsgType   string `json:"msg_type"`
		Content   string `json:"content"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v3/token":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/im/v1/messages":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if r.URL.Query().Get("receive_id_type") != "chat_id" {
				t.Fatalf("receive_id_type = %q, want chat_id", r.URL.Query().Get("receive_id_type"))
			}
			if err := json.NewDecoder(r.Body).Decode(&messageRequest); err != nil {
				t.Fatalf("decode message request: %v", err)
			}
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"message_id": "om_stream"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient("cli_xxx", "secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
		lark.WithHttpClient(server.Client()),
	)
	sender := &sdkSender{client: client}
	messageID, err := sender.CreateText(t.Context(), "oc_chat", "hello")
	if err != nil {
		t.Fatalf("CreateText returned error: %v", err)
	}
	if messageID != "om_stream" {
		t.Fatalf("messageID = %q, want om_stream", messageID)
	}
	if messageRequest.ReceiveID != "oc_chat" || messageRequest.MsgType != "post" {
		t.Fatalf("request = %#v, want post to oc_chat", messageRequest)
	}
}

func TestSDKSenderUpdatesPostRichTextMessage(t *testing.T) {
	var updateRequest struct {
		MsgType string `json:"msg_type"`
		Content string `json:"content"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v3/token":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/im/v1/messages/om_stream":
			if r.Method != http.MethodPut {
				t.Fatalf("method = %s, want PUT", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&updateRequest); err != nil {
				t.Fatalf("decode update request: %v", err)
			}
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"message_id": "om_stream"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient("cli_xxx", "secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
		lark.WithHttpClient(server.Client()),
	)
	sender := &sdkSender{client: client}
	if err := sender.UpdateText(t.Context(), "om_stream", "hello\nworld"); err != nil {
		t.Fatalf("UpdateText returned error: %v", err)
	}
	if updateRequest.MsgType != "post" {
		t.Fatalf("msg_type = %q, want post", updateRequest.MsgType)
	}
	var content richTextContent
	if err := json.Unmarshal([]byte(updateRequest.Content), &content); err != nil {
		t.Fatalf("unmarshal update content: %v", err)
	}
	lines := content.ZhCN.Content
	if len(lines) != 2 || lines[0][0].Text != "hello" || lines[1][0].Text != "world" {
		t.Fatalf("update content = %#v, want rich text lines", lines)
	}
}

func TestSDKSenderUpdateTextRecognizesEditLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v3/token":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              7200,
			})
		case "/open-apis/im/v1/messages/om_stream":
			writeJSON(t, w, map[string]any{
				"code": 230072,
				"msg":  "The message has reached the number of times it can be edited.",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient("cli_xxx", "secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
		lark.WithHttpClient(server.Client()),
	)
	sender := &sdkSender{client: client}
	err := sender.UpdateText(t.Context(), "om_stream", "hello")
	if !errors.Is(err, ErrFeishuMessageEditLimit) {
		t.Fatalf("UpdateText error = %v, want ErrFeishuMessageEditLimit", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
