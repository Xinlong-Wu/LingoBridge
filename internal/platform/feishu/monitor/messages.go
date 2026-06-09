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

type postContent struct {
	Title   string          `json:"title"`
	Content [][]postElement `json:"content"`
}

type postElement struct {
	Tag           string          `json:"tag"`
	Text          string          `json:"text"`
	Href          string          `json:"href"`
	Style         []string        `json:"style"`
	UserID        string          `json:"user_id"`
	OpenID        string          `json:"open_id"`
	UserName      string          `json:"user_name"`
	Name          string          `json:"name"`
	Key           string          `json:"key"`
	MentionedType string          `json:"mentioned_type"`
	FileName      string          `json:"file_name"`
	EmojiType     string          `json:"emoji_type"`
	Language      string          `json:"language"`
	Content       json.RawMessage `json:"content"`
}

type incomingMessage struct {
	UserID           string
	ChatID           string
	MessageID        string
	ReplyToMessageID string
	Text             string
	Unsupported      bool
}

func normalizeEvent(ctx context.Context, event *larkim.P2MessageReceiveV1, botOpenID string) (incomingMessage, bool) {
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

	userKey := "feishu:" + openID
	replyToMessageID := ""
	if chatType != "p2p" {
		userKey = "feishu:group:" + chatID
		replyToMessageID = messageID
	}

	var text string
	var err error
	switch deref(msg.MessageType) {
	case "text":
		text, err = extractText(deref(msg.Content), msg.Mentions, botOpenID)
	case "post":
		text, err = extractPostMarkdown(deref(msg.Content), msg.Mentions, botOpenID)
	default:
		return incomingMessage{UserID: userKey, ChatID: chatID, MessageID: messageID, ReplyToMessageID: replyToMessageID, Unsupported: true}, true
	}
	if err != nil {
		feishuLog.Warn(ctx, "parse %s message: %v", deref(msg.MessageType), err)
		return incomingMessage{UserID: userKey, ChatID: chatID, MessageID: messageID, ReplyToMessageID: replyToMessageID, Unsupported: true}, true
	}
	return incomingMessage{UserID: userKey, ChatID: chatID, MessageID: messageID, ReplyToMessageID: replyToMessageID, Text: text}, true
}

func extractText(raw string, mentions []*larkim.MentionEvent, botOpenID string) (string, error) {
	var content textContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return "", err
	}
	return strings.TrimSpace(stripBotMentionKeys(content.Text, botMentionKeys(mentions, botOpenID))), nil
}

func extractPostMarkdown(raw string, mentions []*larkim.MentionEvent, botOpenID string) (string, error) {
	var content postContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return "", err
	}

	botKeys := botMentionKeys(mentions, botOpenID)
	lines := []string{}
	if title := strings.TrimSpace(content.Title); title != "" {
		lines = append(lines, "# "+title, "")
	}
	for _, row := range content.Content {
		var line strings.Builder
		for _, element := range row {
			line.WriteString(renderPostElement(element, botKeys, botOpenID))
		}
		lines = append(lines, line.String())
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}

func botMentionKeys(mentions []*larkim.MentionEvent, botOpenID string) map[string]bool {
	keys := map[string]bool{}
	botOpenID = strings.TrimSpace(botOpenID)
	if botOpenID == "" {
		return keys
	}
	for _, mention := range mentions {
		if mentionBotOpenID(mention) == botOpenID {
			if key := deref(mention.Key); key != "" {
				keys[key] = true
			}
		}
	}
	return keys
}

func mentionBotOpenID(mention *larkim.MentionEvent) string {
	if mention == nil {
		return ""
	}
	return userOpenID(mention.Id)
}

func stripBotMentionKeys(text string, botKeys map[string]bool) string {
	for key := range botKeys {
		text = strings.ReplaceAll(text, key, "")
	}
	return text
}

func renderPostElement(element postElement, botKeys map[string]bool, botOpenID string) string {
	switch tag := strings.TrimSpace(element.Tag); tag {
	case "", "text":
		return applyPostStyles(stripBotMentionKeys(element.Text, botKeys), element.Style)
	case "a":
		text := strings.TrimSpace(stripBotMentionKeys(element.Text, botKeys))
		href := strings.TrimSpace(element.Href)
		if text == "" {
			text = href
		}
		if href == "" {
			return applyPostStyles(text, element.Style)
		}
		return applyPostStyles("["+text+"]("+href+")", element.Style)
	case "at":
		if isBotPostAt(element, botKeys, botOpenID) {
			return ""
		}
		name := firstNonEmpty(element.Name, element.UserName, element.Text, element.Key, element.UserID, element.OpenID)
		if name == "" {
			return ""
		}
		if !strings.HasPrefix(name, "@") {
			name = "@" + name
		}
		return name
	case "img", "image":
		return "[图片]"
	case "media":
		return "[视频]"
	case "file":
		return "[文件]"
	case "emotion":
		if element.EmojiType != "" {
			return "[表情:" + element.EmojiType + "]"
		}
		return "[表情]"
	case "hr":
		return "---"
	case "code_block":
		return renderPostCodeBlock(element)
	case "md":
		return stripBotMentionKeys(element.Text, botKeys)
	default:
		return "[富文本元素:" + tag + "]"
	}
}

func isBotPostAt(element postElement, botKeys map[string]bool, botOpenID string) bool {
	if strings.TrimSpace(botOpenID) != "" && strings.TrimSpace(element.OpenID) == strings.TrimSpace(botOpenID) {
		return true
	}
	return botKeys[element.Key] || botKeys[element.Text]
}

func renderPostCodeBlock(element postElement) string {
	text := rawString(element.Content)
	if text == "" {
		text = element.Text
	}
	language := strings.TrimSpace(element.Language)
	return "```" + language + "\n" + text + "\n```"
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return text
}

func applyPostStyles(text string, styles []string) string {
	if text == "" {
		return ""
	}
	styleSet := map[string]bool{}
	for _, style := range styles {
		styleSet[style] = true
	}
	if styleSet["underline"] {
		text = "<u>" + text + "</u>"
	}
	if styleSet["lineThrough"] || styleSet["line_through"] {
		text = "~~" + text + "~~"
	}
	if styleSet["italic"] {
		text = "*" + text + "*"
	}
	if styleSet["bold"] {
		text = "**" + text + "**"
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
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
