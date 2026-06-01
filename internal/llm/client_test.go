package llm

import (
	"encoding/base64"
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
		{
			name:   "last line without newline",
			input:  "data: {\"choices\":[{\"delta\":{\"content\":\"tail\"}}]}",
			parser: parseOpenAIStreamEvent,
			want:   "tail",
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

func TestParseSSELongLine(t *testing.T) {
	input := "data: {\"type\":\"keepalive\",\"blob\":\"" + strings.Repeat("x", 2*1024*1024) + "\"}\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}"

	got, err := parseSSE(strings.NewReader(input), parseResponsesStreamEvent, nil)
	if err != nil {
		t.Fatalf("parseSSE returned error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("parseSSE = %q, want ok", got)
	}
}

func TestParseResponsesOutputWithImage(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	resp, err := parseResponsesOutput(responsesOutput{Output: []responsesOutputItem{
		{
			Type: "message",
			Content: []responsesOutputItemPart{
				{Type: "output_text", Text: "done"},
			},
		},
		{
			ID:     "ig_1",
			Type:   "image_generation_call",
			Result: imageB64,
		},
	}})
	if err != nil {
		t.Fatalf("parseResponsesOutput returned error: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("response text = %q, want done", resp.Text)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "image-bytes" {
		t.Fatalf("response images = %#v, want decoded image", resp.Images)
	}
	if resp.Images[0].MIMEType != "image/png" {
		t.Fatalf("image MIME type = %q, want image/png", resp.Images[0].MIMEType)
	}
}

func TestParseResponsesSSEOutputItemDoneImage(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"done"}`,
		`data: {"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","result":"` + imageB64 + `"}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("response text = %q, want done", resp.Text)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "image-bytes" {
		t.Fatalf("response images = %#v, want decoded image", resp.Images)
	}
}

func TestParseResponsesSSECompletedImage(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := `data: {"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","result":"` + imageB64 + `"}]}}`

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "image-bytes" {
		t.Fatalf("response images = %#v, want decoded image", resp.Images)
	}
}

func TestParseResponsesSSECompletedImageFromKnownItemWithoutType(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"image_item_1","type":"image_generation_call","status":"in_progress"}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"image_item_1","result":"` + imageB64 + `"}]}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "image-bytes" {
		t.Fatalf("response images = %#v, want decoded image", resp.Images)
	}
}

func TestParseResponsesSSEIgnoresUntypedImageResultWithoutKnownItem(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := `data: {"type":"response.completed","response":{"output":[{"id":"image_item_1","result":"` + imageB64 + `"}]}}`

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 0 {
		t.Fatalf("response images = %#v, want no image without a known image_generation_call item", resp.Images)
	}
}

func TestParseResponsesSSEIgnoresIgPrefixWithoutKnownItem(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	input := `data: {"type":"response.completed","response":{"output":[{"id":"ig_1","result":"` + imageB64 + `"}]}}`

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 0 {
		t.Fatalf("response images = %#v, want no image from ig_ prefix alone", resp.Images)
	}
}

func TestParseResponsesSSEUsesCompletedImageOverGeneratingItem(t *testing.T) {
	generatingB64 := base64.StdEncoding.EncodeToString([]byte("gray-preview"))
	finalB64 := base64.StdEncoding.EncodeToString([]byte("final-image"))
	input := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","status":"generating","result":"` + generatingB64 + `"}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":"` + finalB64 + `"}]}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "final-image" {
		t.Fatalf("response images = %#v, want final image only", resp.Images)
	}
}

func TestParseResponsesSSECompletedImageEventDedupesResponseOutput(t *testing.T) {
	imageB64 := base64.StdEncoding.EncodeToString([]byte("final-image"))
	input := strings.Join([]string{
		`data: {"type":"response.image_generation_call.completed","item_id":"ig_1","result":"` + imageB64 + `"}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":"` + imageB64 + `"}]}}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 1 || string(resp.Images[0].Data) != "final-image" {
		t.Fatalf("response images = %#v, want one final image", resp.Images)
	}
}

func TestParseResponsesSSEIgnoresPartialImage(t *testing.T) {
	partialB64 := base64.StdEncoding.EncodeToString([]byte("partial-image"))
	input := strings.Join([]string{
		`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_1","partial_image_b64":"` + partialB64 + `"}`,
		"data: [DONE]",
	}, "\n")

	resp, err := parseResponsesSSE(strings.NewReader(input), nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE returned error: %v", err)
	}
	if len(resp.Images) != 0 {
		t.Fatalf("response images = %#v, want no partial images", resp.Images)
	}
}

func TestParseResponsesImageMalformedBase64(t *testing.T) {
	_, err := parseResponsesOutput(responsesOutput{Output: []responsesOutputItem{
		{ID: "ig_1", Type: "image_generation_call", Result: "not-base64"},
	}})
	if err == nil || !strings.Contains(err.Error(), "decode response image result") {
		t.Fatalf("parseResponsesOutput error = %v, want decode response image result", err)
	}
}
