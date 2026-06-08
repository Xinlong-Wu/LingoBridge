package core

import (
	"context"
	"strings"
)

type replyTextStream struct {
	sender   TextStreamSender
	segments []*replyTextSegment
	limit    int
}

type replyTextSegment struct {
	stream   TextStream
	text     strings.Builder
	finished bool
}

func newReplyTextStream(sender TextStreamSender, limit int) *replyTextStream {
	return &replyTextStream{
		sender: sender,
		limit:  limit,
	}
}

func (s *replyTextStream) OnChunk(ctx context.Context, chunk string) error {
	if s == nil {
		return nil
	}
	if chunk == "" {
		return nil
	}
	for chunk != "" {
		segment, err := s.currentSegment(ctx)
		if err != nil {
			return err
		}
		if s.limit <= 0 {
			segment.text.WriteString(chunk)
			return segment.stream.Update(ctx, segment.text.String())
		}

		combined := segment.text.String() + chunk
		chunks := SplitTextChunks(combined, s.limit)
		if len(chunks) == 0 || chunks[0] == "" {
			return nil
		}
		segment.text.Reset()
		segment.text.WriteString(chunks[0])
		if err := segment.stream.Update(ctx, chunks[0]); err != nil {
			return err
		}
		if len(chunks) == 1 {
			return nil
		}
		if err := s.finishSegment(ctx, segment, chunks[0]); err != nil {
			return err
		}
		chunk = strings.Join(chunks[1:], "")
	}
	return nil
}

func (s *replyTextStream) Finish(ctx context.Context, text string) error {
	if s == nil || text == "" {
		return nil
	}
	chunks := SplitTextChunks(text, s.limit)
	if len(chunks) == 0 || chunks[0] == "" {
		return nil
	}
	for i, segment := range s.segments {
		if i >= len(chunks) {
			break
		}
		if err := s.finishSegment(ctx, segment, chunks[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *replyTextStream) FinishedChunks(total int) int {
	if s == nil {
		return 0
	}
	if len(s.segments) < total {
		return len(s.segments)
	}
	return total
}

func (s *replyTextStream) Started() bool {
	return s != nil && len(s.segments) > 0
}

func (s *replyTextStream) currentSegment(ctx context.Context) (*replyTextSegment, error) {
	if len(s.segments) > 0 {
		last := s.segments[len(s.segments)-1]
		if !last.finished {
			return last, nil
		}
	}
	stream, err := s.sender.StartTextStream(ctx)
	if err != nil {
		return nil, err
	}
	segment := &replyTextSegment{stream: stream}
	s.segments = append(s.segments, segment)
	return segment, nil
}

func (s *replyTextStream) finishSegment(ctx context.Context, segment *replyTextSegment, text string) error {
	if segment.finished && segment.text.String() == text {
		return nil
	}
	if err := segment.stream.Finish(ctx, text); err != nil {
		return err
	}
	segment.text.Reset()
	segment.text.WriteString(text)
	segment.finished = true
	return nil
}
