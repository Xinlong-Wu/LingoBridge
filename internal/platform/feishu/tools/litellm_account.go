package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkbitable "github.com/larksuite/oapi-sdk-go/v3/service/bitable/v1"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"

	"lingobridge/internal/logging"
	tooltypes "lingobridge/internal/tools"
)

const (
	liteLLMInviteToolName = "feishu_litellm_invite_create"
	liteLLMUserPath       = "/user/new"
	liteLLMInvitationPath = "/invitation/new"
	liteLLMHTTPTimeout    = 10 * time.Second
	maxReasonRunes        = 2000
)

var feishuToolsLog = logging.For("feishu/tools")
var emailLikePattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

type liteLLMInviteTool struct {
	spec    tooltypes.Spec
	bitable liteLLMBitableWriter
	litellm liteLLMProvisioner
	actor   liteLLMActorResolver
	cfg     LiteLLMToolsConfig
}

type liteLLMBitableWriter interface {
	CreateRecord(ctx context.Context, fields map[string]interface{}) (string, error)
}

type liteLLMProvisioner interface {
	CreateUser(ctx context.Context, email, role, userAlias string) (string, error)
	CreateInvitation(ctx context.Context, userID string) (string, error)
	InvitationLink(invitationID string) string
}

type liteLLMActorResolver interface {
	ResolveActor(ctx context.Context, actor Actor) (Actor, error)
}

// NewLiteLLMAccountTools returns a Feishu tool that records account requests in
// Bitable, creates a LiteLLM user, and returns a password setup invitation URL.
func NewLiteLLMAccountTools(client *lark.Client, cfg Config) []tooltypes.Tool {
	cfg = NormalizeConfig(cfg)
	liteCfg := cfg.LiteLLM
	if client == nil || !liteCfg.Enabled {
		return nil
	}
	if missing := missingLiteLLMConfig(liteCfg); len(missing) > 0 {
		feishuToolsLog.Warn(context.Background(), "litellm account tool enabled but missing config fields: %s", strings.Join(missing, ", "))
		return nil
	}
	return []tooltypes.Tool{liteLLMInviteTool{
		spec:    liteLLMInviteSpec(),
		bitable: larkBitableWriter{client: client, cfg: liteCfg.Bitable},
		litellm: newLiteLLMHTTPClient(liteCfg.BaseURL, liteCfg.APIKey),
		actor:   larkActorResolver{client: client},
		cfg:     liteCfg,
	}}
}

func missingLiteLLMConfig(cfg LiteLLMToolsConfig) []string {
	var missing []string
	if cfg.BaseURL == "" {
		missing = append(missing, "base_url")
	}
	if cfg.APIKey == "" {
		missing = append(missing, "api_key")
	}
	if cfg.Bitable.AppToken == "" {
		missing = append(missing, "bitable.app_token")
	}
	if cfg.Bitable.TableID == "" {
		missing = append(missing, "bitable.table_id")
	}
	if cfg.Bitable.EmailField == "" {
		missing = append(missing, "bitable.email_field")
	}
	if cfg.Bitable.ReasonField == "" {
		missing = append(missing, "bitable.reason_field")
	}
	return missing
}

func (t liteLLMInviteTool) Spec() tooltypes.Spec {
	return t.spec
}

func (t liteLLMInviteTool) Execute(ctx context.Context, call tooltypes.Call) tooltypes.Result {
	content, err := t.createInvite(ctx, call.Arguments)
	return tooltypes.Result{
		CallID:  call.ID,
		Name:    liteLLMInviteToolName,
		Content: contentOrError(content, err),
		IsError: err != nil,
	}
}

type liteLLMInviteArgs struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

type liteLLMInviteOutput struct {
	Email              string `json:"email"`
	BitableRecordID    string `json:"bitable_record_id,omitempty"`
	LiteLLMUserID      string `json:"litellm_user_id,omitempty"`
	InvitationID       string `json:"invitation_id"`
	InvitationLink     string `json:"invitation_link"`
	InvitationMarkdown string `json:"invitation_markdown"`
}

func (t liteLLMInviteTool) createInvite(ctx context.Context, raw json.RawMessage) (string, error) {
	var args liteLLMInviteArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}
	email, err := normalizeInviteEmail(args.Email)
	if err != nil {
		return "", err
	}
	reason := strings.TrimSpace(args.Reason)
	if reason == "" {
		return "", fmt.Errorf("reason is required")
	}
	if utf8.RuneCountInString(reason) > maxReasonRunes {
		return "", fmt.Errorf("reason is too long: max %d characters", maxReasonRunes)
	}

	start := time.Now()
	emailRef := maskedEmail(email)
	feishuToolsLog.Debug(ctx, "litellm invite request start email=%s reason_chars=%d", emailRef, utf8.RuneCountInString(reason))

	actor, _ := ActorFromContext(ctx)
	actor = t.resolveActor(ctx, actor, emailRef)
	fields := map[string]interface{}{
		t.cfg.Bitable.EmailField:  email,
		t.cfg.Bitable.ReasonField: reason,
	}
	ownerSet := false
	if ownerField := strings.TrimSpace(t.cfg.Bitable.OwnerField); ownerField != "" {
		ownerValue := bitableOwnerValue(actor)
		if len(ownerValue) > 0 {
			fields[ownerField] = ownerValue
			ownerSet = true
		} else {
			feishuToolsLog.Warn(ctx, "litellm invite owner field configured but sender open_id missing email=%s", emailRef)
		}
	}
	recordID, err := t.bitable.CreateRecord(ctx, fields)
	if err != nil {
		feishuToolsLog.Error(ctx, "litellm invite bitable create failed email=%s: %v", emailRef, err)
		return "", fmt.Errorf("create bitable record: %w", err)
	}
	feishuToolsLog.Debug(ctx, "litellm invite bitable record created email=%s record=%s owner_set=%t", emailRef, recordID, ownerSet)

	if actor.OpenID != "" && actor.Name == "" {
		feishuToolsLog.Warn(ctx, "litellm invite sender name unavailable; user_alias omitted email=%s sender_ref=%s", emailRef, hashString(actor.OpenID))
	}
	userID, err := t.litellm.CreateUser(ctx, email, t.cfg.UserRole, actor.Name)
	if err != nil {
		feishuToolsLog.Error(ctx, "litellm invite user create failed email=%s record=%s: %v", emailRef, recordID, err)
		return "", fmt.Errorf("create litellm user: %w", err)
	}
	feishuToolsLog.Debug(ctx, "litellm invite user created email=%s record=%s user=%s", emailRef, recordID, userID)

	invitationID, err := t.litellm.CreateInvitation(ctx, userID)
	if err != nil {
		feishuToolsLog.Error(ctx, "litellm invite creation failed email=%s record=%s user=%s: %v", emailRef, recordID, userID, err)
		return "", fmt.Errorf("create litellm invitation: %w", err)
	}
	link := t.litellm.InvitationLink(invitationID)

	out := liteLLMInviteOutput{
		Email:              email,
		BitableRecordID:    recordID,
		LiteLLMUserID:      userID,
		InvitationID:       invitationID,
		InvitationLink:     link,
		InvitationMarkdown: markdownLink("Invitation Link", link),
	}
	feishuToolsLog.Info(ctx, "litellm invite created email=%s record=%s user=%s invitation_ref=%s duration_ms=%d", emailRef, recordID, userID, hashString(invitationID), time.Since(start).Milliseconds())
	return marshalToolOutput(out)
}

func (t liteLLMInviteTool) resolveActor(ctx context.Context, actor Actor, emailRef string) Actor {
	actor = normalizeActor(actor)
	if (actor.OpenID == "" && actor.UserID == "") || t.actor == nil {
		return actor
	}
	resolved, err := t.actor.ResolveActor(ctx, actor)
	if err != nil {
		feishuToolsLog.Warn(ctx, "resolve feishu sender for litellm invite failed email=%s sender_ref=%s: %v", emailRef, actorLogRef(actor), err)
		return actor
	}
	resolved = normalizeActor(resolved)
	if resolved.OpenID == "" {
		resolved.OpenID = actor.OpenID
	}
	if resolved.UserID == "" {
		resolved.UserID = actor.UserID
	}
	feishuToolsLog.Debug(ctx, "resolved feishu sender for litellm invite sender_ref=%s alias_set=%t email_set=%t", actorLogRef(resolved), resolved.Name != "", resolved.Email != "")
	return resolved
}

func normalizeInviteEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("email is required")
	}
	addr, err := mail.ParseAddress(value)
	if err != nil {
		return "", fmt.Errorf("invalid email address")
	}
	email := strings.ToLower(strings.TrimSpace(addr.Address))
	if email == "" || strings.ContainsAny(email, " \t\r\n") {
		return "", fmt.Errorf("invalid email address")
	}
	return email, nil
}

func liteLLMInviteSpec() tooltypes.Spec {
	return tooltypes.Spec{
		Name:        liteLLMInviteToolName,
		Description: "Record an explicit email, application reason, and Feishu sender owner in Bitable, create a LiteLLM account with the sender name as user_alias when available, and return a password setup invitation link. Use only when both email and reason are provided by the user. After success, reply with exactly the `invitation_markdown` value, such as `[Invitation Link](<invitation_link>)`.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"email":{"type":"string","format":"email","description":"The applicant email address."},"reason":{"type":"string","minLength":1,"description":"The user's explicit application reason; do not invent it."}},"required":["email","reason"],"additionalProperties":false}`),
	}
}

func markdownLink(label, href string) string {
	label = strings.NewReplacer("[", "\\[", "]", "\\]").Replace(strings.TrimSpace(label))
	href = strings.TrimSpace(href)
	if label == "" {
		label = "Link"
	}
	if href == "" {
		return label
	}
	return "[" + label + "](" + href + ")"
}

func bitableOwnerValue(actor Actor) []*larkbitable.Person {
	actor = normalizeActor(actor)
	if actor.OpenID == "" {
		return nil
	}
	builder := larkbitable.NewPersonBuilder().Id(actor.OpenID)
	if actor.Name != "" {
		builder.Name(actor.Name)
	}
	if actor.Email != "" {
		builder.Email(actor.Email)
	}
	return []*larkbitable.Person{builder.Build()}
}

type larkBitableWriter struct {
	client *lark.Client
	cfg    LiteLLMBitableConfig
}

func (w larkBitableWriter) CreateRecord(ctx context.Context, fields map[string]interface{}) (string, error) {
	req := larkbitable.NewCreateAppTableRecordReqBuilder().
		AppToken(w.cfg.AppToken).
		TableId(w.cfg.TableID).
		UserIdType(larkbitable.UserIdTypeCreateAppTableRecordOpenId).
		AppTableRecord(larkbitable.NewAppTableRecordBuilder().Fields(fields).Build()).
		Build()
	resp, err := w.client.Bitable.AppTableRecord.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("create feishu bitable record: %w", err)
	}
	if resp == nil || !resp.Success() {
		code, msg := bitableError(resp)
		return "", fmt.Errorf("create feishu bitable record code=%d msg=%s", code, msg)
	}
	recordID := ""
	if resp.Data != nil && resp.Data.Record != nil {
		recordID = deref(resp.Data.Record.RecordId)
	}
	if recordID == "" {
		return "", fmt.Errorf("create feishu bitable record returned no record_id")
	}
	return recordID, nil
}

type larkActorResolver struct {
	client *lark.Client
}

func (r larkActorResolver) ResolveActor(ctx context.Context, actor Actor) (Actor, error) {
	actor = normalizeActor(actor)
	if r.client == nil || r.client.Contact == nil {
		return actor, nil
	}
	lookupID := actor.OpenID
	lookupType := larkcontact.UserIdTypeOpenId
	if lookupID == "" {
		lookupID = actor.UserID
		lookupType = larkcontact.UserIdTypeUserId
	}
	if lookupID == "" {
		return actor, nil
	}
	req := larkcontact.NewGetUserReqBuilder().
		UserId(lookupID).
		UserIdType(lookupType).
		Build()
	resp, err := r.client.Contact.User.Get(ctx, req)
	if err != nil {
		return actor, fmt.Errorf("get feishu contact user: %w", err)
	}
	if resp == nil || !resp.Success() {
		code, msg := contactUserError(resp)
		return actor, fmt.Errorf("get feishu contact user code=%d msg=%s", code, msg)
	}
	if resp.Data == nil || resp.Data.User == nil {
		return actor, fmt.Errorf("get feishu contact user returned no user")
	}
	user := resp.Data.User
	if actor.OpenID == "" {
		actor.OpenID = deref(user.OpenId)
	}
	if actor.UserID == "" {
		actor.UserID = deref(user.UserId)
	}
	if actor.Name == "" {
		actor.Name = firstNonEmptyString(deref(user.Name), deref(user.EnName), deref(user.Nickname))
	}
	if actor.Email == "" {
		actor.Email = firstNonEmptyString(deref(user.Email), deref(user.EnterpriseEmail))
	}
	return normalizeActor(actor), nil
}

func contactUserError(resp *larkcontact.GetUserResp) (int, string) {
	if resp == nil {
		return 0, "empty response"
	}
	return resp.Code, resp.Msg
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func actorLogRef(actor Actor) string {
	ref := firstNonEmptyString(actor.OpenID, actor.UserID)
	if ref == "" {
		return ""
	}
	return hashString(ref)
}

func bitableError(resp *larkbitable.CreateAppTableRecordResp) (int, string) {
	if resp == nil {
		return 0, "empty response"
	}
	return resp.Code, resp.Msg
}

type liteLLMHTTPClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newLiteLLMHTTPClient(baseURL, apiKey string) liteLLMHTTPClient {
	return liteLLMHTTPClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Timeout: liteLLMHTTPTimeout,
		},
	}
}

type liteLLMNewUserRequest struct {
	UserEmail       string `json:"user_email"`
	SendInviteEmail bool   `json:"send_invite_email"`
	AutoCreateKey   bool   `json:"auto_create_key"`
	UserRole        string `json:"user_role,omitempty"`
	UserAlias       string `json:"user_alias,omitempty"`
}

type liteLLMNewUserResponse struct {
	UserID string `json:"user_id"`
	ID     string `json:"id"`
}

type liteLLMInvitationRequest struct {
	UserID string `json:"user_id"`
}

type liteLLMInvitationResponse struct {
	ID string `json:"id"`
}

func (c liteLLMHTTPClient) CreateUser(ctx context.Context, email, role, userAlias string) (string, error) {
	body := liteLLMNewUserRequest{
		UserEmail:       email,
		SendInviteEmail: false,
		AutoCreateKey:   false,
		UserRole:        strings.TrimSpace(role),
		UserAlias:       strings.TrimSpace(userAlias),
	}
	var out liteLLMNewUserResponse
	if err := c.post(ctx, liteLLMUserPath, body, &out); err != nil {
		return "", err
	}
	userID := strings.TrimSpace(out.UserID)
	if userID == "" {
		userID = strings.TrimSpace(out.ID)
	}
	if userID == "" {
		return "", fmt.Errorf("litellm user response missing user_id")
	}
	return userID, nil
}

func (c liteLLMHTTPClient) CreateInvitation(ctx context.Context, userID string) (string, error) {
	var out liteLLMInvitationResponse
	if err := c.post(ctx, liteLLMInvitationPath, liteLLMInvitationRequest{UserID: userID}, &out); err != nil {
		return "", err
	}
	invitationID := strings.TrimSpace(out.ID)
	if invitationID == "" {
		return "", fmt.Errorf("litellm invitation response missing id")
	}
	return invitationID, nil
}

func (c liteLLMHTTPClient) InvitationLink(invitationID string) string {
	baseURL := strings.TrimRight(c.baseURL, "/")
	return baseURL + "/ui?invitation_id=" + url.QueryEscape(strings.TrimSpace(invitationID))
}

func (c liteLLMHTTPClient) post(ctx context.Context, path string, body any, out any) error {
	reqBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal litellm request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("litellm %s request: %w", path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("litellm %s read response: %w", path, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("litellm %s HTTP %d: %s", path, resp.StatusCode, sanitizeLiteLLMErrorBody(respBody, 500))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("parse litellm %s response: %w", path, err)
	}
	return nil
}

func sanitizeLiteLLMErrorBody(data []byte, limit int) string {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return ""
	}
	text = emailLikePattern.ReplaceAllString(text, "<email>")
	truncated, _ := truncateRunes(text, limit)
	return truncated
}

func maskedEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return hashString(email)
	}
	name := email[:at]
	domain := email[at+1:]
	if len(name) > 1 {
		name = name[:1] + "***"
	} else {
		name = "***"
	}
	return name + "@" + domain
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}
