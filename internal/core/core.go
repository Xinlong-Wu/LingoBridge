package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"lingobridge/internal/commands"
	"lingobridge/internal/config"
	"lingobridge/internal/llm"
	"lingobridge/internal/logging"
	"lingobridge/internal/store"
)

const (
	AIErrorSummaryRunes = 300
)

var (
	bearerTokenPattern = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]+`)
	openAIKeyPattern   = regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)
	hexTokenPattern    = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	coreLog            = logging.For("core")
)

type Platform interface {
	Run(ctx context.Context, handler Handler) error
}

type Handler interface {
	Handle(ctx context.Context, msg InboundMessage, sender Sender) error
}

type Sender interface {
	Send(ctx context.Context, msg OutboundMessage) error
	StartTyping(ctx context.Context) func()
}

type TextStreamSender interface {
	StartTextStream(ctx context.Context) (TextStream, error)
}

type TextStream interface {
	Update(ctx context.Context, text string) error
	Finish(ctx context.Context, text string) error
}

type InboundMessage struct {
	Platform           string
	AccountID          string
	UserKey            string
	CommandText        string
	CommandPolicy      commands.Policy
	LLMText            string
	PrepareUserMessage PrepareUserMessageFunc
	PrepareErrorNotice func(error) string
	MutateResponse     ResponseMutator
	ErrorNotice        func(error) string
	Metadata           map[string]string
}

type OutboundMessage struct {
	Text  string
	Image llm.Image
}

type PrepareUserMessageFunc func(ctx context.Context, userID, sessionID string, client llm.Client) (store.Message, error)

type ConversationManager interface {
	commands.SessionManager
	GetOrCreateCurrentSession(userID string) (*store.Session, error)
	LoadHistory(userID, sessionID string) (*store.Conversation, error)
	SaveHistory(userID, sessionID string, conv *store.Conversation) error
}

type LLMFactory func(config.ResolvedModel) llm.Client
type ResponseMutator func(userID, sessionID string, resp llm.Response) llm.Response

type Bot struct {
	Sessions            ConversationManager
	LLMConfig           config.LLMConfig
	LLMClients          map[string]llm.Client
	mu                  sync.Mutex
	NewLLM              LLMFactory
	MutateResponse      ResponseMutator
	ErrorNotice         func(error) string
	TextChunkLimit      int
	EnableTextStreaming bool
}

func New(sessions ConversationManager, cfg config.LLMConfig) *Bot {
	return &Bot{
		Sessions:            sessions,
		LLMConfig:           cfg,
		LLMClients:          map[string]llm.Client{},
		NewLLM:              defaultLLMFactory,
		EnableTextStreaming: false,
	}
}

func defaultLLMFactory(model config.ResolvedModel) llm.Client {
	return llm.NewClient(llm.Config{
		Provider: model.Provider,
		BaseURL:  model.BaseURL,
		APIKey:   model.APIKey,
		Model:    model.ID,
		Endpoint: model.Endpoint,
	})
}

func (b *Bot) Handle(ctx context.Context, msg InboundMessage, sender Sender) error {
	if msg.UserKey == "" {
		return nil
	}
	if resp, handled, err := commands.HandleWithPolicy(msg.CommandText, msg.UserKey, b.Sessions, msg.CommandPolicy); handled {
		if err != nil {
			coreLog.Warn(ctx, "command error: %v", err)
			_ = sender.Send(ctx, OutboundMessage{Text: fmt.Sprintf("❌ 错误：%v", err)})
			return nil
		}
		return sender.Send(ctx, OutboundMessage{Text: resp})
	}
	if strings.TrimSpace(msg.LLMText) == "" && msg.PrepareUserMessage == nil {
		return nil
	}
	return b.reply(ctx, msg, sender)
}

func (b *Bot) reply(ctx context.Context, msg InboundMessage, sender Sender) error {
	sess, err := b.Sessions.GetOrCreateCurrentSession(msg.UserKey)
	if err != nil {
		coreLog.Error(ctx, "get session: %v", err)
		_ = sender.Send(ctx, OutboundMessage{Text: "❌ 会话加载失败，请重试。"})
		return err
	}

	modelName, provider, llmClient, err := b.llmForUser(msg.UserKey)
	if err != nil {
		coreLog.Error(ctx, "resolve LLM: %v", err)
		_ = sender.Send(ctx, OutboundMessage{Text: "❌ 模型配置不可用，请检查配置。"})
		return err
	}

	userMsg, err := b.prepareUserMessage(ctx, msg, sess.ID, llmClient)
	if err != nil {
		coreLog.Warn(ctx, "prepare user message failed provider=%s model=%s: %v", provider, modelName, err)
		_ = sender.Send(ctx, OutboundMessage{Text: b.prepareErrorNotice(msg, err)})
		return err
	}
	if userMsg.Content == "" && len(userMsg.Attachments) == 0 {
		return nil
	}

	conv, err := b.Sessions.LoadHistory(msg.UserKey, sess.ID)
	if err != nil {
		coreLog.Warn(ctx, "load history: %v", err)
		conv = &store.Conversation{}
	}

	msgs := ToLLMMessagesWithUserMessage(b.LLMConfig.SystemPrompt, conv, userMsg, b.LLMConfig.MaxHistory)

	var textStream *replyTextStream
	if b.EnableTextStreaming {
		if streamSender, ok := sender.(TextStreamSender); ok {
			textStream = newReplyTextStream(streamSender, b.chunkLimit())
		}
	}
	var onChunk func(string) error
	if textStream != nil {
		onChunk = func(chunk string) error {
			return textStream.OnChunk(ctx, chunk)
		}
	}

	stopTyping := sender.StartTyping(ctx)
	llmResponse, err := llmClient.ChatStream(b.LLMConfig.SystemPrompt, msgs, onChunk)
	stopTyping()
	if err != nil {
		coreLog.Error(ctx, "LLM error provider=%s model=%s: %v", provider, modelName, err)
		notice := b.errorNotice(msg, err)
		if textStream != nil && textStream.Started() {
			if streamErr := textStream.Finish(ctx, notice); streamErr == nil {
				return err
			}
		}
		_ = sender.Send(ctx, OutboundMessage{Text: notice})
		return err
	}
	if msg.MutateResponse != nil {
		llmResponse = msg.MutateResponse(msg.UserKey, sess.ID, llmResponse)
	} else if b.MutateResponse != nil {
		llmResponse = b.MutateResponse(msg.UserKey, sess.ID, llmResponse)
	}

	assistantHistory, err := llmClient.AssistantMessage(llmResponse)
	if err != nil {
		coreLog.Error(ctx, "prepare assistant history failed provider=%s model=%s: %v", provider, modelName, err)
		_ = sender.Send(ctx, OutboundMessage{Text: b.errorNotice(msg, err)})
		return err
	}
	conv.Messages = append(conv.Messages, userMsg, assistantHistory)

	if err := b.Sessions.SaveHistory(msg.UserKey, sess.ID, conv); err != nil {
		coreLog.Warn(ctx, "save history: %v", err)
	}

	if llmResponse.Text != "" {
		chunks := SplitTextChunks(llmResponse.Text, b.chunkLimit())
		start := 0
		if textStream != nil {
			if err := textStream.Finish(ctx, llmResponse.Text); err != nil {
				return err
			}
			start = textStream.FinishedChunks(len(chunks))
		}
		for _, chunk := range chunks[start:] {
			if err := sender.Send(ctx, OutboundMessage{Text: chunk}); err != nil {
				return err
			}
		}
	}

	for i, image := range llmResponse.Images {
		if err := sender.Send(ctx, OutboundMessage{Image: image}); err != nil {
			coreLog.Error(ctx, "send image failed image=%d/%d: %v", i+1, len(llmResponse.Images), err)
			_ = sender.Send(ctx, OutboundMessage{Text: b.errorNotice(msg, err)})
			return err
		}
	}

	coreLog.Debug(ctx, "reply to=%s provider=%s model=%s len=%d images=%d", msg.UserKey, provider, modelName, len(llmResponse.Text), len(llmResponse.Images))
	return nil
}

func (b *Bot) prepareUserMessage(ctx context.Context, msg InboundMessage, sessionID string, llmClient llm.Client) (store.Message, error) {
	if msg.PrepareUserMessage != nil {
		return msg.PrepareUserMessage(ctx, msg.UserKey, sessionID, llmClient)
	}
	return llmClient.PrepareUserMessage(msg.LLMText, nil)
}

func (b *Bot) llmForUser(userID string) (string, string, llm.Client, error) {
	modelName, err := b.Sessions.CurrentModel(userID)
	if err != nil {
		return "", "", nil, err
	}
	model, err := b.LLMConfig.ResolveModel(modelName)
	if err != nil {
		model, err = b.LLMConfig.ResolveModel(b.LLMConfig.DefaultModel)
		if err != nil {
			return "", "", nil, err
		}
	}
	newLLM := b.NewLLM
	if newLLM == nil {
		newLLM = defaultLLMFactory
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.LLMClients == nil {
		b.LLMClients = map[string]llm.Client{}
	}
	client, ok := b.LLMClients[model.Name]
	if !ok {
		client = newLLM(model)
		b.LLMClients[model.Name] = client
	}
	return model.Name, model.Provider, client, nil
}

func (b *Bot) chunkLimit() int {
	if b.TextChunkLimit <= 0 {
		return -1
	}
	return b.TextChunkLimit
}

func (b *Bot) prepareErrorNotice(msg InboundMessage, err error) string {
	if msg.PrepareErrorNotice != nil {
		return msg.PrepareErrorNotice(err)
	}
	return b.errorNotice(msg, err)
}

func (b *Bot) errorNotice(msg InboundMessage, err error) string {
	if msg.ErrorNotice != nil {
		return msg.ErrorNotice(err)
	}
	if b.ErrorNotice != nil {
		return b.ErrorNotice(err)
	}
	return AIErrorNotice(err)
}

func AIErrorNotice(err error) string {
	summary := SummarizeError(err, AIErrorSummaryRunes)
	if summary == "" {
		summary = "未知错误"
	}
	return "❌ AI 响应失败：" + summary
}

func SummarizeError(err error, maxRunes int) string {
	if err == nil {
		return ""
	}
	summary := err.Error()
	summary = bearerTokenPattern.ReplaceAllString(summary, "Bearer [REDACTED]")
	summary = openAIKeyPattern.ReplaceAllString(summary, "sk-[REDACTED]")
	summary = hexTokenPattern.ReplaceAllString(summary, "[REDACTED]")
	summary = strings.Join(strings.Fields(summary), " ")
	return TruncateRunes(summary, maxRunes)
}

func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

var ErrUnsupportedImage = errors.New("platform does not support sending images")
