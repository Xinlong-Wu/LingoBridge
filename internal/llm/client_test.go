package llm

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSSE(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		parser streamParser
		want   string
	}{
		{
			name: "openai chat stream",
			input: strings.Join([]string{
				"data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}",
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}",
				"data: [DONE]",
			}, "\n"),
			parser: parseOpenAIStreamEvent,
			want:   "hello",
		},
		{
			name: "responses stream",
			input: strings.Join([]string{
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}",
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\" there\"}",
				"data: [DONE]",
			}, "\n"),
			parser: parseResponsesStreamEvent,
			want:   "hi there",
		},
		{
			name: "anthropic stream",
			input: strings.Join([]string{
				"data: {\"type\":\"content_block_start\",\"content_block\":{\"text\":\"he\"}}",
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"llo\"}}",
			}, "\n"),
			parser: parseAnthropicStreamEvent,
			want:   "hello",
		},
		{
			name: "malformed event ignored",
			input: strings.Join([]string{
				"data: {not-json",
				"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}",
			}, "\n"),
			parser: parseOpenAIStreamEvent,
			want:   "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSSE(strings.NewReader(tt.input), tt.parser, nil)
			if err != nil {
				t.Fatalf("parseSSE returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseSSE = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseSSECallbackError(t *testing.T) {
	errStop := errors.New("stop")
	input := strings.Join([]string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}",
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}",
	}, "\n")

	got, err := parseSSE(strings.NewReader(input), parseOpenAIStreamEvent, func(chunk string) error {
		if chunk == "two" {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("parseSSE error = %v, want %v", err, errStop)
	}
	if got != "onetwo" {
		t.Fatalf("parseSSE partial text = %q, want %q", got, "onetwo")
	}
}
