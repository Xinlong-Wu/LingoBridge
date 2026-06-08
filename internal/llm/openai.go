package llm

import (
	"net/http"
	"strings"

	"lingobridge/internal/store"
)

const (
	openAIEndpointResponses          = "responses"
	openAIRefProvider                = "openai"
	openAIRefTypeFile                = "file"
	openAIRefTypeImageGenerationCall = "image_generation_call"
	openAIRefTypeCompaction          = "compaction"
)

type openaiBase struct {
	cfg        Config
	httpClient *http.Client
}

func newOpenAIClient(cfg Config) Client {
	base := openaiBase{cfg: cfg, httpClient: http.DefaultClient}
	if cfg.Endpoint == openAIEndpointResponses {
		return &openaiResponsesClient{openaiBase: base}
	}
	return &openaiChatClient{openaiBase: base}
}

func (c *openaiBase) refProvider() string {
	return openAIRefProvider
}

func (c *openaiBase) isRef(ref AttachmentRef, refType string) bool {
	return ref.Provider == c.refProvider() && ref.Type == refType && ref.ID != ""
}

func (c *openaiBase) isStoreRef(attachment store.Attachment, refType string) bool {
	return attachment.RefProvider == c.refProvider() && attachment.RefType == refType && attachment.RefID != ""
}

func (c *openaiBase) uploadVisionFile(filename string, data []byte) (string, error) {
	return uploadOpenAIVisionFile(c.httpClient, c.cfg.BaseURL, c.cfg.APIKey, filename, data)
}

func (c *openaiBase) chatCompletionsURL() string {
	return openAIURL(c.cfg.BaseURL, "/chat/completions")
}

func (c *openaiBase) responsesURL() string {
	return openAIURL(c.cfg.BaseURL, "/responses")
}

func (c *openaiBase) responsesCompactURL() string {
	return openAIURL(c.cfg.BaseURL, "/responses/compact")
}

func openAIURL(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + path
	}
	return base + "/v1" + path
}
