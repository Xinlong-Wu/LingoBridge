package monitor

import (
	"context"
	"encoding/json"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type textContent struct {
	Text string `json:"text"`
}

type incomingMessage struct {
	UserID      string
	ChatID      string
	MessageID   string
	Text        string
	Unsupported bool
}

func normalizeEvent(ctx context.Context, event *larkim.P2MessageReceiveV1) (incomingMessage, bool) {
	if event == nil || event.Event == nil || event.Event.Sender == nil || event.Event.Message == nil {
		return incomingMessage{}, false
	}
	msg := event.Event.Message
	chatID := deref(msg.ChatId)
	messageID := deref(msg.MessageId)
	if chatID == "" {
		return incomingMessage{}, false
	}
	openID := userOpenID(event.Event.Sender.SenderId)
	if openID == "" {
		openID = userID(event.Event.Sender.SenderId)
	}
	if openID == "" {
		return incomingMessage{}, false
	}

	chatType := deref(msg.ChatType)
	if chatType != "p2p" && !mentionsBot(msg.Mentions) {
		return incomingMessage{}, false
	}

	userKey := "feishu:" + openID
	if chatType != "p2p" {
		userKey = "feishu:" + chatID + ":" + openID
	}

	if deref(msg.MessageType) != "text" {
		return incomingMessage{UserID: userKey, ChatID: chatID, MessageID: messageID, Unsupported: true}, true
	}

	text, err := extractText(deref(msg.Content), msg.Mentions)
	if err != nil {
		feishuLog.Warn(ctx, "parse text message: %v", err)
		return incomingMessage{UserID: userKey, ChatID: chatID, MessageID: messageID, Unsupported: true}, true
	}
	return incomingMessage{UserID: userKey, ChatID: chatID, MessageID: messageID, Text: text}, true
}

func mentionsBot(mentions []*larkim.MentionEvent) bool {
	for _, mention := range mentions {
		if deref(mention.MentionedType) == "app" {
			return true
		}
	}
	return false
}

func extractText(raw string, mentions []*larkim.MentionEvent) (string, error) {
	var content textContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return "", err
	}
	text := content.Text
	for _, mention := range mentions {
		if key := deref(mention.Key); key != "" {
			text = strings.ReplaceAll(text, key, "")
		}
	}
	return strings.TrimSpace(text), nil
}

func userOpenID(id *larkim.UserId) string {
	if id == nil {
		return ""
	}
	return deref(id.OpenId)
}

func userID(id *larkim.UserId) string {
	if id == nil {
		return ""
	}
	return deref(id.UserId)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
