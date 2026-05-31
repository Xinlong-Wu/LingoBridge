package monitor

import (
	"errors"
	"strings"
	"testing"

	"wechatbox/internal/config"
	"wechatbox/internal/store"
	"wechatbox/internal/wechat/api"
)

type fakeWechatClient struct {
	sent   []*api.WeixinMessage
	typing []int
}

func (f *fakeWechatClient) GetUpdates(buf string) (*api.GetUpdatesResp, error) {
	return &api.GetUpdatesResp{}, nil
}

func (f *fakeWechatClient) SendMessage(msg *api.WeixinMessage) error {
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeWechatClient) GetConfig(ilinkUserID, contextToken string) (*api.GetConfigResp, error) {
	return &api.GetConfigResp{TypingTicket: "ticket"}, nil
}

func (f *fakeWechatClient) SendTyping(ilinkUserID, typingTicket string, status int) error {
	f.typing = append(f.typing, status)
	return nil
}

func (f *fakeWechatClient) NotifyStart() error { return nil }
func (f *fakeWechatClient) NotifyStop() error  { return nil }

type fakeConversationManager struct {
	sess     *store.Session
	conv     *store.Conversation
	saved    *store.Conversation
	sessions []store.Session
}

func (f *fakeConversationManager) GetOrCreateActiveSession(userID string) (*store.Session, error) {
	if f.sess != nil {
		return f.sess, nil
	}
	return &store.Session{ID: "session", UserID: userID, Name: "default", Active: true}, nil
}

func (f *fakeConversationManager) LoadHistory(userID, sessionID string) (*store.Conversation, error) {
	if f.conv != nil {
		return f.conv, nil
	}
	return &store.Conversation{}, nil
}

func (f *fakeConversationManager) SaveHistory(userID, sessionID string, conv *store.Conversation) error {
	f.saved = conv
	return nil
}

func (f *fakeConversationManager) CreateSession(userID, name string) (*store.Session, error) {
	return &store.Session{ID: "new", UserID: userID, Name: name, Active: true}, nil
}

func (f *fakeConversationManager) ListSessions(userID string) ([]store.Session, error) {
	return f.sessions, nil
}

func (f *fakeConversationManager) SwitchSession(userID, sessionName string) (*store.Session, error) {
	return &store.Session{ID: "switched", UserID: userID, Name: sessionName, Active: true}, nil
}

func (f *fakeConversationManager) ClearSession(userID string) (*store.Session, error) {
	return &store.Session{ID: "cleared", UserID: userID, Name: "session-1", Active: true}, nil
}

type fakeLLM struct {
	response string
	err      error
	called   bool
	messages []store.Message
}

func (f *fakeLLM) Chat(systemPrompt string, messages []store.Message) (string, error) {
	return f.response, f.err
}

func (f *fakeLLM) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (string, error) {
	f.called = true
	f.messages = messages
	if f.err != nil {
		return "", f.err
	}
	if onChunk != nil {
		if err := onChunk(f.response); err != nil {
			return "", err
		}
	}
	return f.response, nil
}

func newTestBot() (*bot, *fakeWechatClient, *fakeConversationManager, *fakeLLM) {
	client := &fakeWechatClient{}
	sessions := &fakeConversationManager{
		sess: &store.Session{ID: "session", UserID: "user", Name: "default", Active: true},
		conv: &store.Conversation{},
	}
	llmClient := &fakeLLM{response: "hello"}
	return &bot{
		client:    client,
		sessions:  sessions,
		llmClient: llmClient,
		cfg:       config.LLMConfig{SystemPrompt: "system"},
	}, client, sessions, llmClient
}

func textMessage(text string) *api.WeixinMessage {
	return &api.WeixinMessage{
		FromUserID:   "user",
		ContextToken: "context",
		ItemList: []*api.MessageItem{
			{Type: api.ItemTypeText, TextItem: &api.TextItem{Text: text}},
		},
	}
}

func lastSentText(t *testing.T, client *fakeWechatClient) string {
	t.Helper()
	if len(client.sent) == 0 {
		t.Fatal("no message sent")
	}
	items := client.sent[len(client.sent)-1].ItemList
	if len(items) == 0 || items[0].TextItem == nil {
		t.Fatal("last sent message is not text")
	}
	return items[0].TextItem.Text
}

func TestProcessOneTextMessage(t *testing.T) {
	b, client, sessions, llmClient := newTestBot()

	if err := b.processOne(textMessage("hi")); err != nil {
		t.Fatalf("processOne returned error: %v", err)
	}

	if !llmClient.called {
		t.Fatal("LLM was not called")
	}
	if got := lastSentText(t, client); got != "hello" {
		t.Fatalf("sent text = %q, want hello", got)
	}
	if len(client.typing) != 2 || client.typing[0] != api.TypingStatusTyping || client.typing[1] != api.TypingStatusCancel {
		t.Fatalf("typing statuses = %v", client.typing)
	}
	if sessions.saved == nil || len(sessions.saved.Messages) != 2 {
		t.Fatalf("saved conversation = %#v, want two messages", sessions.saved)
	}
	if got := sessions.saved.Messages[0].Content; got != "hi" {
		t.Fatalf("saved user message = %q, want hi", got)
	}
}

func TestProcessOneSlashCommand(t *testing.T) {
	b, client, _, llmClient := newTestBot()
	b.sessions.(*fakeConversationManager).sessions = []store.Session{
		{ID: "session", UserID: "user", Name: "default", Active: true},
	}

	if err := b.processOne(textMessage("/list")); err != nil {
		t.Fatalf("processOne returned error: %v", err)
	}

	if llmClient.called {
		t.Fatal("LLM was called for slash command")
	}
	if got := lastSentText(t, client); !strings.Contains(got, "default") {
		t.Fatalf("sent text = %q, want session list", got)
	}
}

func TestProcessOneVoiceTranscription(t *testing.T) {
	b, _, sessions, llmClient := newTestBot()
	msg := &api.WeixinMessage{
		FromUserID:   "user",
		ContextToken: "context",
		ItemList: []*api.MessageItem{
			{Type: api.ItemTypeVoice, VoiceItem: &api.VoiceItem{Text: "voice text"}},
		},
	}

	if err := b.processOne(msg); err != nil {
		t.Fatalf("processOne returned error: %v", err)
	}

	if !llmClient.called {
		t.Fatal("LLM was not called")
	}
	if got := sessions.saved.Messages[0].Content; got != "voice text" {
		t.Fatalf("saved user message = %q, want voice text", got)
	}
}

func TestProcessOneUnsupportedFile(t *testing.T) {
	b, client, sessions, llmClient := newTestBot()
	msg := &api.WeixinMessage{
		FromUserID:   "user",
		ContextToken: "context",
		ItemList: []*api.MessageItem{
			{Type: api.ItemTypeFile, FileItem: &api.FileItem{FileName: "a.txt"}},
		},
	}

	if err := b.processOne(msg); err != nil {
		t.Fatalf("processOne returned error: %v", err)
	}

	if llmClient.called {
		t.Fatal("LLM was called for unsupported file")
	}
	if sessions.saved != nil {
		t.Fatalf("conversation was saved: %#v", sessions.saved)
	}
	if got := lastSentText(t, client); !strings.Contains(got, "文件消息") {
		t.Fatalf("sent text = %q, want file unsupported message", got)
	}
}

func TestProcessOneLLMError(t *testing.T) {
	b, client, sessions, llmClient := newTestBot()
	llmClient.err = errors.New("boom")

	err := b.processOne(textMessage("hi"))
	if err == nil {
		t.Fatal("processOne returned nil error")
	}
	if sessions.saved != nil {
		t.Fatalf("conversation was saved: %#v", sessions.saved)
	}
	if got := lastSentText(t, client); !strings.Contains(got, "AI 响应失败") {
		t.Fatalf("sent text = %q, want AI error message", got)
	}
}
