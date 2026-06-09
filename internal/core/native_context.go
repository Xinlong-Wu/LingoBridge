package core

import (
	"errors"
	"math"
	"unicode/utf8"

	"lingobridge/internal/llm"
	"lingobridge/internal/store"
)

const nativeContextKeepRecentMessages = 12
const compactModeFalse = "false"

func providerContextForModel(conv *store.Conversation, modelName string) store.ProviderContext {
	if conv == nil || conv.ProviderContexts == nil {
		return store.ProviderContext{}
	}
	return conv.ProviderContexts[modelName]
}

func (b *Bot) prepareNativeContext(systemPrompt string, history []store.Message, newMsg store.Message, providerContext store.ProviderContext, compact llm.CompactConfig, client llm.Client, onStart func(compactedMessages, retainedMessages int)) ([]store.Message, store.ProviderContext, bool, error) {
	compactor, ok := client.(llm.ContextCompactor)
	thresholdTokens := compactThresholdTokens(compact)
	if !ok || !automaticCompactAllowed(compact) || thresholdTokens <= 0 {
		return history, providerContext, false, nil
	}
	if estimateNativeContextTokens(systemPrompt, history, newMsg, providerContext, compact) <= thresholdTokens {
		return history, providerContext, false, nil
	}
	if len(history) <= nativeContextKeepRecentMessages {
		return history, providerContext, false, nil
	}

	cutoff := len(history) - nativeContextKeepRecentMessages
	if onStart != nil {
		onStart(cutoff, nativeContextKeepRecentMessages)
	}
	compactedContext, err := compactor.CompactContext(systemPrompt, history[:cutoff], providerContext, compact)
	if err != nil {
		if errors.Is(err, llm.ErrCompactionNotTriggered) {
			return history, providerContext, false, nil
		}
		return history, providerContext, false, err
	}
	if compactedContext.IsEmpty() {
		return history, providerContext, false, nil
	}
	return retainRecentMessages(history, nativeContextKeepRecentMessages), compactedContext, true, nil
}

func automaticCompactAllowed(compact llm.CompactConfig) bool {
	return compact.Mode != compactModeFalse
}

func compactThresholdTokens(compact llm.CompactConfig) int {
	if compact.ContextWindow <= 0 || compact.Threshold <= 0 {
		return 0
	}
	return int(math.Ceil(float64(compact.ContextWindow) * compact.Threshold))
}

func retainRecentMessages(messages []store.Message, keep int) []store.Message {
	if keep <= 0 || len(messages) <= keep {
		return append([]store.Message(nil), messages...)
	}
	return append([]store.Message(nil), messages[len(messages)-keep:]...)
}

func estimateNativeContextTokens(systemPrompt string, history []store.Message, newMsg store.Message, providerContext store.ProviderContext, compact llm.CompactConfig) int {
	runes := utf8.RuneCountInString(systemPrompt) + utf8.RuneCountInString(compact.Instructions)
	for _, item := range providerContext.Items {
		runes += len(item)
	}
	for _, msg := range history {
		runes += estimateMessageRunes(msg)
	}
	runes += estimateMessageRunes(newMsg)
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

func estimateMessageRunes(msg store.Message) int {
	runes := utf8.RuneCountInString(msg.Role) + utf8.RuneCountInString(msg.Content)
	for _, attachment := range msg.Attachments {
		runes += utf8.RuneCountInString(attachment.Type)
		runes += utf8.RuneCountInString(attachment.MIMEType)
		runes += utf8.RuneCountInString(attachment.Filename)
		runes += utf8.RuneCountInString(attachment.RefProvider)
		runes += utf8.RuneCountInString(attachment.RefType)
		runes += utf8.RuneCountInString(attachment.RefID)
		runes += utf8.RuneCountInString(attachment.LocalPath)
	}
	return runes
}
