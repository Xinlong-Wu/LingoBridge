package core

import (
	"context"
	"errors"
	"testing"

	"wechatbox/internal/commands"
	"wechatbox/internal/config"
	"wechatbox/internal/llm"
	"wechatbox/internal/store"
)

type fakeSessions struct {
	sess       *store.Session
	conv       *store.Conversation
	saved      *store.Conversation
	model      string
	commandHit bool
}

func (f *fakeSessions) GetOrCreateCurrentSession(userID string) (*store.Session, error) {
	if f.sess != nil {
		return f.sess, nil
	}
	return &store.Session{ID: "session", UserID: userID, Name: "default", Current: true}, nil
}

func (f *fakeSessions) LoadHistory(userID, sessionID string) (*store.Conversation, error) {
	if f.conv != nil {
		return f.conv, nil
	}
	return &store.Conversation{}, nil
}

func (f *fakeSessions) SaveHistory(userID, sessionID string, conv *store.Conversation) error {
	f.saved = conv
	return nil
}

func (f *fakeSessions) CurrentSession(userID string) (*store.Session, error) {
	f.commandHit = true
	return f.GetOrCreateCurrentSession(userID)
}

func (f *fakeSessions) CreateSession(userID, name string) (*store.Session, error) {
	return &store.Session{ID: "new", UserID: userID, Name: name, Current: true}, nil
}

func (f *fakeSessions) ListSessions(userID string) ([]store.Session, error) { return nil, nil }
func (f *fakeSessions) SwitchSession(userID, sessionName string) (*store.Session, error) {
	return &store.Session{ID: "switched", UserID: userID, Name: sessionName, Current: true}, nil
}
func (f *fakeSessions) RenameCurrentSession(userID, newName string) (*store.Session, error) {
	return &store.Session{ID: "current", UserID: userID, Name: newName, Current: true}, nil
}
func (f *fakeSessions) ArchiveSession(userID, sessionName string) (*store.ArchiveResult, error) {
	return &store.ArchiveResult{Archived: store.Session{Name: sessionName}}, nil
}
func (f *fakeSessions) ClearSession(userID string) (*store.Session, error) {
	return &store.Session{ID: "clear", UserID: userID, Name: "clear", Current: true}, nil
}
func (f *fakeSessions) CurrentModel(userID string) (string, error) {
	if f.model != "" {
		return f.model, nil
	}
	return "deepseek", nil
}
func (f *fakeSessions) SetModel(userID, modelName string) error { return nil }
func (f *fakeSessions) DefaultModelName() string                { return "deepseek" }
func (f *fakeSessions) ListModels() []string                    { return []string{"deepseek"} }

type fakeLLM struct {
	called          bool
	preparedContent string
	messages        []store.Message
	resp            llm.Response
	prepareErr      error
}

func (f *fakeLLM) PrepareUserMessage(content string, attachments []llm.InputAttachment) (store.Message, error) {
	f.preparedContent = content
	if f.prepareErr != nil {
		return store.Message{}, f.prepareErr
	}
	return store.Message{Role: "user", Content: content}, nil
}

func (f *fakeLLM) Chat(systemPrompt string, messages []store.Message) (llm.Response, error) {
	return f.ChatStream(systemPrompt, messages, nil)
}

func (f *fakeLLM) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (llm.Response, error) {
	f.called = true
	f.messages = messages
	if f.resp.Text == "" {
		f.resp.Text = "hello"
	}
	return f.resp, nil
}

func (f *fakeLLM) AssistantMessage(resp llm.Response) (store.Message, error) {
	return store.Message{Role: "assistant", Content: resp.Text}, nil
}

type fakeSender struct {
	sent       []OutboundMessage
	typing     int
	stopTyping int
}

func (f *fakeSender) Send(ctx context.Context, msg OutboundMessage) error {
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeSender) StartTyping(ctx context.Context) func() {
	f.typing++
	return func() { f.stopTyping++ }
}

func TestHandleCommandDoesNotCallLLM(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{}
	b := testBot(sessions, client)
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "/current", LLMText: "/current"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if client.called {
		t.Fatal("LLM was called for command")
	}
	if len(sender.sent) != 1 || sender.sent[0].Text == "" {
		t.Fatalf("sent = %#v, want command text response", sender.sent)
	}
}

func TestHandleCommandPolicyDisablesCommand(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{}
	b := testBot(sessions, client)
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{
		UserKey:       "user",
		CommandText:   "/model",
		LLMText:       "/model",
		CommandPolicy: commands.PolicyWithDisabled("/model"),
	}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if client.called {
		t.Fatal("LLM was called for disabled command")
	}
	if len(sender.sent) != 1 || sender.sent[0].Text == "" {
		t.Fatalf("sent = %#v, want unsupported command response", sender.sent)
	}
}

func TestHandleTextSavesConversationAndSendsReply(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{}
	b := testBot(sessions, client)
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !client.called || client.preparedContent != "hi" {
		t.Fatalf("client called=%v prepared=%q", client.called, client.preparedContent)
	}
	if sessions.saved == nil || len(sessions.saved.Messages) != 2 {
		t.Fatalf("saved = %#v, want two history messages", sessions.saved)
	}
	if len(sender.sent) != 1 || sender.sent[0].Text != "hello" {
		t.Fatalf("sent = %#v, want hello", sender.sent)
	}
	if sender.typing != 1 || sender.stopTyping != 1 {
		t.Fatalf("typing start/stop = %d/%d, want 1/1", sender.typing, sender.stopTyping)
	}
}

func TestHandleUsesPrepareHookAndPrepareErrorNotice(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{}
	b := testBot(sessions, client)
	sender := &fakeSender{}
	prepareErr := errors.New("prepare failed")

	err := b.Handle(context.Background(), InboundMessage{
		UserKey: "user",
		PrepareUserMessage: func(ctx context.Context, userID, sessionID string, client llm.Client) (store.Message, error) {
			return store.Message{}, prepareErr
		},
		PrepareErrorNotice: func(error) string { return "custom prepare notice" },
	}, sender)
	if !errors.Is(err, prepareErr) {
		t.Fatalf("Handle error = %v, want prepareErr", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].Text != "custom prepare notice" {
		t.Fatalf("sent = %#v, want custom prepare notice", sender.sent)
	}
}

func testBot(sessions *fakeSessions, client *fakeLLM) *Bot {
	cfg := config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {Provider: "openai", BaseURL: "https://llm.test", APIKey: "key", ID: "model"},
		},
		SystemPrompt: "system",
	}
	return &Bot{
		Sessions:       sessions,
		LLMConfig:      cfg,
		LLMClients:     map[string]llm.Client{},
		NewLLM:         func(config.ResolvedModel) llm.Client { return client },
		TextChunkLimit: DefaultTextChunkLimit,
	}
}
