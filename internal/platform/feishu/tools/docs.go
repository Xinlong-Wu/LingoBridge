package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larksearch "github.com/larksuite/oapi-sdk-go/v3/service/search/v2"

	"lingobridge/internal/core"
	"lingobridge/internal/platform/feishu"
)

const (
	searchToolName = "feishu_docs_search"
	readToolName   = "feishu_docs_read"
	createToolName = "feishu_docs_create"
	appendToolName = "feishu_docs_append"
	docxTextBlock  = 2
)

type docsTool struct {
	name   string
	spec   core.ToolSpec
	client *lark.Client
	cfg    feishu.ToolsConfig
}

// NewDocsTools returns Feishu document tools for tool-capable LLM providers.
func NewDocsTools(client *lark.Client, cfg feishu.ToolsConfig) []core.Tool {
	cfg = feishu.NormalizeToolsConfig(cfg)
	if client == nil || !cfg.Docs.Enabled {
		return nil
	}
	tools := []core.Tool{
		docsTool{name: searchToolName, spec: docsSearchSpec(), client: client, cfg: cfg},
		docsTool{name: readToolName, spec: docsReadSpec(), client: client, cfg: cfg},
	}
	if cfg.Docs.AllowWrite && len(cfg.AllowedFolderTokens) > 0 {
		tools = append(tools,
			docsTool{name: createToolName, spec: docsCreateSpec(), client: client, cfg: cfg},
			docsTool{name: appendToolName, spec: docsAppendSpec(), client: client, cfg: cfg},
		)
	}
	return tools
}

func (t docsTool) Spec() core.ToolSpec {
	return t.spec
}

func (t docsTool) Execute(ctx context.Context, call core.ToolCall) core.ToolResult {
	var content string
	var err error
	switch t.name {
	case searchToolName:
		content, err = t.search(ctx, call.Arguments)
	case readToolName:
		content, err = t.read(ctx, call.Arguments)
	case createToolName:
		content, err = t.create(ctx, call.Arguments)
	case appendToolName:
		content, err = t.append(ctx, call.Arguments)
	default:
		err = fmt.Errorf("unsupported feishu docs tool %q", t.name)
	}
	return core.ToolResult{
		CallID:  call.ID,
		Name:    t.name,
		Content: contentOrError(content, err),
		IsError: err != nil,
	}
}

func contentOrError(content string, err error) string {
	if err != nil {
		return err.Error()
	}
	return content
}

type searchArgs struct {
	Query    string `json:"query"`
	MaxItems int    `json:"max_items,omitempty"`
	SpaceID  string `json:"space_id,omitempty"`
}

type readArgs struct {
	Token string `json:"token,omitempty"`
	URL   string `json:"url,omitempty"`
	Type  string `json:"type,omitempty"`
}

type createArgs struct {
	Title       string `json:"title"`
	Content     string `json:"content,omitempty"`
	FolderToken string `json:"folder_token"`
}

type appendArgs struct {
	Token       string `json:"token,omitempty"`
	URL         string `json:"url,omitempty"`
	Content     string `json:"content"`
	FolderToken string `json:"folder_token"`
}

type searchOutput struct {
	Query   string         `json:"query"`
	Results []searchResult `json:"results"`
}

type searchResult struct {
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	Type    string `json:"type,omitempty"`
	URL     string `json:"url,omitempty"`
	Token   string `json:"token,omitempty"`
	Owner   string `json:"owner,omitempty"`
}

type readOutput struct {
	Token     string `json:"token"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

type writeOutput struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Appended   bool   `json:"appended,omitempty"`
}

func (t docsTool) search(ctx context.Context, raw json.RawMessage) (string, error) {
	var args searchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	limit := args.MaxItems
	if limit <= 0 || limit > t.cfg.MaxResults {
		limit = t.cfg.MaxResults
	}

	bodyBuilder := larksearch.NewSearchDocWikiReqBodyBuilder().
		Query(args.Query).
		PageSize(limit)
	if len(t.cfg.AllowedFolderTokens) > 0 {
		bodyBuilder.DocFilter(larksearch.NewDocFilterBuilder().FolderTokens(t.cfg.AllowedFolderTokens).Build())
	}
	spaceIDs := t.cfg.AllowedSpaceIDs
	if args.SpaceID = strings.TrimSpace(args.SpaceID); args.SpaceID != "" {
		if len(t.cfg.AllowedSpaceIDs) > 0 && !allowedValue(args.SpaceID, t.cfg.AllowedSpaceIDs) {
			return "", fmt.Errorf("space_id %q is not allowed", args.SpaceID)
		}
		spaceIDs = []string{args.SpaceID}
	}
	if len(spaceIDs) > 0 {
		bodyBuilder.WikiFilter(larksearch.NewWikiFilterBuilder().SpaceIds(spaceIDs).Build())
	}
	req := larksearch.NewSearchDocWikiReqBuilder().Body(bodyBuilder.Build()).Build()
	resp, err := t.client.Search.DocWiki.Search(ctx, req)
	if err != nil {
		return "", fmt.Errorf("search feishu docs: %w", err)
	}
	if resp == nil || !resp.Success() {
		return "", fmt.Errorf("search feishu docs code=%d msg=%s", resp.Code, resp.Msg)
	}

	out := searchOutput{Query: args.Query}
	if resp.Data != nil {
		for _, unit := range resp.Data.ResUnits {
			if unit == nil {
				continue
			}
			result := searchResult{
				Title:   stripSearchHighlight(deref(unit.TitleHighlighted)),
				Summary: stripSearchHighlight(deref(unit.SummaryHighlighted)),
				Type:    deref(unit.EntityType),
			}
			if unit.ResultMeta != nil {
				result.URL = deref(unit.ResultMeta.Url)
				result.Token = deref(unit.ResultMeta.Token)
				result.Owner = deref(unit.ResultMeta.OwnerName)
				if result.Type == "" {
					result.Type = deref(unit.ResultMeta.DocTypes)
				}
			}
			out.Results = append(out.Results, result)
		}
	}
	return marshalToolOutput(out)
}

func (t docsTool) read(ctx context.Context, raw json.RawMessage) (string, error) {
	var args readArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	ref, err := parseDocRef(args.Token, args.URL, args.Type)
	if err != nil {
		return "", err
	}
	if ref.Kind != "docx" {
		return "", fmt.Errorf("reading %s documents is not supported yet; provide a docx token or URL", ref.Kind)
	}
	req := larkdocx.NewRawContentDocumentReqBuilder().
		DocumentId(ref.Token).
		Lang(larkdocx.LangZH).
		Build()
	resp, err := t.client.Docx.Document.RawContent(ctx, req)
	if err != nil {
		return "", fmt.Errorf("read feishu document: %w", err)
	}
	if resp == nil || !resp.Success() {
		return "", fmt.Errorf("read feishu document code=%d msg=%s", resp.Code, resp.Msg)
	}
	content := ""
	if resp.Data != nil {
		content = deref(resp.Data.Content)
	}
	content, truncated := truncateRunes(content, t.cfg.MaxChars)
	return marshalToolOutput(readOutput{Token: ref.Token, Type: ref.Kind, Content: content, Truncated: truncated})
}

func (t docsTool) create(ctx context.Context, raw json.RawMessage) (string, error) {
	var args createArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	args.Title = strings.TrimSpace(args.Title)
	args.FolderToken = strings.TrimSpace(args.FolderToken)
	if args.Title == "" {
		return "", fmt.Errorf("title is required")
	}
	if !allowedValue(args.FolderToken, t.cfg.AllowedFolderTokens) {
		return "", fmt.Errorf("folder_token %q is not allowed", args.FolderToken)
	}
	req := larkdocx.NewCreateDocumentReqBuilder().
		Body(larkdocx.NewCreateDocumentReqBodyBuilder().
			Title(args.Title).
			FolderToken(args.FolderToken).
			Build()).
		Build()
	resp, err := t.client.Docx.Document.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("create feishu document: %w", err)
	}
	if resp == nil || !resp.Success() {
		return "", fmt.Errorf("create feishu document code=%d msg=%s", resp.Code, resp.Msg)
	}
	docID := ""
	if resp.Data != nil && resp.Data.Document != nil {
		docID = deref(resp.Data.Document.DocumentId)
	}
	if docID == "" {
		return "", fmt.Errorf("create feishu document returned no document_id")
	}
	if strings.TrimSpace(args.Content) != "" {
		if err := t.appendTextBlocks(ctx, docID, args.Content); err != nil {
			return "", err
		}
	}
	return marshalToolOutput(writeOutput{DocumentID: docID, Title: args.Title, URL: "https://docs.feishu.cn/docx/" + docID})
}

func (t docsTool) append(ctx context.Context, raw json.RawMessage) (string, error) {
	var args appendArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	if strings.TrimSpace(args.Content) == "" {
		return "", fmt.Errorf("content is required")
	}
	if !allowedValue(strings.TrimSpace(args.FolderToken), t.cfg.AllowedFolderTokens) {
		return "", fmt.Errorf("folder_token %q is not allowed", args.FolderToken)
	}
	ref, err := parseDocRef(args.Token, args.URL, "docx")
	if err != nil {
		return "", err
	}
	if ref.Kind != "docx" {
		return "", fmt.Errorf("append supports docx documents only")
	}
	if err := t.appendTextBlocks(ctx, ref.Token, args.Content); err != nil {
		return "", err
	}
	return marshalToolOutput(writeOutput{DocumentID: ref.Token, URL: "https://docs.feishu.cn/docx/" + ref.Token, Appended: true})
}

func (t docsTool) appendTextBlocks(ctx context.Context, documentID, content string) error {
	blocks := textBlocks(content)
	if len(blocks) == 0 {
		return nil
	}
	index := 0
	childrenReq := larkdocx.NewGetDocumentBlockChildrenReqBuilder().
		DocumentId(documentID).
		BlockId(documentID).
		DocumentRevisionId(-1).
		PageSize(500).
		Build()
	children, err := t.client.Docx.DocumentBlockChildren.Get(ctx, childrenReq)
	if err == nil && children != nil && children.Success() && children.Data != nil {
		index = len(children.Data.Items)
	}
	req := larkdocx.NewCreateDocumentBlockChildrenReqBuilder().
		DocumentId(documentID).
		BlockId(documentID).
		DocumentRevisionId(-1).
		Body(larkdocx.NewCreateDocumentBlockChildrenReqBodyBuilder().
			Children(blocks).
			Index(index).
			Build()).
		Build()
	resp, err := t.client.Docx.DocumentBlockChildren.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("append feishu document: %w", err)
	}
	if resp == nil || !resp.Success() {
		return fmt.Errorf("append feishu document code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func textBlocks(content string) []*larkdocx.Block {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	blocks := make([]*larkdocx.Block, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		text := larkdocx.NewTextBuilder().
			Elements([]*larkdocx.TextElement{
				larkdocx.NewTextElementBuilder().
					TextRun(larkdocx.NewTextRunBuilder().Content(line).Build()).
					Build(),
			}).
			Build()
		blocks = append(blocks, larkdocx.NewBlockBuilder().BlockType(docxTextBlock).Text(text).Build())
	}
	return blocks
}

type docRef struct {
	Kind  string
	Token string
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,}$`)

func parseDocRef(token, rawURL, kind string) (docRef, error) {
	kind = normalizeDocKind(kind)
	token = strings.TrimSpace(token)
	rawURL = strings.TrimSpace(rawURL)
	if token == "" && rawURL == "" {
		return docRef{}, fmt.Errorf("token or url is required")
	}
	if token == "" {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return docRef{}, fmt.Errorf("parse url: %w", err)
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		for i, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			switch part {
			case "docx", "docs", "doc":
				if i+1 < len(parts) {
					kind = "docx"
					token = strings.TrimSpace(parts[i+1])
				}
			case "wiki":
				if i+1 < len(parts) {
					kind = "wiki"
					token = strings.TrimSpace(parts[i+1])
				}
			case "file":
				if i+1 < len(parts) {
					kind = "file"
					token = strings.TrimSpace(parts[i+1])
				}
			}
		}
	}
	if token == "" {
		return docRef{}, fmt.Errorf("document token not found")
	}
	if !tokenPattern.MatchString(token) {
		return docRef{}, fmt.Errorf("invalid document token")
	}
	if kind == "" {
		kind = "docx"
	}
	return docRef{Kind: kind, Token: token}, nil
}

func normalizeDocKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "doc", "docs", "document":
		return "docx"
	default:
		return kind
	}
}

func docsSearchSpec() core.ToolSpec {
	return core.ToolSpec{
		Name:        searchToolName,
		Description: "Search Feishu Docs and Wiki visible to the configured Feishu app. Returns titles, summaries, URLs, and tokens.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search keywords."},"max_items":{"type":"integer","minimum":1,"maximum":20},"space_id":{"type":"string","description":"Optional Wiki space ID; must be allowed by configuration."}},"required":["query"],"additionalProperties":false}`),
	}
}

func docsReadSpec() core.ToolSpec {
	return core.ToolSpec{
		Name:        readToolName,
		Description: "Read plain text from a Feishu docx document by token or URL. The result may be truncated by configuration.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"token":{"type":"string","description":"Feishu docx document token."},"url":{"type":"string","description":"Feishu document URL."},"type":{"type":"string","enum":["docx","wiki","file"],"description":"Document type hint."}},"additionalProperties":false}`),
	}
}

func docsCreateSpec() core.ToolSpec {
	return core.ToolSpec{
		Name:        createToolName,
		Description: "Create a Feishu docx document in an allowed folder and optionally add initial text content.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"content":{"type":"string"},"folder_token":{"type":"string","description":"Must be listed in platforms.feishu.tools.allowed_folder_tokens."}},"required":["title","folder_token"],"additionalProperties":false}`),
	}
}

func docsAppendSpec() core.ToolSpec {
	return core.ToolSpec{
		Name:        appendToolName,
		Description: "Append plain text paragraphs to an existing Feishu docx document. Requires an allowed folder token guard.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"token":{"type":"string"},"url":{"type":"string"},"content":{"type":"string"},"folder_token":{"type":"string","description":"Must be listed in platforms.feishu.tools.allowed_folder_tokens."}},"required":["content","folder_token"],"additionalProperties":false}`),
	}
}

func allowedValue(value string, allowed []string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func stripSearchHighlight(text string) string {
	text = strings.ReplaceAll(text, "<h>", "")
	text = strings.ReplaceAll(text, "</h>", "")
	return html.UnescapeString(text)
}

func marshalToolOutput(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func truncateRunes(text string, limit int) (string, bool) {
	if limit <= 0 {
		return text, false
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]), true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
