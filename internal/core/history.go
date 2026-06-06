package core

import "lingobridge/internal/store"

// ToLLMMessages converts a session conversation and a new user message into
// the messages array format for the LLM API. It includes the system prompt
// and truncates history to maxHistory messages.
func ToLLMMessages(systemPrompt string, conv *store.Conversation, newMsg string, maxHistory int) []store.Message {
	return ToLLMMessagesWithUserMessage(systemPrompt, conv, store.Message{Role: "user", Content: newMsg}, maxHistory)
}

// ToLLMMessagesWithUserMessage converts a session conversation and a new user
// message into the messages array format for the LLM API. It includes the
// system prompt and truncates history to maxHistory messages.
func ToLLMMessagesWithUserMessage(systemPrompt string, conv *store.Conversation, newMsg store.Message, maxHistory int) []store.Message {
	var msgs []store.Message

	if systemPrompt != "" {
		msgs = append(msgs, store.Message{Role: "system", Content: systemPrompt})
	}

	history := conv.Messages
	startIdx := 0
	if maxHistory > 0 && len(history) > maxHistory {
		startIdx = len(history) - maxHistory
	}
	msgs = append(msgs, history[startIdx:]...)

	if newMsg.Role == "" {
		newMsg.Role = "user"
	}
	msgs = append(msgs, newMsg)
	return msgs
}
