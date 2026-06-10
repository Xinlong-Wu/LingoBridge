package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"lingobridge/internal/commands"
	"lingobridge/internal/config"
	"lingobridge/internal/llm"
	"lingobridge/internal/store"
	tooltypes "lingobridge/internal/tools"
)

const testTextChunkLimit = 4000

func testLLMConfig() config.LLMConfig {
	return config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {Provider: "openai", BaseURL: "https://llm.test", APIKey: "key", ID: "model"},
		},
		SystemPrompt: "system",
	}
}

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
	return &store.Session{ID: "cleared", UserID: userID, Name: "session-1", Current: true}, nil
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
	streamChunks    []string
	streamErr       error
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
	for _, chunk := range f.streamChunks {
		if onChunk != nil {
			if err := onChunk(chunk); err != nil {
				return llm.Response{}, err
			}
		}
	}
	if f.streamErr != nil {
		return llm.Response{}, f.streamErr
	}
	if f.resp.Text == "" {
		f.resp.Text = "hello"
	}
	return f.resp, nil
}

func (f *fakeLLM) AssistantMessage(resp llm.Response) (store.Message, error) {
	return store.Message{Role: "assistant", Content: resp.Text}, nil
}

type fakeNativeLLM struct {
	fakeLLM
	compactedMessages []store.Message
	contextMessages   []store.Message
	context           store.ProviderContext
	compactErr        error
}

type fakeToolLLM struct {
	fakeLLM
	turns       int
	calls       []tooltypes.Call
	results     []tooltypes.Result
	toolSpecs   []tooltypes.Spec
	finalText   string
	providerCtx store.ProviderContext
}

func (f *fakeToolLLM) ChatStreamWithTools(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact llm.CompactConfig, tools []tooltypes.Spec, previous llm.ToolState, results []tooltypes.Result, onChunk func(chunk string) error) (llm.ToolResponse, error) {
	f.called = true
	f.messages = messages
	f.providerCtx = providerContext
	f.toolSpecs = tools
	f.results = append(f.results, results...)
	if f.turns == 0 {
		f.turns++
		return llm.ToolResponse{
			ToolCalls: f.calls,
			ToolState: llm.ToolState{
				Provider: "fake",
				Endpoint: "tools",
				Items:    []json.RawMessage{json.RawMessage(`{"type":"function_call"}`)},
			},
		}, nil
	}
	text := f.finalText
	if text == "" {
		text = "tool final"
	}
	return llm.ToolResponse{Response: llm.Response{Text: text}}, nil
}

type fakeTool struct {
	spec   tooltypes.Spec
	result string
	err    string
	block  bool
	calls  []tooltypes.Call
}

func (f *fakeTool) Spec() tooltypes.Spec {
	if f.spec.Name == "" {
		f.spec.Name = "fake_tool"
	}
	return f.spec
}

func (f *fakeTool) Execute(ctx context.Context, call tooltypes.Call) tooltypes.Result {
	f.calls = append(f.calls, call)
	if f.block {
		<-ctx.Done()
		return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: ctx.Err().Error(), IsError: true}
	}
	if f.err != "" {
		return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: f.err, IsError: true}
	}
	return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: f.result}
}

type fakeToolProvider struct {
	tools   []tooltypes.Tool
	options tooltypes.Options
}

func (f fakeToolProvider) Tools() []tooltypes.Tool {
	return f.tools
}

func (f fakeToolProvider) ToolOptions() tooltypes.Options {
	return f.options
}

func (f *fakeNativeLLM) CompactContext(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact llm.CompactConfig) (store.ProviderContext, error) {
	f.compactedMessages = append([]store.Message(nil), messages...)
	if f.compactErr != nil {
		return store.ProviderContext{}, f.compactErr
	}
	return store.ProviderContext{
		Provider: "openai",
		Endpoint: "responses",
		Items:    []json.RawMessage{json.RawMessage(`{"type":"compaction","content":"summary"}`)},
	}, nil
}

func (f *fakeNativeLLM) ChatStreamWithContext(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact llm.CompactConfig, onChunk func(chunk string) error) (llm.Response, error) {
	f.called = true
	f.messages = messages
	f.contextMessages = append([]store.Message(nil), messages...)
	f.context = providerContext
	if f.resp.Text == "" {
		f.resp.Text = "hello"
	}
	return f.resp, nil
}

type fakeSender struct {
	sent             []OutboundMessage
	typing           int
	stopTyping       int
	compactStarts    []CompactNotice
	compactFinishes  []CompactNotice
	compactStartErr  error
	compactFinishErr error
}

func (f *fakeSender) Send(ctx context.Context, msg OutboundMessage) error {
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeSender) StartTyping(ctx context.Context) func() {
	f.typing++
	return func() { f.stopTyping++ }
}

func (f *fakeSender) StartCompactNotice(ctx context.Context, notice CompactNotice) (CompactNoticeHandle, error) {
	f.compactStarts = append(f.compactStarts, notice)
	if f.compactStartErr != nil {
		return CompactNoticeHandle{}, f.compactStartErr
	}
	return CompactNoticeHandle{MessageID: "compact-notice"}, nil
}

func (f *fakeSender) FinishCompactNotice(ctx context.Context, handle CompactNoticeHandle, notice CompactNotice) error {
	f.compactFinishes = append(f.compactFinishes, notice)
	return f.compactFinishErr
}

type fakeStreamingSender struct {
	fakeSender
	stream  *fakeTextStream
	streams []*fakeTextStream
}

func (f *fakeStreamingSender) StartTextStream(ctx context.Context) (TextStream, error) {
	f.stream = &fakeTextStream{}
	f.streams = append(f.streams, f.stream)
	return f.stream, nil
}

type fakeTextStream struct {
	updates  []string
	finishes []string
}

func (f *fakeTextStream) Update(ctx context.Context, text string) error {
	f.updates = append(f.updates, text)
	return nil
}

func (f *fakeTextStream) Finish(ctx context.Context, text string) error {
	f.finishes = append(f.finishes, text)
	return nil
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

func TestHandleHelpIncludesToolSummaries(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{}
	b := testBot(sessions, client)
	sender := &fakeSender{}
	tool := &fakeTool{
		spec: tooltypes.Spec{
			Name:        "feishu_docs_search",
			Description: "Search Feishu Docs and Wiki visible to the configured Feishu app.",
		},
	}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "/help", LLMText: "/help", Tools: []tooltypes.Tool{tool}}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if client.called {
		t.Fatal("LLM was called for help command")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent = %#v, want one help message", sender.sent)
	}
	for _, want := range []string{"## 可用工具", "feishu_docs_search", "Search Feishu Docs"} {
		if !strings.Contains(sender.sent[0].Text, want) {
			t.Fatalf("help = %q, want %s", sender.sent[0].Text, want)
		}
	}
}

func TestHandleHelpIncludesToolProviderSummaries(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{}
	b := testBot(sessions, client)
	b.ToolProvider = fakeToolProvider{tools: []tooltypes.Tool{&fakeTool{
		spec: tooltypes.Spec{
			Name:        "mcp_files_read_file",
			Description: "Read a file through MCP.",
		},
	}}}
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "/help", LLMText: "/help"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Text, "mcp_files_read_file") {
		t.Fatalf("help = %#v, want MCP tool summary", sender.sent)
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

func TestHandleTextRunsToolsAndSavesTrace(t *testing.T) {
	sessions := &fakeSessions{}
	llmClient := &fakeToolLLM{
		calls: []tooltypes.Call{{
			ID:        "call_1",
			Name:      "fake_tool",
			Arguments: json.RawMessage(`{"query":"roadmap"}`),
		}},
		finalText: "answer with tool",
	}
	tool := &fakeTool{result: `{"ok":true}`}
	bot := New(sessions, testLLMConfig())
	bot.NewLLM = func(config.ResolvedModel) llm.Client { return llmClient }
	sender := &fakeSender{}

	err := bot.Handle(context.Background(), InboundMessage{
		UserKey:     "u1",
		CommandText: "search roadmap",
		LLMText:     "search roadmap",
		Tools:       []tooltypes.Tool{tool},
	}, sender)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(tool.calls) != 1 || tool.calls[0].Name != "fake_tool" {
		t.Fatalf("tool calls = %#v, want one fake_tool call", tool.calls)
	}
	if len(llmClient.results) != 1 || llmClient.results[0].Content != `{"ok":true}` {
		t.Fatalf("tool results = %#v, want result returned to model", llmClient.results)
	}
	if len(sender.sent) != 1 || sender.sent[0].Text != "answer with tool" {
		t.Fatalf("sent = %#v, want final tool answer", sender.sent)
	}
	if sessions.saved == nil || len(sessions.saved.Messages) != 2 {
		t.Fatalf("saved = %#v, want user and assistant messages", sessions.saved)
	}
	traces := sessions.saved.Messages[1].ToolTraces
	if len(traces) != 1 {
		t.Fatalf("tool traces = %#v, want one trace", traces)
	}
	if traces[0].Name != "fake_tool" || traces[0].Status != "ok" || !strings.Contains(traces[0].Arguments, "roadmap") {
		t.Fatalf("trace = %#v, want ok fake_tool trace", traces[0])
	}
}

func TestHandleTextRunsToolProviderTools(t *testing.T) {
	sessions := &fakeSessions{}
	llmClient := &fakeToolLLM{
		calls: []tooltypes.Call{{
			ID:        "call_1",
			Name:      "mcp_files_read_file",
			Arguments: json.RawMessage(`{"path":"a.txt"}`),
		}},
		finalText: "answer with mcp tool",
	}
	tool := &fakeTool{
		spec:   tooltypes.Spec{Name: "mcp_files_read_file", Description: "Read file"},
		result: `{"content":"ok"}`,
	}
	bot := New(sessions, testLLMConfig())
	bot.NewLLM = func(config.ResolvedModel) llm.Client { return llmClient }
	bot.ToolProvider = fakeToolProvider{tools: []tooltypes.Tool{tool}}
	sender := &fakeSender{}

	err := bot.Handle(context.Background(), InboundMessage{
		UserKey:     "u1",
		CommandText: "read file",
		LLMText:     "read file",
	}, sender)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(tool.calls) != 1 || tool.calls[0].Name != "mcp_files_read_file" {
		t.Fatalf("tool calls = %#v, want provider tool call", tool.calls)
	}
	if len(llmClient.toolSpecs) != 1 || llmClient.toolSpecs[0].Name != "mcp_files_read_file" {
		t.Fatalf("tool specs = %#v, want provider tool spec", llmClient.toolSpecs)
	}
}

func TestHandleTextPlatformToolWinsDuplicateProviderTool(t *testing.T) {
	sessions := &fakeSessions{}
	llmClient := &fakeToolLLM{
		calls: []tooltypes.Call{{
			ID:        "call_1",
			Name:      "shared_tool",
			Arguments: json.RawMessage(`{"source":"model"}`),
		}},
		finalText: "answer with platform tool",
	}
	platformTool := &fakeTool{
		spec:   tooltypes.Spec{Name: "shared_tool", Description: "Platform version"},
		result: `{"source":"platform"}`,
	}
	providerTool := &fakeTool{
		spec:   tooltypes.Spec{Name: "shared_tool", Description: "Provider version"},
		result: `{"source":"provider"}`,
	}
	bot := New(sessions, testLLMConfig())
	bot.NewLLM = func(config.ResolvedModel) llm.Client { return llmClient }
	bot.ToolProvider = fakeToolProvider{tools: []tooltypes.Tool{providerTool}}
	sender := &fakeSender{}

	err := bot.Handle(context.Background(), InboundMessage{
		UserKey: "u1",
		LLMText: "use duplicate",
		Tools:   []tooltypes.Tool{platformTool},
	}, sender)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(platformTool.calls) != 1 {
		t.Fatalf("platform tool calls = %#v, want one call", platformTool.calls)
	}
	if len(providerTool.calls) != 0 {
		t.Fatalf("provider tool calls = %#v, want skipped duplicate", providerTool.calls)
	}
	if len(llmClient.toolSpecs) != 1 || llmClient.toolSpecs[0].Description != "Platform version" {
		t.Fatalf("tool specs = %#v, want only platform spec", llmClient.toolSpecs)
	}
}

func TestHandleTextToolErrorsAreReturnedToModel(t *testing.T) {
	sessions := &fakeSessions{}
	llmClient := &fakeToolLLM{
		calls: []tooltypes.Call{{
			ID:        "call_1",
			Name:      "fake_tool",
			Arguments: json.RawMessage(`{}`),
		}},
		finalText: "handled error",
	}
	tool := &fakeTool{err: "permission denied"}
	bot := New(sessions, testLLMConfig())
	bot.NewLLM = func(config.ResolvedModel) llm.Client { return llmClient }
	sender := &fakeSender{}

	err := bot.Handle(context.Background(), InboundMessage{
		UserKey: "u1",
		LLMText: "try it",
		Tools:   []tooltypes.Tool{tool},
	}, sender)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(llmClient.results) != 1 || !llmClient.results[0].IsError || llmClient.results[0].Content != "permission denied" {
		t.Fatalf("tool results = %#v, want error result returned to model", llmClient.results)
	}
	traces := sessions.saved.Messages[1].ToolTraces
	if len(traces) != 1 || traces[0].Status != "error" || traces[0].Error != "permission denied" {
		t.Fatalf("tool traces = %#v, want error trace", traces)
	}
}

func TestHandleTextToolTimeoutIsReturnedToModel(t *testing.T) {
	sessions := &fakeSessions{}
	llmClient := &fakeToolLLM{
		calls: []tooltypes.Call{{
			ID:        "call_1",
			Name:      "fake_tool",
			Arguments: json.RawMessage(`{}`),
		}},
		finalText: "handled timeout",
	}
	tool := &fakeTool{block: true}
	bot := New(sessions, testLLMConfig())
	bot.ToolProvider = fakeToolProvider{options: tooltypes.Options{Timeout: time.Millisecond}}
	bot.NewLLM = func(config.ResolvedModel) llm.Client { return llmClient }

	err := bot.Handle(context.Background(), InboundMessage{
		UserKey: "u1",
		LLMText: "try it",
		Tools:   []tooltypes.Tool{tool},
	}, &fakeSender{})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(llmClient.results) != 1 || !llmClient.results[0].IsError || !strings.Contains(llmClient.results[0].Content, "timed out") {
		t.Fatalf("tool results = %#v, want timeout result returned to model", llmClient.results)
	}
}

func TestHandleTextToolCallLimit(t *testing.T) {
	sessions := &fakeSessions{}
	llmClient := &fakeToolLLM{
		calls: []tooltypes.Call{
			{ID: "call_1", Name: "fake_tool", Arguments: json.RawMessage(`{}`)},
			{ID: "call_2", Name: "fake_tool", Arguments: json.RawMessage(`{}`)},
		},
	}
	bot := New(sessions, testLLMConfig())
	bot.ToolProvider = fakeToolProvider{options: tooltypes.Options{MaxCalls: 1}}
	bot.NewLLM = func(config.ResolvedModel) llm.Client { return llmClient }

	err := bot.Handle(context.Background(), InboundMessage{
		UserKey: "u1",
		LLMText: "try it",
		Tools:   []tooltypes.Tool{&fakeTool{}},
	}, &fakeSender{})
	if err == nil || !strings.Contains(err.Error(), "tool call limit exceeded") {
		t.Fatalf("Handle error = %v, want tool call limit exceeded", err)
	}
	if sessions.saved != nil {
		t.Fatalf("saved = %#v, want no history saved when tool limit is exceeded", sessions.saved)
	}
}

func TestHandleTextCompactsNativeContextAndSavesRecentHistory(t *testing.T) {
	var history []store.Message
	for i := 0; i < nativeContextKeepRecentMessages+3; i++ {
		history = append(history, store.Message{Role: "user", Content: "old message " + string(rune('a'+i))})
	}
	sessions := &fakeSessions{
		conv: &store.Conversation{Messages: history},
	}
	client := &fakeNativeLLM{}
	cfg := config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {
				Provider:      "openai",
				BaseURL:       "https://llm.test",
				APIKey:        "key",
				ID:            "model",
				Endpoint:      "responses",
				ContextWindow: 4,
				Compact:       config.LLMCompactConfig{Mode: config.CompactModeAuto, Threshold: 0.25},
			},
		},
		SystemPrompt: "system",
	}
	b := &Bot{
		Sessions:            sessions,
		LLMConfig:           cfg,
		LLMClients:          map[string]llm.Client{},
		NewLLM:              func(config.ResolvedModel) llm.Client { return client },
		TextChunkLimit:      testTextChunkLimit,
		EnableTextStreaming: true,
	}
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "new", LLMText: "new"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.compactStarts) != 1 || sender.compactStarts[0].ModelName != "deepseek" || sender.compactStarts[0].Manual {
		t.Fatalf("compact starts = %#v, want one automatic deepseek notice", sender.compactStarts)
	}
	if len(sender.compactFinishes) != 1 || sender.compactFinishes[0].CompactedMessages != 3 || sender.compactFinishes[0].RetainedMessages != nativeContextKeepRecentMessages {
		t.Fatalf("compact finishes = %#v, want one success notice", sender.compactFinishes)
	}
	if len(client.compactedMessages) != 3 {
		t.Fatalf("compacted messages = %d, want 3 old messages", len(client.compactedMessages))
	}
	if len(client.contextMessages) != nativeContextKeepRecentMessages+2 {
		t.Fatalf("context request messages = %d, want system + recent history + new", len(client.contextMessages))
	}
	for _, msg := range client.contextMessages {
		if msg.Content == "old message a" {
			t.Fatalf("request included compacted old message: %#v", client.contextMessages)
		}
	}
	if client.context.IsEmpty() {
		t.Fatal("provider context was not passed to context-aware request")
	}
	if sessions.saved == nil {
		t.Fatal("history was not saved")
	}
	if got, want := len(sessions.saved.Messages), nativeContextKeepRecentMessages+2; got != want {
		t.Fatalf("saved messages = %d, want %d", got, want)
	}
	if sessions.saved.Messages[0].Content != "old message d" {
		t.Fatalf("first saved message = %#v, want first retained recent message", sessions.saved.Messages[0])
	}
	ctx := sessions.saved.ProviderContexts["deepseek"]
	if ctx.Provider != "openai" || ctx.Endpoint != "responses" || len(ctx.Items) != 1 {
		t.Fatalf("saved provider context = %#v, want openai responses compact context", ctx)
	}
}

func TestHandleTextContinuesWhenNativeCompactNotTriggered(t *testing.T) {
	var history []store.Message
	for i := 0; i < nativeContextKeepRecentMessages+3; i++ {
		history = append(history, store.Message{Role: "user", Content: "old message " + string(rune('a'+i))})
	}
	sessions := &fakeSessions{
		conv: &store.Conversation{Messages: history},
	}
	client := &fakeNativeLLM{compactErr: llm.ErrCompactionNotTriggered}
	cfg := config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {
				Provider:      "openai",
				BaseURL:       "https://llm.test",
				APIKey:        "key",
				ID:            "model",
				Endpoint:      "responses",
				ContextWindow: 4,
				Compact:       config.LLMCompactConfig{Mode: config.CompactModeAuto, Threshold: 0.25},
			},
		},
		SystemPrompt: "system",
	}
	b := &Bot{
		Sessions:            sessions,
		LLMConfig:           cfg,
		LLMClients:          map[string]llm.Client{},
		NewLLM:              func(config.ResolvedModel) llm.Client { return client },
		TextChunkLimit:      testTextChunkLimit,
		EnableTextStreaming: true,
	}
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "new", LLMText: "new"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.compactStarts) != 1 {
		t.Fatalf("compact starts = %#v, want one attempted compact notice", sender.compactStarts)
	}
	if len(sender.compactFinishes) != 0 {
		t.Fatalf("compact finishes = %#v, want none when compaction was not triggered", sender.compactFinishes)
	}
	if len(client.compactedMessages) != 3 {
		t.Fatalf("compacted messages = %d, want attempted compaction of 3 old messages", len(client.compactedMessages))
	}
	if !client.called {
		t.Fatal("LLM request was not sent after compaction was not triggered")
	}
	if sessions.saved == nil {
		t.Fatal("history was not saved")
	}
	if got, want := len(sessions.saved.Messages), len(history)+2; got != want {
		t.Fatalf("saved messages = %d, want original history plus user/assistant %d", got, want)
	}
	if len(sessions.saved.ProviderContexts) != 0 {
		t.Fatalf("provider contexts = %#v, want none when compaction was not triggered", sessions.saved.ProviderContexts)
	}
	if len(sender.sent) != 1 || sender.sent[0].Text != "hello" {
		t.Fatalf("sent = %#v, want normal reply", sender.sent)
	}
}

func TestHandleCompactCommandCompactsAndSavesRecentHistory(t *testing.T) {
	var history []store.Message
	for i := 0; i < nativeContextKeepRecentMessages+2; i++ {
		history = append(history, store.Message{Role: "user", Content: "old message " + string(rune('a'+i))})
	}
	sessions := &fakeSessions{conv: &store.Conversation{Messages: history}}
	client := &fakeNativeLLM{}
	cfg := config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {
				Provider:      "openai",
				BaseURL:       "https://llm.test",
				APIKey:        "key",
				ID:            "model",
				Endpoint:      "responses",
				ContextWindow: 128000,
				Compact:       config.LLMCompactConfig{Mode: config.CompactModeAuto, Threshold: 0.9},
			},
		},
		SystemPrompt: "system",
	}
	b := &Bot{
		Sessions:       sessions,
		LLMConfig:      cfg,
		LLMClients:     map[string]llm.Client{},
		NewLLM:         func(config.ResolvedModel) llm.Client { return client },
		TextChunkLimit: testTextChunkLimit,
	}
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "/compact", LLMText: "/compact"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.compactStarts) != 1 || !sender.compactStarts[0].Manual || sender.compactStarts[0].CompactedMessages != 2 {
		t.Fatalf("compact starts = %#v, want one manual notice", sender.compactStarts)
	}
	if len(sender.compactFinishes) != 1 || sender.compactFinishes[0].RetainedMessages != nativeContextKeepRecentMessages {
		t.Fatalf("compact finishes = %#v, want one manual success notice", sender.compactFinishes)
	}
	if len(client.compactedMessages) != 2 {
		t.Fatalf("compacted messages = %d, want 2", len(client.compactedMessages))
	}
	if sessions.saved == nil {
		t.Fatal("history was not saved")
	}
	if got, want := len(sessions.saved.Messages), nativeContextKeepRecentMessages; got != want {
		t.Fatalf("saved messages = %d, want %d", got, want)
	}
	if sessions.saved.ProviderContexts["deepseek"].IsEmpty() {
		t.Fatalf("saved provider contexts = %#v, want compact context", sessions.saved.ProviderContexts)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %#v, want compact success handled by notice sender", sender.sent)
	}
}

func TestHandleCompactCommandContinuesWhenNoticeFails(t *testing.T) {
	var history []store.Message
	for i := 0; i < nativeContextKeepRecentMessages+2; i++ {
		history = append(history, store.Message{Role: "user", Content: "old message " + string(rune('a'+i))})
	}
	sessions := &fakeSessions{conv: &store.Conversation{Messages: history}}
	client := &fakeNativeLLM{}
	cfg := config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {
				Provider:      "openai",
				BaseURL:       "https://llm.test",
				APIKey:        "key",
				ID:            "model",
				Endpoint:      "responses",
				ContextWindow: 128000,
				Compact:       config.LLMCompactConfig{Mode: config.CompactModeAuto, Threshold: 0.9},
			},
		},
		SystemPrompt: "system",
	}
	b := &Bot{
		Sessions:       sessions,
		LLMConfig:      cfg,
		LLMClients:     map[string]llm.Client{},
		NewLLM:         func(config.ResolvedModel) llm.Client { return client },
		TextChunkLimit: testTextChunkLimit,
	}
	sender := &fakeSender{
		compactStartErr:  errors.New("notice start failed"),
		compactFinishErr: errors.New("notice finish failed"),
	}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "/compact", LLMText: "/compact"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if sessions.saved == nil {
		t.Fatal("history was not saved")
	}
	if len(sender.compactStarts) != 1 || len(sender.compactFinishes) != 1 {
		t.Fatalf("compact notices = %#v/%#v, want attempted start and finish", sender.compactStarts, sender.compactFinishes)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %#v, want no fallback text from failing notice sender", sender.sent)
	}
}

func TestHandleCompactCommandNotTriggeredDoesNotSave(t *testing.T) {
	var history []store.Message
	for i := 0; i < nativeContextKeepRecentMessages+2; i++ {
		history = append(history, store.Message{Role: "user", Content: "old message " + string(rune('a'+i))})
	}
	sessions := &fakeSessions{conv: &store.Conversation{Messages: history}}
	client := &fakeNativeLLM{compactErr: llm.ErrCompactionNotTriggered}
	cfg := config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {
				Provider:      "openai",
				BaseURL:       "https://llm.test",
				APIKey:        "key",
				ID:            "model",
				Endpoint:      "responses",
				ContextWindow: 128000,
				Compact:       config.LLMCompactConfig{Mode: config.CompactModeAuto, Threshold: 0.9},
			},
		},
		SystemPrompt: "system",
	}
	b := &Bot{
		Sessions:       sessions,
		LLMConfig:      cfg,
		LLMClients:     map[string]llm.Client{},
		NewLLM:         func(config.ResolvedModel) llm.Client { return client },
		TextChunkLimit: testTextChunkLimit,
	}
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "/compact", LLMText: "/compact"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.compactStarts) != 1 {
		t.Fatalf("compact starts = %#v, want one attempted manual compact notice", sender.compactStarts)
	}
	if len(sender.compactFinishes) != 0 {
		t.Fatalf("compact finishes = %#v, want none when manual compaction was not triggered", sender.compactFinishes)
	}
	if len(client.compactedMessages) != 2 {
		t.Fatalf("compacted messages = %d, want attempted compact of 2 old messages", len(client.compactedMessages))
	}
	if sessions.saved != nil {
		t.Fatalf("saved = %#v, want no saved history when compaction was not triggered", sessions.saved)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Text, "未达到供应商原生压缩触发阈值") {
		t.Fatalf("sent = %#v, want not-triggered compact notice", sender.sent)
	}
}

func TestHandleCompactCommandDisabledByMode(t *testing.T) {
	sessions := &fakeSessions{conv: &store.Conversation{Messages: []store.Message{{Role: "user", Content: "old"}}}}
	client := &fakeNativeLLM{}
	cfg := config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {
				Provider: "openai",
				BaseURL:  "https://llm.test",
				APIKey:   "key",
				ID:       "model",
				Endpoint: "chat",
				Compact:  config.LLMCompactConfig{Mode: config.CompactModeFalse, Threshold: 0.9},
			},
		},
		SystemPrompt: "system",
	}
	b := &Bot{
		Sessions:   sessions,
		LLMConfig:  cfg,
		LLMClients: map[string]llm.Client{},
		NewLLM:     func(config.ResolvedModel) llm.Client { return client },
	}
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "/compact", LLMText: "/compact"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(client.compactedMessages) != 0 {
		t.Fatalf("compacted messages = %#v, want no compaction", client.compactedMessages)
	}
	if len(sender.compactStarts) != 0 || len(sender.compactFinishes) != 0 {
		t.Fatalf("compact notices = %#v/%#v, want none when compact is disabled", sender.compactStarts, sender.compactFinishes)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Text, "已禁用") {
		t.Fatalf("sent = %#v, want disabled compact error", sender.sent)
	}
}

func TestHandleCompactCommandUnsupportedProviderErrors(t *testing.T) {
	sessions := &fakeSessions{conv: &store.Conversation{Messages: []store.Message{{Role: "user", Content: "old"}}}}
	client := &fakeLLM{}
	b := testBot(sessions, client)
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "/compact", LLMText: "/compact"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if client.called {
		t.Fatal("LLM chat was called for unsupported /compact")
	}
	if len(sender.compactStarts) != 0 || len(sender.compactFinishes) != 0 {
		t.Fatalf("compact notices = %#v/%#v, want none for unsupported compact", sender.compactStarts, sender.compactFinishes)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Text, "不支持上下文压缩") {
		t.Fatalf("sent = %#v, want unsupported compact error", sender.sent)
	}
}

func TestNewDisablesTextStreamingByDefault(t *testing.T) {
	b := New(&fakeSessions{}, config.LLMConfig{})
	if b.EnableTextStreaming {
		t.Fatal("EnableTextStreaming = true, want false")
	}
}

func TestHandleTextStreamsFirstChunkWhenSenderSupportsStream(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{
		streamChunks: []string{"hel", "lo"},
		resp:         llm.Response{Text: "hello"},
	}
	b := testBot(sessions, client)
	sender := &fakeStreamingSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if sender.stream == nil {
		t.Fatal("stream was not started")
	}
	if got, want := sender.stream.updates, []string{"hel", "hello"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("stream updates = %#v, want %#v", got, want)
	}
	if got, want := sender.stream.finishes, []string{"hello"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("stream finishes = %#v, want %#v", got, want)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %#v, want no duplicate text send", sender.sent)
	}
	if sessions.saved == nil || len(sessions.saved.Messages) != 2 {
		t.Fatalf("saved = %#v, want history saved", sessions.saved)
	}
}

func TestHandleTextStreamsPreserveRawMarkdown(t *testing.T) {
	text := "```bash\nsudo dnf update -y\n```\ninline `code`"
	sessions := &fakeSessions{}
	client := &fakeLLM{
		streamChunks: []string{"```bash\n", "sudo dnf update -y\n```", "\ninline `code`"},
		resp:         llm.Response{Text: text},
	}
	b := testBot(sessions, client)
	sender := &fakeStreamingSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if sender.stream == nil {
		t.Fatal("stream was not started")
	}
	if got := sender.stream.updates[len(sender.stream.updates)-1]; got != text {
		t.Fatalf("last stream update = %q, want raw markdown %q", got, text)
	}
	if got := sender.stream.finishes; len(got) != 1 || got[0] != text {
		t.Fatalf("stream finishes = %#v, want raw markdown", got)
	}
}

func TestHandleTextStreamingDisabledUsesFinalChunkedSend(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{
		streamChunks: []string{"abc", "def"},
		resp:         llm.Response{Text: "abcdef"},
	}
	b := testBot(sessions, client)
	b.TextChunkLimit = 3
	b.EnableTextStreaming = false
	sender := &fakeStreamingSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.streams) != 0 {
		t.Fatalf("streams = %d, want 0 when streaming disabled", len(sender.streams))
	}
	if got, want := len(sender.sent), 2; got != want {
		t.Fatalf("sent messages = %d, want %d", got, want)
	}
	if sender.sent[0].Text != "abc" || sender.sent[1].Text != "def" {
		t.Fatalf("sent = %#v, want abc/def chunks", sender.sent)
	}
}

func TestHandleTextStreamingDisabledSendsRawMarkdown(t *testing.T) {
	text := "```bash\nsudo dnf update -y\n```\ninline `code`"
	sessions := &fakeSessions{}
	client := &fakeLLM{resp: llm.Response{Text: text}}
	b := testBot(sessions, client)
	b.EnableTextStreaming = false
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].Text != text {
		t.Fatalf("sent = %#v, want raw markdown", sender.sent)
	}
}

func TestHandleTextStreamsFirstChunkAndSendsOverflow(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{
		streamChunks: []string{"abc", "def"},
		resp:         llm.Response{Text: "abcdef"},
	}
	b := testBot(sessions, client)
	b.TextChunkLimit = 3
	sender := &fakeStreamingSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.streams) != 2 {
		t.Fatalf("streams = %d, want 2", len(sender.streams))
	}
	if got, want := sender.streams[0].finishes, []string{"abc"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("stream finishes = %#v, want %#v", got, want)
	}
	if got, want := sender.streams[1].finishes, []string{"def"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("second stream finishes = %#v, want %#v", got, want)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %#v, want no duplicate overflow send", sender.sent)
	}
}

func TestHandleTextStreamingUnavailableUsesFinalChunkedSend(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{
		streamChunks: []string{"abc", "def"},
		resp:         llm.Response{Text: "abcdef"},
	}
	b := testBot(sessions, client)
	b.TextChunkLimit = 3
	sender := &fakeSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got, want := len(sender.sent), 2; got != want {
		t.Fatalf("sent messages = %d, want %d", got, want)
	}
	if sender.sent[0].Text != "abc" || sender.sent[1].Text != "def" {
		t.Fatalf("sent = %#v, want abc/def chunks", sender.sent)
	}
}

func TestHandleTextStreamingFinalUncreatedTailFallsBackToSend(t *testing.T) {
	sessions := &fakeSessions{}
	client := &fakeLLM{
		streamChunks: []string{"abc"},
		resp:         llm.Response{Text: "abcdef"},
	}
	b := testBot(sessions, client)
	b.TextChunkLimit = 3
	sender := &fakeStreamingSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.streams) != 1 {
		t.Fatalf("streams = %d, want 1", len(sender.streams))
	}
	if got, want := sender.streams[0].finishes, []string{"abc"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("stream finishes = %#v, want %#v", got, want)
	}
	if len(sender.sent) != 1 || sender.sent[0].Text != "def" {
		t.Fatalf("sent = %#v, want uncreated tail def", sender.sent)
	}
}

func TestHandleTextStreamsSplitLinesAndPreserveUTF8(t *testing.T) {
	text := "第一行\n第二行\n第三行"
	sessions := &fakeSessions{}
	client := &fakeLLM{
		streamChunks: []string{text},
		resp:         llm.Response{Text: text},
	}
	b := testBot(sessions, client)
	b.TextChunkLimit = len("第一行\n第二")
	sender := &fakeStreamingSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.streams) != 3 {
		t.Fatalf("streams = %d, want 3", len(sender.streams))
	}
	var got []string
	for i, stream := range sender.streams {
		if len(stream.finishes) != 1 {
			t.Fatalf("stream %d finishes = %#v, want one finish", i+1, stream.finishes)
		}
		if !utf8.ValidString(stream.finishes[0]) {
			t.Fatalf("stream %d finish is invalid UTF-8: %q", i+1, stream.finishes[0])
		}
		got = append(got, stream.finishes[0])
	}
	if joined := strings.Join(got, ""); joined != text {
		t.Fatalf("joined stream text = %q, want %q", joined, text)
	}
	if got[0] != "第一行\n" || got[1] != "第二行\n" || got[2] != "第三行" {
		t.Fatalf("stream chunks = %#v, want line chunks", got)
	}
}

func TestHandleTextStreamsSplitLongLineWithoutBreakingUTF8(t *testing.T) {
	text := "网络端口🙂网络端口"
	sessions := &fakeSessions{}
	client := &fakeLLM{
		streamChunks: []string{text},
		resp:         llm.Response{Text: text},
	}
	b := testBot(sessions, client)
	b.TextChunkLimit = 5
	sender := &fakeStreamingSender{}

	if err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(sender.streams) < 2 {
		t.Fatalf("streams = %d, want multiple streams", len(sender.streams))
	}
	var got []string
	for i, stream := range sender.streams {
		if len(stream.finishes) != 1 {
			t.Fatalf("stream %d finishes = %#v, want one finish", i+1, stream.finishes)
		}
		chunk := stream.finishes[0]
		if !utf8.ValidString(chunk) {
			t.Fatalf("stream %d finish is invalid UTF-8: %q", i+1, chunk)
		}
		got = append(got, chunk)
	}
	if joined := strings.Join(got, ""); joined != text {
		t.Fatalf("joined stream text = %q, want %q", joined, text)
	}
}

func TestHandleTextStreamErrorUpdatesNoticeAndDoesNotSaveHistory(t *testing.T) {
	sessions := &fakeSessions{}
	streamErr := errors.New("stream broke")
	client := &fakeLLM{
		streamChunks: []string{"partial"},
		streamErr:    streamErr,
	}
	b := testBot(sessions, client)
	sender := &fakeStreamingSender{}

	err := b.Handle(context.Background(), InboundMessage{UserKey: "user", CommandText: "hi", LLMText: "hi"}, sender)
	if !errors.Is(err, streamErr) {
		t.Fatalf("Handle error = %v, want streamErr", err)
	}
	if sender.stream == nil {
		t.Fatal("stream was not started")
	}
	if len(sender.stream.finishes) != 1 || !strings.Contains(sender.stream.finishes[0], "AI 响应失败") {
		t.Fatalf("stream finishes = %#v, want error notice", sender.stream.finishes)
	}
	if sessions.saved != nil {
		t.Fatalf("saved = %#v, want no failed assistant history", sessions.saved)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %#v, want stream error notice without fallback send", sender.sent)
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

func TestSplitTextChunksPrefersLineBoundaries(t *testing.T) {
	text := "第一行\n\n第二行\n第三行"
	chunks := SplitTextChunks(text, len("第一行\n第二"))

	if got, want := strings.Join(chunks, ""), text; got != want {
		t.Fatalf("joined chunks = %q, want %q", got, want)
	}
	want := []string{"第一行\n\n", "第二行\n", "第三行"}
	if len(chunks) != len(want) {
		t.Fatalf("chunks = %#v, want %#v", chunks, want)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i+1, chunks[i], want[i])
		}
		if !utf8.ValidString(chunks[i]) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i+1, chunks[i])
		}
	}
}

func TestSplitTextChunksSplitsLongLineOnUTF8Boundary(t *testing.T) {
	text := "网络端口🙂网络端口"
	chunks := SplitTextChunks(text, 5)

	if len(chunks) < 2 {
		t.Fatalf("chunks = %#v, want multiple chunks", chunks)
	}
	if got, want := strings.Join(chunks, ""), text; got != want {
		t.Fatalf("joined chunks = %q, want %q", got, want)
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i+1, chunk)
		}
	}
}

func TestSplitTextChunksKeepsRuneWhenLimitSmallerThanRune(t *testing.T) {
	text := "网络"
	chunks := SplitTextChunks(text, 1)

	if got, want := strings.Join(chunks, ""), text; got != want {
		t.Fatalf("joined chunks = %q, want %q", got, want)
	}
	if len(chunks) != 2 || chunks[0] != "网" || chunks[1] != "络" {
		t.Fatalf("chunks = %#v, want individual runes", chunks)
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i+1, chunk)
		}
	}
}

func TestSplitTextChunksByRunesUsesCharacterLimit(t *testing.T) {
	text := strings.Repeat("界", 5)
	chunks := SplitTextChunksByRunes(text, 2)

	if len(chunks) != 3 {
		t.Fatalf("chunks = %#v, want 3 chunks", chunks)
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i+1, chunk)
		}
		if got := len([]rune(chunk)); got > 2 {
			t.Fatalf("chunk %d rune length = %d, want <= 2", i+1, got)
		}
	}
	if got := strings.Join(chunks, ""); got != text {
		t.Fatalf("joined chunks = %q, want %q", got, text)
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
		Sessions:            sessions,
		LLMConfig:           cfg,
		LLMClients:          map[string]llm.Client{},
		NewLLM:              func(config.ResolvedModel) llm.Client { return client },
		TextChunkLimit:      testTextChunkLimit,
		EnableTextStreaming: true,
	}
}
