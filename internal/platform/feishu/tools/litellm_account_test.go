package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkbitable "github.com/larksuite/oapi-sdk-go/v3/service/bitable/v1"
)

func TestLiteLLMAccountToolRegistration(t *testing.T) {
	client := &lark.Client{}
	if got := NewLiteLLMAccountTools(client, Config{}); len(got) != 0 {
		t.Fatalf("disabled tools = %d, want 0", len(got))
	}

	cfg := NormalizeConfig(Config{
		LiteLLM: LiteLLMToolsConfig{
			Enabled: true,
			BaseURL: "https://litellm.example/",
			APIKey:  "sk-admin",
			Bitable: LiteLLMBitableConfig{
				AppToken: "base_token",
				TableID:  "tbl_token",
			},
		},
	})
	tools := NewLiteLLMAccountTools(client, cfg)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	spec := tools[0].Spec()
	if spec.Name != liteLLMInviteToolName || !strings.Contains(spec.Description, "LiteLLM") {
		t.Fatalf("spec = %#v", spec)
	}
	if cfg.LiteLLM.UserRole != DefaultLiteLLMUserRole {
		t.Fatalf("user role = %q, want default", cfg.LiteLLM.UserRole)
	}
	if cfg.LiteLLM.Bitable.EmailField != DefaultLiteLLMEmailField || cfg.LiteLLM.Bitable.ReasonField != DefaultLiteLLMReasonField || cfg.LiteLLM.Bitable.OwnerField != DefaultLiteLLMOwnerField {
		t.Fatalf("bitable fields = %#v, want defaults", cfg.LiteLLM.Bitable)
	}
}

func TestLiteLLMInviteToolCreatesRecordUserInvitationAndUpdatesRecord(t *testing.T) {
	bitable := &fakeLiteLLMBitable{recordID: "rec_1"}
	provisioner := &fakeLiteLLMProvisioner{userID: "user_1", invitationID: "inv_1", link: "https://litellm.example/ui?invitation_id=inv_1"}
	actorResolver := &fakeLiteLLMActorResolver{resolved: Actor{OpenID: "ou_sender", UserID: "user_sender", Name: "Alice", Email: "alice@example.com"}}
	tool := liteLLMInviteTool{
		spec:    liteLLMInviteSpec(),
		bitable: bitable,
		litellm: provisioner,
		actor:   actorResolver,
		cfg: LiteLLMToolsConfig{
			UserRole: "internal_user",
			Bitable: LiteLLMBitableConfig{
				EmailField:  "邮箱",
				ReasonField: "申请原因",
				OwnerField:  "所有者",
			},
		},
	}

	ctx := WithActor(context.Background(), Actor{OpenID: "ou_sender"})
	content, err := tool.createInvite(ctx, json.RawMessage(`{"email":" User@Example.COM ","reason":"需要接入测试"}`))
	if err != nil {
		t.Fatalf("createInvite returned error: %v", err)
	}
	if got := bitable.created["邮箱"]; got != "user@example.com" {
		t.Fatalf("email field = %#v, want normalized email", got)
	}
	if got := bitable.created["申请原因"]; got != "需要接入测试" {
		t.Fatalf("reason field = %#v", got)
	}
	owners, ok := bitable.created["所有者"].([]*larkbitable.Person)
	if !ok || len(owners) != 1 || owners[0] == nil || deref(owners[0].Id) != "ou_sender" || deref(owners[0].Name) != "Alice" || deref(owners[0].Email) != "alice@example.com" {
		t.Fatalf("owner field = %#v, want sender person", bitable.created["所有者"])
	}
	if !actorResolver.called || actorResolver.in.OpenID != "ou_sender" {
		t.Fatalf("actor resolver input = %#v called=%v", actorResolver.in, actorResolver.called)
	}
	if provisioner.email != "user@example.com" || provisioner.role != "internal_user" || provisioner.alias != "Alice" {
		t.Fatalf("provisioner request = email:%q role:%q alias:%q", provisioner.email, provisioner.role, provisioner.alias)
	}
	if provisioner.inviteUserID != "user_1" {
		t.Fatalf("invite user = %q, want user_1", provisioner.inviteUserID)
	}

	var out liteLLMInviteOutput
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if out.Email != "user@example.com" || out.BitableRecordID != "rec_1" || out.LiteLLMUserID != "user_1" || out.InvitationID != "inv_1" || out.InvitationLink != provisioner.link || out.InvitationMarkdown != "[Invitation Link](https://litellm.example/ui?invitation_id=inv_1)" {
		t.Fatalf("output = %#v", out)
	}
}

func TestLiteLLMInviteToolValidatesEmailAndReason(t *testing.T) {
	tool := liteLLMInviteTool{
		spec:    liteLLMInviteSpec(),
		bitable: &fakeLiteLLMBitable{recordID: "rec_1"},
		litellm: &fakeLiteLLMProvisioner{userID: "user_1", invitationID: "inv_1", link: "https://example.test"},
		cfg: LiteLLMToolsConfig{
			Bitable: LiteLLMBitableConfig{EmailField: "邮箱", ReasonField: "申请原因"},
		},
	}

	tests := []struct {
		name    string
		args    string
		wantErr string
	}{
		{name: "email", args: `{"email":"not-email","reason":"test"}`, wantErr: "invalid email"},
		{name: "reason", args: `{"email":"user@example.com","reason":" "}`, wantErr: "reason is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.createInvite(context.Background(), json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("createInvite error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestLiteLLMHTTPClientCreatesUserInvitationAndLink(t *testing.T) {
	var sawUser, sawInvite bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-admin" {
			t.Fatalf("authorization = %q", got)
		}
		switch r.URL.Path {
		case liteLLMUserPath:
			sawUser = true
			var req liteLLMNewUserRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode user request: %v", err)
			}
			if req.UserEmail != "user@example.com" || req.UserRole != "internal_user" || req.UserAlias != "Alice" || req.SendInviteEmail || req.AutoCreateKey {
				t.Fatalf("user request = %#v", req)
			}
			writeJSON(t, w, map[string]any{"user_id": "user_1"})
		case liteLLMInvitationPath:
			sawInvite = true
			var req liteLLMInvitationRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode invitation request: %v", err)
			}
			if req.UserID != "user_1" {
				t.Fatalf("invitation request = %#v", req)
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, map[string]any{"id": "inv_1"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := liteLLMHTTPClient{baseURL: server.URL, apiKey: "sk-admin", httpClient: server.Client()}
	userID, err := client.CreateUser(context.Background(), "user@example.com", "internal_user", "Alice")
	if err != nil {
		t.Fatalf("CreateUser returned error: %v", err)
	}
	invitationID, err := client.CreateInvitation(context.Background(), userID)
	if err != nil {
		t.Fatalf("CreateInvitation returned error: %v", err)
	}
	if !sawUser || !sawInvite || userID != "user_1" || invitationID != "inv_1" {
		t.Fatalf("calls user=%v invite=%v userID=%q invitationID=%q", sawUser, sawInvite, userID, invitationID)
	}
	if got := client.InvitationLink(invitationID); got != server.URL+"/ui?invitation_id=inv_1" {
		t.Fatalf("link = %q", got)
	}
}

func TestSanitizeLiteLLMErrorBodyRedactsEmail(t *testing.T) {
	got := sanitizeLiteLLMErrorBody([]byte(`{"detail":"user@example.com already exists"}`), 500)
	if strings.Contains(got, "user@example.com") || !strings.Contains(got, "<email>") {
		t.Fatalf("sanitized body = %q", got)
	}
}

type fakeLiteLLMBitable struct {
	recordID string
	created  map[string]interface{}
}

func (f *fakeLiteLLMBitable) CreateRecord(ctx context.Context, fields map[string]interface{}) (string, error) {
	f.created = cloneFields(fields)
	return f.recordID, nil
}

type fakeLiteLLMProvisioner struct {
	userID       string
	invitationID string
	link         string
	email        string
	role         string
	alias        string
	inviteUserID string
}

func (f *fakeLiteLLMProvisioner) CreateUser(ctx context.Context, email, role, userAlias string) (string, error) {
	f.email = email
	f.role = role
	f.alias = userAlias
	return f.userID, nil
}

func (f *fakeLiteLLMProvisioner) CreateInvitation(ctx context.Context, userID string) (string, error) {
	f.inviteUserID = userID
	return f.invitationID, nil
}

func (f *fakeLiteLLMProvisioner) InvitationLink(invitationID string) string {
	return f.link
}

type fakeLiteLLMActorResolver struct {
	resolved Actor
	in       Actor
	called   bool
}

func (f *fakeLiteLLMActorResolver) ResolveActor(ctx context.Context, actor Actor) (Actor, error) {
	f.called = true
	f.in = actor
	return f.resolved, nil
}

func cloneFields(fields map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for key, value := range fields {
		out[key] = value
	}
	return out
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode json: %v", err)
	}
}
