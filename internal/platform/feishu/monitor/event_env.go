package monitor

import (
	"context"
	"encoding/json"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"lingobridge/internal/commands"
	"lingobridge/internal/store"
)

type feishuEventEnvelope struct {
	Event map[string]interface{} `json:"event"`
}

func baseFeishuEventEnv(eventName string) map[string]string {
	return map[string]string{
		"LINGOBRIDGE_PLATFORM":     store.PlatformFeishu,
		"LINGOBRIDGE_EVENT_NAME":   eventName,
		"LINGOBRIDGE_COMMAND_HELP": commands.HelpText(commands.DefaultPolicy()),
	}
}

func customizedFeishuEventEnv(eventName string, req *larkevent.EventReq) (map[string]string, string) {
	env := baseFeishuEventEnv(eventName)
	if req == nil || len(req.Body) == 0 {
		return env, ""
	}
	var envelope feishuEventEnvelope
	if err := json.Unmarshal(req.Body, &envelope); err != nil {
		feishuLog.Warn(context.Background(), "parse feishu event %s payload: %v", eventName, err)
		env["LINGOBRIDGE_EVENT_JSON"] = string(req.Body)
		return env, ""
	}
	if len(envelope.Event) == 0 {
		env["LINGOBRIDGE_EVENT_JSON"] = string(req.Body)
		return env, ""
	}
	if data, err := json.Marshal(envelope.Event); err == nil {
		env["LINGOBRIDGE_EVENT_JSON"] = string(data)
	}
	applyFeishuEventFields(env, envelope.Event)
	return env, stringField(envelope.Event, "chat_id")
}

func botP2PChatEnteredEnv(event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) (map[string]string, string) {
	env := baseFeishuEventEnv(feishuBotP2PChatEnteredV2Event)
	if event == nil || event.Event == nil {
		return env, ""
	}
	if data, err := json.Marshal(event.Event); err == nil {
		env["LINGOBRIDGE_EVENT_JSON"] = string(data)
	}
	chatID := deref(event.Event.ChatId)
	env["LINGOBRIDGE_FEISHU_CHAT_ID"] = chatID
	if event.Event.OperatorId != nil {
		env["LINGOBRIDGE_FEISHU_OPERATOR_OPEN_ID"] = deref(event.Event.OperatorId.OpenId)
		env["LINGOBRIDGE_FEISHU_OPERATOR_USER_ID"] = deref(event.Event.OperatorId.UserId)
	}
	return env, chatID
}

func applyFeishuEventFields(env map[string]string, event map[string]interface{}) {
	setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_APP_ID", stringField(event, "app_id"))
	setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_CHAT_ID", stringField(event, "chat_id"))
	setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_TENANT_KEY", stringField(event, "tenant_key"))
	setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_TYPE", stringField(event, "type"))
	if operator, ok := mapField(event, "operator"); ok {
		setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_OPERATOR_OPEN_ID", stringField(operator, "open_id"))
		setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_OPERATOR_USER_ID", stringField(operator, "user_id"))
	}
	if user, ok := mapField(event, "user"); ok {
		setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_USER_OPEN_ID", stringField(user, "open_id"))
		setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_USER_USER_ID", stringField(user, "user_id"))
		setEnvIfNotEmpty(env, "LINGOBRIDGE_FEISHU_USER_NAME", stringField(user, "name"))
	}
}

func mapField(values map[string]interface{}, name string) (map[string]interface{}, bool) {
	value, ok := values[name]
	if !ok {
		return nil, false
	}
	out, ok := value.(map[string]interface{})
	return out, ok
}

func stringField(values map[string]interface{}, name string) string {
	value, ok := values[name]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func setEnvIfNotEmpty(env map[string]string, name, value string) {
	if value != "" {
		env[name] = value
	}
}
