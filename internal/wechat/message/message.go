package message

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"wechatbox/internal/store"
	"wechatbox/internal/wechat/api"
)

func generateClientID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "wechatbox-" + hex.EncodeToString(b)
}

// ExtractText extracts the text content from a WeChat message.
func ExtractText(msg *api.WeixinMessage) string {
	if msg == nil || msg.ItemList == nil {
		return ""
	}
	var texts []string
	for _, item := range msg.ItemList {
		if item.Type == api.ItemTypeText && item.TextItem != nil {
			texts = append(texts, item.TextItem.Text)
		}
	}
	return strings.Join(texts, "")
}

// HasMedia checks if the message contains any media items.
func HasMedia(msg *api.WeixinMessage) bool {
	if msg == nil || msg.ItemList == nil {
		return false
	}
	for _, item := range msg.ItemList {
		switch item.Type {
		case api.ItemTypeImage, api.ItemTypeVoice, api.ItemTypeVideo, api.ItemTypeFile:
			return true
		}
	}
	return false
}

// ToLLMMessages converts a session conversation and a new user message into
// the messages array format for the LLM API. It includes the system prompt
// and truncates history to maxHistory messages.
func ToLLMMessages(systemPrompt string, conv *store.Conversation, newMsg string, maxHistory int) []store.Message {
	var msgs []store.Message

	// Add system prompt
	if systemPrompt != "" {
		msgs = append(msgs, store.Message{Role: "system", Content: systemPrompt})
	}

	// Add conversation history (already includes previous turns)
	history := conv.Messages
	startIdx := 0
	if maxHistory > 0 && len(history) > maxHistory {
		startIdx = len(history) - maxHistory
	}

	msgs = append(msgs, history[startIdx:]...)

	// Add the new user message
	msgs = append(msgs, store.Message{Role: "user", Content: newMsg})

	return msgs
}

// BuildTextMessage creates a WeixinMessage for sending text.
func BuildTextMessage(toUserID, text, contextToken string) *api.WeixinMessage {
	return &api.WeixinMessage{
		ToUserID:     toUserID,
		ClientID:     generateClientID(),
		MessageType:  api.MessageTypeBot,
		MessageState: api.MessageStateFinish,
		ItemList: []*api.MessageItem{
			{
				Type:        api.ItemTypeText,
				IsCompleted: true,
				TextItem:    &api.TextItem{Text: text},
			},
		},
		ContextToken: contextToken,
	}
}

// BuildImageMessage creates a WeixinMessage for sending an image.
func BuildImageMessage(toUserID string, media *api.CDNMedia, contextToken string) *api.WeixinMessage {
	return &api.WeixinMessage{
		ToUserID:     toUserID,
		MessageType:  api.MessageTypeBot,
		MessageState: api.MessageStateFinish,
		ItemList: []*api.MessageItem{
			{
				Type:        api.ItemTypeImage,
				IsCompleted: true,
				ImageItem:   &api.ImageItem{Media: media},
			},
		},
		ContextToken: contextToken,
	}
}
