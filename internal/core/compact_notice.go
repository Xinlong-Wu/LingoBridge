package core

import (
	"context"
	"fmt"
)

const compactStartText = "正在压缩当前会话上下文..."

// CompactNotice describes a provider-native compaction progress event.
type CompactNotice struct {
	ModelName         string
	Manual            bool
	CompactedMessages int
	RetainedMessages  int
}

// CompactNoticeHandle carries platform-private state from start to finish.
type CompactNoticeHandle struct {
	MessageID string
}

// CompactNoticeSender is an optional sender extension for compaction progress.
type CompactNoticeSender interface {
	StartCompactNotice(ctx context.Context, notice CompactNotice) (CompactNoticeHandle, error)
	FinishCompactNotice(ctx context.Context, handle CompactNoticeHandle, notice CompactNotice) error
}

// CompactStartText returns the default user-facing compaction start notice.
func CompactStartText() string {
	return compactStartText
}

// CompactSuccessText returns the default user-facing compaction success notice.
func CompactSuccessText(notice CompactNotice) string {
	return fmt.Sprintf("✅ 已压缩当前会话上下文：模型 %s，压缩 %d 条历史，保留 %d 条最近消息。", notice.ModelName, notice.CompactedMessages, notice.RetainedMessages)
}

func startCompactNotice(ctx context.Context, sender Sender, notice CompactNotice) CompactNoticeHandle {
	notifier, ok := sender.(CompactNoticeSender)
	if !ok {
		return CompactNoticeHandle{}
	}
	handle, err := notifier.StartCompactNotice(ctx, notice)
	if err != nil {
		coreLog.Warn(ctx, "start compact notice failed model=%s manual=%v: %v", notice.ModelName, notice.Manual, err)
		return CompactNoticeHandle{}
	}
	return handle
}

func finishCompactNotice(ctx context.Context, sender Sender, handle CompactNoticeHandle, notice CompactNotice) error {
	notifier, ok := sender.(CompactNoticeSender)
	if !ok {
		return sender.Send(ctx, OutboundMessage{Text: CompactSuccessText(notice)})
	}
	if err := notifier.FinishCompactNotice(ctx, handle, notice); err != nil {
		coreLog.Warn(ctx, "finish compact notice failed model=%s manual=%v: %v", notice.ModelName, notice.Manual, err)
	}
	return nil
}
