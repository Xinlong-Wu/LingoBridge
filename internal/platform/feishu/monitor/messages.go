package monitor

import (
	"context"
	"encoding/json"
	"html"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
	SenderOpenID     string
	SenderUserID     string
	ChatID           string
	MessageID        string
	ReplyToMessageID string
	Text             string
	Mentions         []feishuMention
	Unsupported      bool
}

type feishuMention struct {
	Key    string
	Name   string
	OpenID string
	UserID string
}

func (m feishuMention) targetID() string {
	if m.OpenID != "" {
		return m.OpenID
	}
	return m.UserID
}

type mentionCatalog struct {
	botKeys             map[string]bool
	mentionReplacements map[string]string
	mentionsByKey       map[string]feishuMention
	mentions            []feishuMention
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
	senderOpenID := userOpenID(event.Event.Sender.SenderId)
	senderUserID := userID(event.Event.Sender.SenderId)
	senderID := senderOpenID
	if senderID == "" {
		senderID = senderUserID
	}
	if senderID == "" {
		return incomingMessage{}, false
	}

	chatType := deref(msg.ChatType)

	userKey := "feishu:" + senderID
	replyToMessageID := ""
	if chatType != "p2p" {
		userKey = "feishu:group:" + chatID
		replyToMessageID = messageID
	}

	mentions := newMentionCatalog(msg.Mentions, botOpenID)
	var text string
	var err error
	switch deref(msg.MessageType) {
	case "text":
		text, err = extractText(deref(msg.Content), mentions)
	case "post":
		text, err = extractPostMarkdown(deref(msg.Content), mentions, botOpenID)
	default:
		return incomingMessage{UserID: userKey, SenderOpenID: senderOpenID, SenderUserID: senderUserID, ChatID: chatID, MessageID: messageID, ReplyToMessageID: replyToMessageID, Mentions: mentions.list(), Unsupported: true}, true
	}
	if err != nil {
		feishuLog.Warn(ctx, "parse %s message: %v", deref(msg.MessageType), err)
		return incomingMessage{UserID: userKey, SenderOpenID: senderOpenID, SenderUserID: senderUserID, ChatID: chatID, MessageID: messageID, ReplyToMessageID: replyToMessageID, Mentions: mentions.list(), Unsupported: true}, true
	}
	return incomingMessage{UserID: userKey, SenderOpenID: senderOpenID, SenderUserID: senderUserID, ChatID: chatID, MessageID: messageID, ReplyToMessageID: replyToMessageID, Text: text, Mentions: mentions.list()}, true
}

func extractText(raw string, mentions *mentionCatalog) (string, error) {
	var content textContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return "", err
	}
	return strings.TrimSpace(mentions.replaceKeys(content.Text)), nil
}

func extractPostMarkdown(raw string, mentions *mentionCatalog, botOpenID string) (string, error) {
	var content postContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return "", err
	}

	lines := []string{}
	if title := strings.TrimSpace(mentions.replaceKeys(content.Title)); title != "" {
		lines = append(lines, "# "+title, "")
	}
	for _, row := range content.Content {
		var line strings.Builder
		for _, element := range row {
			line.WriteString(renderPostElement(element, mentions, botOpenID))
		}
		lines = append(lines, line.String())
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}

func newMentionCatalog(mentions []*larkim.MentionEvent, botOpenID string) *mentionCatalog {
	c := &mentionCatalog{
		botKeys:             map[string]bool{},
		mentionReplacements: map[string]string{},
		mentionsByKey:       map[string]feishuMention{},
	}
	botOpenID = strings.TrimSpace(botOpenID)
	for _, mention := range mentions {
		if mention == nil {
			continue
		}
		if botOpenID != "" && mentionBotOpenID(mention) == botOpenID {
			c.addBotKey(deref(mention.Key))
			continue
		}
		c.addMention(feishuMention{
			Key:    deref(mention.Key),
			Name:   mentionName(deref(mention.Name)),
			OpenID: userOpenID(mention.Id),
			UserID: userID(mention.Id),
		})
	}
	return c
}

func mentionBotOpenID(mention *larkim.MentionEvent) string {
	if mention == nil {
		return ""
	}
	return userOpenID(mention.Id)
}

func (c *mentionCatalog) addBotKey(key string) {
	key = strings.TrimSpace(key)
	if key != "" {
		if c.botKeys == nil {
			c.botKeys = map[string]bool{}
		}
		c.botKeys[key] = true
	}
}

func (c *mentionCatalog) addMention(mention feishuMention) {
	mention.Key = strings.TrimSpace(mention.Key)
	mention.Name = mentionName(mention.Name)
	mention.OpenID = strings.TrimSpace(mention.OpenID)
	mention.UserID = strings.TrimSpace(mention.UserID)
	if mention.Key == "" && mention.Name == "" {
		return
	}
	if mention.Key != "" {
		if c.mentionsByKey == nil {
			c.mentionsByKey = map[string]feishuMention{}
		}
		c.mentionsByKey[mention.Key] = mention
		if mention.Name != "" {
			if c.mentionReplacements == nil {
				c.mentionReplacements = map[string]string{}
			}
			c.mentionReplacements[mention.Key] = "@" + mention.Name
		}
	}
	duplicateIndexes := c.duplicateMentionIndexes(mention)
	if len(duplicateIndexes) == 0 {
		c.mentions = append(c.mentions, mention)
		return
	}
	primary := duplicateIndexes[0]
	merged := mergeFeishuMention(c.mentions[primary], mention)
	for _, idx := range duplicateIndexes[1:] {
		merged = mergeFeishuMention(merged, c.mentions[idx])
	}
	c.mentions[primary] = merged
	for i := len(duplicateIndexes) - 1; i >= 1; i-- {
		idx := duplicateIndexes[i]
		c.mentions = append(c.mentions[:idx], c.mentions[idx+1:]...)
	}
}

func (c *mentionCatalog) duplicateMentionIndexes(mention feishuMention) []int {
	if mention.OpenID == "" && mention.UserID == "" {
		return nil
	}
	indexes := []int{}
	for i, existing := range c.mentions {
		if sameMentionKey(existing, mention) || sameMentionIdentity(existing, mention) {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func sameMentionKey(a, b feishuMention) bool {
	return a.Key != "" && a.Key == b.Key
}

func sameMentionIdentity(a, b feishuMention) bool {
	return (a.OpenID != "" && a.OpenID == b.OpenID) || (a.UserID != "" && a.UserID == b.UserID)
}

func mergeFeishuMention(existing, incoming feishuMention) feishuMention {
	if existing.Key == "" {
		existing.Key = incoming.Key
	}
	if existing.Name == "" {
		existing.Name = incoming.Name
	}
	if existing.OpenID == "" {
		existing.OpenID = incoming.OpenID
	}
	if existing.UserID == "" {
		existing.UserID = incoming.UserID
	}
	return existing
}

func (c *mentionCatalog) list() []feishuMention {
	if c == nil || len(c.mentions) == 0 {
		return nil
	}
	mentions := make([]feishuMention, len(c.mentions))
	copy(mentions, c.mentions)
	return mentions
}

func (c *mentionCatalog) replaceKeys(text string) string {
	if c == nil {
		return text
	}
	replacements := map[string]string{}
	for key := range c.botKeys {
		replacements[key] = ""
	}
	for key, replacement := range c.mentionReplacements {
		replacements[key] = replacement
	}
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})
	for _, key := range keys {
		text = strings.ReplaceAll(text, key, replacements[key])
	}
	return text
}

func (c *mentionCatalog) mentionByKey(key string) (feishuMention, bool) {
	if c == nil || key == "" {
		return feishuMention{}, false
	}
	key = strings.TrimSpace(key)
	if mention, ok := c.mentionsByKey[key]; ok {
		return mention, true
	}
	return feishuMention{}, false
}

func renderPostElement(element postElement, mentions *mentionCatalog, botOpenID string) string {
	switch tag := strings.TrimSpace(element.Tag); tag {
	case "", "text":
		return applyPostStyles(mentions.replaceKeys(element.Text), element.Style)
	case "a":
		text := strings.TrimSpace(mentions.replaceKeys(element.Text))
		href := strings.TrimSpace(element.Href)
		if text == "" {
			text = href
		}
		if href == "" {
			return applyPostStyles(text, element.Style)
		}
		return applyPostStyles("["+text+"]("+href+")", element.Style)
	case "at":
		if isBotPostAt(element, mentions, botOpenID) {
			return ""
		}
		mention := postElementMention(element, mentions)
		mentions.addMention(mention)
		name := mention.Name
		if name == "" {
			return ""
		}
		return "@" + name
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
		return mentions.replaceKeys(element.Text)
	default:
		return "[富文本元素:" + tag + "]"
	}
}

func isBotPostAt(element postElement, mentions *mentionCatalog, botOpenID string) bool {
	if strings.TrimSpace(botOpenID) != "" && strings.TrimSpace(element.OpenID) == strings.TrimSpace(botOpenID) {
		return true
	}
	if mentions == nil {
		return false
	}
	return mentions.botKeys[element.Key] || mentions.botKeys[element.Text]
}

func postElementMention(element postElement, mentions *mentionCatalog) feishuMention {
	elementMention := feishuMention{
		Key:    firstNonEmpty(element.Key, element.Text),
		Name:   mentionName(firstNonEmpty(element.Name, element.UserName, element.Text, element.Key, element.UserID, element.OpenID)),
		OpenID: element.OpenID,
		UserID: element.UserID,
	}
	if mention, ok := mentions.mentionByKey(element.Key); ok {
		return mergeFeishuMention(mention, elementMention)
	}
	if mention, ok := mentions.mentionByKey(element.Text); ok {
		return mergeFeishuMention(mention, elementMention)
	}
	return elementMention
}

func mentionName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "@")
	return strings.TrimSpace(name)
}

func renderFeishuMentions(text string, mentions []feishuMention) string {
	unique := uniqueMentionTargets(mentions)
	if len(unique) == 0 {
		return text
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) > len(names[j])
	})
	for _, name := range names {
		text = replaceMentionName(text, name, unique[name])
	}
	return text
}

func uniqueMentionTargets(mentions []feishuMention) map[string]feishuMention {
	byName := map[string]map[string]feishuMention{}
	missingTarget := map[string]bool{}
	for _, mention := range mentions {
		name := mentionName(mention.Name)
		if name == "" {
			continue
		}
		targetID := mention.targetID()
		if byName[name] == nil {
			byName[name] = map[string]feishuMention{}
		}
		if targetID == "" {
			missingTarget[name] = true
			continue
		}
		byName[name][targetID] = mention
	}
	unique := map[string]feishuMention{}
	for name, targets := range byName {
		if missingTarget[name] || len(targets) != 1 {
			continue
		}
		for _, mention := range targets {
			unique[name] = mention
		}
	}
	return unique
}

func replaceMentionName(text, name string, mention feishuMention) string {
	token := "@" + name
	var out strings.Builder
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], token) && mentionStartBoundary(text, i) && mentionEndBoundary(text, i+len(token)) {
			out.WriteString(feishuAtTag(mention))
			i += len(token)
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		out.WriteRune(r)
		i += size
	}
	return out.String()
}

func mentionStartBoundary(text string, pos int) bool {
	if pos <= 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:pos])
	return mentionBoundaryRune(r)
}

func mentionEndBoundary(text string, pos int) bool {
	if pos >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[pos:])
	return mentionBoundaryRune(r)
}

func mentionBoundaryRune(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

func feishuAtTag(mention feishuMention) string {
	targetID := html.EscapeString(mention.targetID())
	name := html.EscapeString(mentionName(mention.Name))
	return `<at user_id="` + targetID + `">` + name + `</at>`
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
