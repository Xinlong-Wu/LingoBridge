package monitor

import (
	"context"
	"encoding/json"
	"fmt"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type textSender interface {
	SendText(ctx context.Context, chatID, text string) error
}

type sdkSender struct {
	client *lark.Client
}

func (s *sdkSender) SendText(ctx context.Context, chatID, text string) error {
	body, err := json.Marshal(textContent{Text: text})
	if err != nil {
		return fmt.Errorf("marshal feishu text content: %w", err)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(body)).
			Build()).
		Build()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send feishu message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("send feishu message code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
