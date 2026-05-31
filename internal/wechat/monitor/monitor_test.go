package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"wechatbox/internal/config"
	"wechatbox/internal/llm"
	"wechatbox/internal/store"
	"wechatbox/internal/wechat/api"
)

type fakeWechatClient struct {
	sent   []*api.WeixinMessage
	typing []int
	stops  int
}

func (f *fakeWechatClient) GetUpdatesContext(ctx context.Context, buf string) (*api.GetUpdatesResp, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
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
func (f *fakeWechatClient) NotifyStop() error {
	f.stops++
	return nil
}

type fakeCursorStore struct{}

func (f *fakeCursorStore) GetSyncBuf(accountID string) (string, error) {
	return "", nil
}

func (f *fakeCursorStore) SaveSyncBuf(accountID, buf string) error {
	return nil
}

type fakeConversationManager struct {
	sess        *store.Session
	conv        *store.Conversation
	saved       *store.Conversation
	sessions    []store.Session
	modelByUser map[string]string
	models      []string
}

func (f *fakeConversationManager) GetOrCreateCurrentSession(userID string) (*store.Session, error) {
	if f.sess != nil {
		return f.sess, nil
	}
	return &store.Session{ID: "session", UserID: userID, Name: "default", Current: true}, nil
}

func (f *fakeConversationManager) CurrentSession(userID string) (*store.Session, error) {
	return f.GetOrCreateCurrentSession(userID)
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
	return &store.Session{ID: "new", UserID: userID, Name: name, Current: true}, nil
}

func (f *fakeConversationManager) ListSessions(userID string) ([]store.Session, error) {
	return f.sessions, nil
}

func (f *fakeConversationManager) SwitchSession(userID, sessionName string) (*store.Session, error) {
	return &store.Session{ID: "switched", UserID: userID, Name: sessionName, Current: true}, nil
}

func (f *fakeConversationManager) RenameCurrentSession(userID, newName string) (*store.Session, error) {
	return &store.Session{ID: "session", UserID: userID, Name: newName, Current: true}, nil
}

func (f *fakeConversationManager) ArchiveSession(userID, sessionName string) (*store.ArchiveResult, error) {
	return &store.ArchiveResult{
		Archived:       store.Session{ID: "session", UserID: userID, Name: "default", Archived: true},
		Current:        &store.Session{ID: "new", UserID: userID, Name: "new", Current: true},
		CurrentChanged: true,
	}, nil
}

func (f *fakeConversationManager) ClearSession(userID string) (*store.Session, error) {
	return &store.Session{ID: "cleared", UserID: userID, Name: "session-1", Current: true}, nil
}

func (f *fakeConversationManager) CurrentModel(userID string) (string, error) {
	if f.modelByUser != nil && f.modelByUser[userID] != "" {
		return f.modelByUser[userID], nil
	}
	return "deepseek", nil
}

func (f *fakeConversationManager) SetModel(userID, modelName string) error {
	if f.modelByUser == nil {
		f.modelByUser = map[string]string{}
	}
	f.modelByUser[userID] = modelName
	return nil
}

func (f *fakeConversationManager) DefaultModelName() string {
	return "deepseek"
}

func (f *fakeConversationManager) ListModels() []string {
	if len(f.models) > 0 {
		return f.models
	}
	return []string{"deepseek", "gpt4o"}
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
		sess:        &store.Session{ID: "session", UserID: "user", Name: "default", Current: true},
		conv:        &store.Conversation{},
		modelByUser: map[string]string{"user": "deepseek"},
	}
	llmClient := &fakeLLM{response: "hello"}
	return &bot{
		client:     client,
		cursors:    &fakeCursorStore{},
		sessions:   sessions,
		cfg:        testLLMConfig(),
		llmClients: map[string]llm.Client{"deepseek": llmClient},
		newLLM: func(model config.ResolvedModel) llm.Client {
			return &fakeLLM{response: model.Name}
		},
	}, client, sessions, llmClient
}

func testLLMConfig() config.LLMConfig {
	return config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {
				Provider: "openai",
				BaseURL:  "https://deepseek.test/v1",
				APIKey:   "key",
				ID:       "deepseek-chat",
				Endpoint: "chat",
			},
			"gpt4o": {
				Provider: "openai",
				BaseURL:  "https://openai.test/v1",
				APIKey:   "key",
				ID:       "gpt-4o",
				Endpoint: "responses",
			},
		},
		SystemPrompt: "system",
	}
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
		{ID: "session", UserID: "user", Name: "default", Current: true},
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

func TestProcessOneQuotedTextMessage(t *testing.T) {
	b, _, sessions, llmClient := newTestBot()
	msg := &api.WeixinMessage{
		FromUserID:   "user",
		ContextToken: "context",
		ItemList: []*api.MessageItem{
			{
				Type:     api.ItemTypeText,
				TextItem: &api.TextItem{Text: "what about this?"},
				RefMsg: &api.RefMessage{MessageItem: &api.MessageItem{
					Type:     api.ItemTypeText,
					TextItem: &api.TextItem{Text: "original text"},
				}},
			},
		},
	}

	if err := b.processOne(msg); err != nil {
		t.Fatalf("processOne returned error: %v", err)
	}

	want := "[引用: original text]\nwhat about this?"
	if !llmClient.called {
		t.Fatal("LLM was not called")
	}
	if got := llmClient.messages[len(llmClient.messages)-1].Content; got != want {
		t.Fatalf("LLM user message = %q, want %q", got, want)
	}
	if got := sessions.saved.Messages[0].Content; got != want {
		t.Fatalf("saved user message = %q, want %q", got, want)
	}
}

func TestProcessOneQuotedSlashCommandUsesPlainText(t *testing.T) {
	b, client, _, llmClient := newTestBot()
	b.sessions.(*fakeConversationManager).sessions = []store.Session{
		{ID: "session", UserID: "user", Name: "default", Current: true},
	}
	msg := &api.WeixinMessage{
		FromUserID:   "user",
		ContextToken: "context",
		ItemList: []*api.MessageItem{
			{
				Type:     api.ItemTypeText,
				TextItem: &api.TextItem{Text: "/list"},
				RefMsg: &api.RefMessage{MessageItem: &api.MessageItem{
					Type:     api.ItemTypeText,
					TextItem: &api.TextItem{Text: "quoted"},
				}},
			},
		},
	}

	if err := b.processOne(msg); err != nil {
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

func TestProcessOneUsesUserModelPreference(t *testing.T) {
	b, client, sessions, defaultLLM := newTestBot()
	preferredLLM := &fakeLLM{response: "from gpt4o"}
	sessions.modelByUser["user"] = "gpt4o"
	b.newLLM = func(model config.ResolvedModel) llm.Client {
		if model.Name != "gpt4o" {
			t.Fatalf("created model = %s, want gpt4o", model.Name)
		}
		return preferredLLM
	}

	if err := b.processOne(textMessage("hi")); err != nil {
		t.Fatalf("processOne returned error: %v", err)
	}

	if defaultLLM.called {
		t.Fatal("default LLM was called")
	}
	if !preferredLLM.called {
		t.Fatal("preferred LLM was not called")
	}
	if got := lastSentText(t, client); got != "from gpt4o" {
		t.Fatalf("sent text = %q, want preferred response", got)
	}
}

func TestProcessOneFallsBackForUnknownUserModel(t *testing.T) {
	b, _, sessions, defaultLLM := newTestBot()
	sessions.modelByUser["user"] = "missing"

	if err := b.processOne(textMessage("hi")); err != nil {
		t.Fatalf("processOne returned error: %v", err)
	}

	if !defaultLLM.called {
		t.Fatal("default LLM was not called for unknown model")
	}
}

func TestRunAccountStopsOnContextCancel(t *testing.T) {
	b, client, _, _ := newTestBot()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := b.runAccount(ctx, store.Account{ID: "account", Name: "bot"})
	if err != nil {
		t.Fatalf("runAccount returned error: %v", err)
	}
	if client.stops != 1 {
		t.Fatalf("NotifyStop calls = %d, want 1", client.stops)
	}
}

func TestRunAccountCancelsDuringGetUpdates(t *testing.T) {
	client := &blockingWechatClient{ready: make(chan struct{})}
	b := &bot{
		client:     client,
		cursors:    &fakeCursorStore{},
		sessions:   &fakeConversationManager{},
		cfg:        testLLMConfig(),
		llmClients: map[string]llm.Client{"deepseek": &fakeLLM{}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- b.runAccount(ctx, store.Account{ID: "account", Name: "bot"})
	}()

	<-client.ready
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAccount returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runAccount did not exit after cancel")
	}
	if client.stops != 1 {
		t.Fatalf("NotifyStop calls = %d, want 1", client.stops)
	}
}

type blockingWechatClient struct {
	ready chan struct{}
	stops int
}

func (b *blockingWechatClient) GetUpdatesContext(ctx context.Context, buf string) (*api.GetUpdatesResp, error) {
	close(b.ready)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingWechatClient) SendMessage(msg *api.WeixinMessage) error { return nil }

func (b *blockingWechatClient) GetConfig(ilinkUserID, contextToken string) (*api.GetConfigResp, error) {
	return &api.GetConfigResp{}, nil
}

func (b *blockingWechatClient) SendTyping(ilinkUserID, typingTicket string, status int) error {
	return nil
}

func (b *blockingWechatClient) NotifyStart() error { return nil }
func (b *blockingWechatClient) NotifyStop() error {
	b.stops++
	return nil
}
