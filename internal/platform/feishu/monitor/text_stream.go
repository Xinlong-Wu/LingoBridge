package monitor

import (
	"context"
	"errors"
	"time"
)

const (
	feishuMaxMessageEdits       = 20
	feishuMaxStreamPreviewEdits = 18
)

type feishuTextStream struct {
	sender       textSender
	chatID       string
	messageID    string
	lastUpdateAt time.Time
	lastSentText string
	editCount    int
	editLimited  bool
	now          func() time.Time
}

func (s *feishuTextStream) Update(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	if s.messageID == "" {
		return s.create(ctx, text)
	}
	if s.nowTime().Sub(s.lastUpdateAt) < feishuStreamPreviewInterval(s.editCount) {
		return nil
	}
	if s.editLimited || s.editCount >= feishuMaxStreamPreviewEdits {
		return nil
	}
	if err := s.update(ctx, text); err != nil {
		if errors.Is(err, ErrFeishuMessageEditLimit) {
			s.editLimited = true
			return nil
		}
		return err
	}
	return nil
}

func (s *feishuTextStream) Finish(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	if s.messageID == "" {
		return s.create(ctx, text)
	}
	if text == s.lastSentText {
		return nil
	}
	if s.editLimited || s.editCount >= feishuMaxMessageEdits {
		return s.sender.SendText(ctx, s.chatID, text)
	}
	if err := s.update(ctx, text); err != nil {
		if errors.Is(err, ErrFeishuMessageEditLimit) {
			s.editLimited = true
			return s.sender.SendText(ctx, s.chatID, text)
		}
		return err
	}
	return nil
}

func (s *feishuTextStream) create(ctx context.Context, text string) error {
	messageID, err := s.sender.CreateText(ctx, s.chatID, text)
	if err != nil {
		return err
	}
	s.messageID = messageID
	s.lastSentText = text
	s.lastUpdateAt = s.nowTime()
	return nil
}

func (s *feishuTextStream) update(ctx context.Context, text string) error {
	if err := s.sender.UpdateText(ctx, s.messageID, text); err != nil {
		return err
	}
	s.lastSentText = text
	s.lastUpdateAt = s.nowTime()
	s.editCount++
	return nil
}

func (s *feishuTextStream) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func feishuStreamPreviewInterval(editCount int) time.Duration {
	switch {
	case editCount < 3:
		return 300 * time.Millisecond
	case editCount < 8:
		return 800 * time.Millisecond
	case editCount < 14:
		return 1500 * time.Millisecond
	default:
		return 2500 * time.Millisecond
	}
}
