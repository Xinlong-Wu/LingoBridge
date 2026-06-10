package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"lingobridge/internal/commands"
	"lingobridge/internal/config"
	"lingobridge/internal/llm"
	"lingobridge/internal/logging"
	"lingobridge/internal/store"
	tooltypes "lingobridge/internal/tools"
)

var coreLog = logging.For("core")

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
	Tools              []tooltypes.Tool
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
	ToolProvider        tooltypes.Provider
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

func (b *Bot) Handle(ctx context.Context, msg InboundMessage, sender Sender) error {
	if msg.UserKey == "" {
		return nil
	}
	if isCompactCommand(msg.CommandText) {
		return b.handleCompactCommand(ctx, msg, sender)
	}
	msg.Tools = b.toolsForMessage(ctx, msg.Tools)
	commandTools := commandToolSummaries(msg.Tools)
	if resp, handled, err := commands.HandleWithOptions(msg.CommandText, msg.UserKey, b.Sessions, commands.HandleOptions{
		Policy: msg.CommandPolicy,
		Tools:  commandTools,
	}); handled {
		if err != nil {
			coreLog.Warn(ctx, "command error: %v", err)
			_ = sender.Send(ctx, OutboundMessage{Text: fmt.Sprintf("❌ 错误：%v", err)})
			return nil
		}
		coreLog.Debug(ctx, "command handled command=%s tools=%d", commandName(msg.CommandText), len(commandTools))
		return sender.Send(ctx, OutboundMessage{Text: resp})
	}
	if strings.TrimSpace(msg.LLMText) == "" && msg.PrepareUserMessage == nil {
		return nil
	}
	return b.reply(ctx, msg, sender)
}

func (b *Bot) toolsForMessage(ctx context.Context, messageTools []tooltypes.Tool) []tooltypes.Tool {
	var provided []tooltypes.Tool
	if b.ToolProvider != nil {
		provided = b.ToolProvider.Tools()
	}
	tools := mergeTools(ctx, messageTools, provided)
	if len(tools) != len(messageTools)+len(provided) {
		coreLog.Debug(ctx, "merged tools platform=%d provider=%d effective=%d", len(messageTools), len(provided), len(tools))
	}
	return tools
}

func (b *Bot) toolOptions() tooltypes.Options {
	if b.ToolProvider == nil {
		return tooltypes.Options{}
	}
	return b.ToolProvider.ToolOptions()
}

func (b *Bot) reply(ctx context.Context, msg InboundMessage, sender Sender) error {
	sess, err := b.Sessions.GetOrCreateCurrentSession(msg.UserKey)
	if err != nil {
		coreLog.Error(ctx, "get session: %v", err)
		_ = sender.Send(ctx, OutboundMessage{Text: "❌ 会话加载失败，请重试。"})
		return err
	}

	model, llmClient, err := b.llmForUser(msg.UserKey)
	if err != nil {
		coreLog.Error(ctx, "resolve LLM: %v", err)
		_ = sender.Send(ctx, OutboundMessage{Text: "❌ 模型配置不可用，请检查配置。"})
		return err
	}
	modelName, provider := model.Name, model.Provider

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

	compact := llm.CompactConfig{
		Mode:          string(model.Compact.Mode),
		ContextWindow: model.ContextWindow,
		Threshold:     model.Compact.Threshold,
		Instructions:  model.Compact.Instructions,
	}
	historyForRequest := conv.Messages
	providerContext := providerContextForModel(conv, modelName)
	preCompacted := false
	var compactNoticeHandle CompactNoticeHandle
	var preCompactNotice CompactNotice
	compactAllowed := automaticCompactAllowed(compact)
	if compactAllowed {
		var compactErr error
		historyForRequest, providerContext, preCompacted, compactErr = b.prepareNativeContext(b.LLMConfig.SystemPrompt, historyForRequest, userMsg, providerContext, compact, llmClient, func(compactedMessages, retainedMessages int) {
			preCompactNotice = CompactNotice{
				ModelName:         modelName,
				Manual:            false,
				CompactedMessages: compactedMessages,
				RetainedMessages:  retainedMessages,
			}
			compactNoticeHandle = startCompactNotice(ctx, sender, preCompactNotice)
		})
		if compactErr != nil {
			coreLog.Error(ctx, "compact context failed provider=%s model=%s: %v", provider, modelName, compactErr)
			_ = sender.Send(ctx, OutboundMessage{Text: b.errorNotice(msg, compactErr)})
			return compactErr
		}
	}

	msgs := ToLLMMessagesWithUserMessage(b.LLMConfig.SystemPrompt, &store.Conversation{Messages: historyForRequest}, userMsg, b.LLMConfig.MaxHistory)

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
	var llmResponse llm.Response
	var toolTraces []store.ToolTrace
	if len(msg.Tools) > 0 {
		if toolClient, ok := llmClient.(llm.ToolCallingClient); ok {
			llmResponse, toolTraces, err = b.chatWithTools(ctx, toolClient, b.LLMConfig.SystemPrompt, msgs, providerContext, compact, compactAllowed, msg.Tools, onChunk)
		} else {
			coreLog.Warn(ctx, "model provider=%s model=%s does not support tool calling; continuing without tools", provider, modelName)
			llmResponse, err = b.chatWithoutTools(llmClient, compactAllowed, providerContext, compact, msgs, onChunk)
		}
	} else {
		llmResponse, err = b.chatWithoutTools(llmClient, compactAllowed, providerContext, compact, msgs, onChunk)
	}
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
	assistantHistory.ToolTraces = toolTraces
	historyForSave := conv.Messages
	if compactAllowed {
		if preCompacted {
			historyForSave = historyForRequest
		}
		if llmResponse.Compacted {
			historyForSave = retainRecentMessages(historyForSave, nativeContextKeepRecentMessages)
		}
		if !llmResponse.ProviderContext.IsEmpty() {
			providerContext = llmResponse.ProviderContext
		}
		if preCompacted || llmResponse.Compacted || !providerContext.IsEmpty() {
			if conv.ProviderContexts == nil {
				conv.ProviderContexts = map[string]store.ProviderContext{}
			}
			if !providerContext.IsEmpty() {
				conv.ProviderContexts[modelName] = providerContext
			}
		}
	}
	conv.Messages = append(historyForSave, userMsg, assistantHistory)

	if err := b.Sessions.SaveHistory(msg.UserKey, sess.ID, conv); err != nil {
		coreLog.Warn(ctx, "save history: %v", err)
	} else if preCompacted {
		if err := finishCompactNotice(ctx, sender, compactNoticeHandle, preCompactNotice); err != nil {
			return err
		}
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

func (b *Bot) chatWithoutTools(client llm.Client, compactAllowed bool, providerContext store.ProviderContext, compact llm.CompactConfig, msgs []store.Message, onChunk func(string) error) (llm.Response, error) {
	if compactAllowed {
		if contextClient, ok := client.(llm.ContextStreamingClient); ok {
			return contextClient.ChatStreamWithContext(b.LLMConfig.SystemPrompt, msgs, providerContext, compact, onChunk)
		}
	}
	return client.ChatStream(b.LLMConfig.SystemPrompt, msgs, onChunk)
}

func (b *Bot) chatWithTools(ctx context.Context, client llm.ToolCallingClient, systemPrompt string, msgs []store.Message, providerContext store.ProviderContext, compact llm.CompactConfig, compactAllowed bool, tools []tooltypes.Tool, onChunk func(string) error) (llm.Response, []store.ToolTrace, error) {
	specs := toolSpecs(tools)
	if len(specs) == 0 {
		coreLog.Warn(ctx, "tool calling requested with %d tool entries but no valid tool specs; falling back to plain chat", len(tools))
		baseClient, ok := client.(llm.Client)
		if !ok {
			return llm.Response{}, nil, fmt.Errorf("tool-capable client does not implement base chat")
		}
		resp, err := b.chatWithoutTools(baseClient, compactAllowed, providerContext, compact, msgs, onChunk)
		return resp, nil, err
	}

	options := b.toolOptions()
	maxCalls := effectiveMaxToolCalls(options.MaxCalls)
	lookup := toolMap(tools)
	coreLog.Debug(ctx, "tool loop start tools=%d max_calls=%d timeout=%s result_limit=%d", len(specs), maxCalls, effectiveToolTimeout(options.Timeout), effectiveToolResultLimit(options.ResultLimit))
	var traces []store.ToolTrace
	var previous llm.ToolState
	var results []tooltypes.Result
	totalCalls := 0
	effectiveCompact := llm.CompactConfig{}
	if compactAllowed {
		effectiveCompact = compact
	}

	for {
		resp, err := client.ChatStreamWithTools(systemPrompt, msgs, providerContext, effectiveCompact, specs, previous, results, onChunk)
		if err != nil {
			return llm.Response{}, traces, err
		}
		if len(resp.ToolCalls) == 0 {
			coreLog.Debug(ctx, "tool loop finish calls=%d traces=%d text_len=%d images=%d", totalCalls, len(traces), len(resp.Text), len(resp.Images))
			return resp.Response, traces, nil
		}
		if totalCalls+len(resp.ToolCalls) > maxCalls {
			err := fmt.Errorf("tool call limit exceeded: %d > %d", totalCalls+len(resp.ToolCalls), maxCalls)
			coreLog.Warn(ctx, "%v", err)
			return llm.Response{}, traces, err
		}

		previous = resp.ToolState
		results = results[:0]
		coreLog.Debug(ctx, "model requested tool calls count=%d total_before=%d", len(resp.ToolCalls), totalCalls)
		for _, call := range resp.ToolCalls {
			call.Name = strings.TrimSpace(call.Name)
			if _, ok := lookup[call.Name]; !ok {
				coreLog.Warn(ctx, "model requested unavailable tool name=%s call_id=%s", call.Name, call.ID)
			}
			totalCalls++
			result, trace := runTool(ctx, lookup[call.Name], call, options.Timeout, options.ResultLimit)
			results = append(results, result)
			traces = append(traces, trace)
			if trace.Status == "error" {
				coreLog.Warn(ctx, "tool call failed name=%s call_id=%s duration_ms=%d error=%s", trace.Name, trace.CallID, trace.DurationMillis, truncateText(trace.Error, defaultToolTraceTextLimit))
			} else {
				coreLog.Debug(ctx, "tool call finished name=%s call_id=%s status=%s duration_ms=%d", trace.Name, trace.CallID, trace.Status, trace.DurationMillis)
			}
		}
	}
}

func effectiveMaxToolCalls(maxCalls int) int {
	if maxCalls <= 0 {
		return defaultMaxToolCalls
	}
	return maxCalls
}

func effectiveToolTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultToolTimeout
	}
	return timeout
}

func effectiveToolResultLimit(limit int) int {
	if limit <= 0 {
		return defaultToolResultLimit
	}
	return limit
}

func (b *Bot) prepareUserMessage(ctx context.Context, msg InboundMessage, sessionID string, llmClient llm.Client) (store.Message, error) {
	if msg.PrepareUserMessage != nil {
		return msg.PrepareUserMessage(ctx, msg.UserKey, sessionID, llmClient)
	}
	return llmClient.PrepareUserMessage(msg.LLMText, nil)
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
