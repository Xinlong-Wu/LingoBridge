package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lingobridge/internal/config"
	"lingobridge/internal/core"
	"lingobridge/internal/store"
	tooltypes "lingobridge/internal/tools"
)

type fakeCursorStore struct {
	buf string
}

func (f *fakeCursorStore) GetSyncBuf(accountID string) (string, error) {
	return f.buf, nil
}

func (f *fakeCursorStore) SaveSyncBuf(accountID, buf string) error {
	f.buf = buf
	return nil
}

type staticTokenSource struct {
	token string
}

func (s staticTokenSource) Token(ctx context.Context) (string, error) {
	if s.token == "" {
		return "token", nil
	}
	return s.token, nil
}

func TestParseAccountNewFlagsAndConfigDefaults(t *testing.T) {
	values, err := ParseAccountNewFlags([]string{
		"--name", "reviewer",
		"--app-id", "123",
		"--installation-id", "456",
		"--private-key-path", "/tmp/app.pem",
		"--repo", "owner/repo",
		"--repo", "owner/repo",
		"--poll-interval", "5m",
	}, strings.NewReader(""), ioDiscard{})
	if err != nil {
		t.Fatalf("ParseAccountNewFlags returned error: %v", err)
	}
	if values.Name != "reviewer" || values.PollInterval != 5*time.Minute {
		t.Fatalf("values = %#v, want reviewer with 5m poll interval", values)
	}
	if len(values.Repositories) != 1 || values.Repositories[0] != "owner/repo" {
		t.Fatalf("repositories = %#v, want one normalized repo", values.Repositories)
	}

	cfg := config.DefaultConfig()
	platformCtx, err := core.NewPlatformContext(store.PlatformGitHub, &cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewPlatformContext returned error: %v", err)
	}
	if err := UpsertAccountConfig(platformCtx, values.Name, AccountConfig{
		AppID:          values.AppID,
		InstallationID: values.InstallationID,
		PrivateKeyPath: values.PrivateKeyPath,
		Repositories:   values.Repositories,
	}); err != nil {
		t.Fatalf("UpsertAccountConfig returned error: %v", err)
	}
	account, ok, err := ResolveAccountConfig(platformCtx, "reviewer")
	if err != nil {
		t.Fatalf("ResolveAccountConfig returned error: %v", err)
	}
	if !ok {
		t.Fatal("ResolveAccountConfig did not find account")
	}
	if account.BaseURL != DefaultBaseURL || account.WebURL != DefaultWebURL || account.PollInterval.Duration != DefaultPollInterval {
		t.Fatalf("defaults = base:%s web:%s poll:%s", account.BaseURL, account.WebURL, account.PollInterval.Duration)
	}
	if account.MCP.Command != "" || len(account.MCP.Args) != 0 {
		t.Fatalf("mcp defaults = %#v, want no command or args", account.MCP)
	}
	if err := validateAccountRuntime("reviewer", account); err == nil || !strings.Contains(err.Error(), "mcp.command") {
		t.Fatalf("validateAccountRuntime error = %v, want missing mcp.command", err)
	}
}

func TestMakeAppJWTAndInstallationTokenCache(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("GenerateKey returned error: %v", err)
	}
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	jwt, err := makeAppJWT("123", key, now)
	if err != nil {
		t.Fatalf("makeAppJWT returned error: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d, want 3", len(parts))
	}
	rawClaims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims returned error: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(rawClaims, &claims); err != nil {
		t.Fatalf("unmarshal claims returned error: %v", err)
	}
	if claims["iss"] != "123" || int64(claims["iat"].(float64)) != now.Add(-appJWTBackdate).Unix() || int64(claims["exp"].(float64)) != now.Add(appJWTLifetime).Unix() {
		t.Fatalf("claims = %#v", claims)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if parsed, err := parsePrivateKeyPEM(keyPEM); err != nil || parsed.N.Cmp(key.N) != 0 {
		t.Fatalf("parsePrivateKeyPEM = key:%v err:%v", parsed, err)
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/app/installations/456/access_tokens" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_token",
			"expires_at": now.Add(time.Hour).Format(time.RFC3339),
		})
	}))
	defer server.Close()

	source := newAppTokenSource("123", "456", server.URL, key, server.Client())
	source.now = func() time.Time { return now }
	if token, err := source.Token(context.Background()); err != nil || token != "ghs_token" {
		t.Fatalf("Token first = %q err=%v", token, err)
	}
	if token, err := source.Token(context.Background()); err != nil || token != "ghs_token" {
		t.Fatalf("Token second = %q err=%v", token, err)
	}
	if calls != 1 {
		t.Fatalf("calls after cached token = %d, want 1", calls)
	}
	source.now = func() time.Time { return now.Add(time.Hour - tokenRefreshBefore + time.Second) }
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatalf("Token refresh returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls after refresh = %d, want 2", calls)
	}
}

func TestReviewInstructionsReadOrder(t *testing.T) {
	pr := testPullRequest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/base/repo/contents/.github/review_instructions.md" {
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("base instructions"))})
			return
		}
		t.Fatalf("unexpected request %s", r.URL.String())
	}))
	defer server.Close()
	client := newGitHubClient(server.URL, staticTokenSource{}, server.Client())
	instructions, ok, err := client.ReviewInstructions(context.Background(), pr)
	if err != nil || !ok || instructions.Text != "base instructions" {
		t.Fatalf("base instructions = %#v ok=%t err=%v", instructions, ok, err)
	}

	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/base/repo/contents/.github/review_instructions.md":
			http.NotFound(w, r)
		case "/repos/head/repo/contents/.github/review_instructions.md":
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("head instructions"))})
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	})
	instructions, ok, err = client.ReviewInstructions(context.Background(), pr)
	if err != nil || !ok || instructions.Text != "head instructions" {
		t.Fatalf("head fallback = %#v ok=%t err=%v", instructions, ok, err)
	}
}

func TestCursorDecisions(t *testing.T) {
	pr := testPullRequest()
	state := cursorState{PRs: map[string]cursorEntry{}}
	if !shouldProcessCursor(state, pr) {
		t.Fatal("new PR should process")
	}
	state = markCursor(state, pr, cursorStatusMissingInstructions, time.Unix(0, 0))
	if shouldProcessCursor(state, pr) {
		t.Fatal("missing instructions same SHA should not process")
	}
	pr.Head.SHA = "new-head"
	if !shouldProcessCursor(state, pr) {
		t.Fatal("changed head SHA should process")
	}
}

func TestMCPGuardAllowsCommentReviewAndRejectsUnsafeCalls(t *testing.T) {
	pr := testPullRequest()
	state := &reviewGuardState{}
	submit := &fakeTool{spec: tooltypes.Spec{Name: "mcp_github_pull_request_review_write"}}
	guarded := guardReviewTools(context.Background(), []tooltypes.Tool{submit}, pr, state)
	if len(guarded) != 1 {
		t.Fatalf("guarded tools = %d, want 1", len(guarded))
	}
	result := guarded[0].Execute(context.Background(), tooltypes.Call{
		ID:   "1",
		Name: "mcp_github_pull_request_review_write",
		Arguments: json.RawMessage(`{
			"owner":"base","repo":"repo","pullNumber":7,
			"method":"submit_pending","event":"COMMENT","body":"done"
		}`),
	})
	if result.IsError || !state.SubmittedComment {
		t.Fatalf("submit result = %#v submitted=%t", result, state.SubmittedComment)
	}

	state.SubmittedComment = false
	result = guarded[0].Execute(context.Background(), tooltypes.Call{
		ID:   "2",
		Name: "mcp_github_pull_request_review_write",
		Arguments: json.RawMessage(`{
			"owner":"base","repo":"repo","pullNumber":7,
			"method":"submit_pending","event":"APPROVE","body":"done"
		}`),
	})
	if !result.IsError || state.SubmittedComment {
		t.Fatalf("approve result = %#v submitted=%t, want rejected", result, state.SubmittedComment)
	}

	read := &fakeTool{spec: tooltypes.Spec{Name: "mcp_github_get_file_contents"}}
	guarded = guardReviewTools(context.Background(), []tooltypes.Tool{read}, pr, state)
	result = guarded[0].Execute(context.Background(), tooltypes.Call{
		ID:        "3",
		Name:      "mcp_github_get_file_contents",
		Arguments: json.RawMessage(`{"owner":"base","repo":"repo","path":"README.md"}`),
	})
	if result.IsError {
		t.Fatalf("file read result = %#v", result)
	}
	if got := read.lastArgs["sha"]; got != pr.Head.SHA {
		t.Fatalf("default sha = %#v, want head SHA", got)
	}

	result = guarded[0].Execute(context.Background(), tooltypes.Call{
		ID:        "4",
		Name:      "mcp_github_get_file_contents",
		Arguments: json.RawMessage(`{"owner":"evil","repo":"repo","path":"README.md"}`),
	})
	if !result.IsError {
		t.Fatalf("cross repo result = %#v, want error", result)
	}
}

func TestPollOnceMarksMissingInstructionsAndSkipsDraft(t *testing.T) {
	pr := testPullRequest()
	draft := pr
	draft.Number = 8
	draft.Draft = true
	st := &fakeCursorStore{}
	p := &Platform{
		account: store.Account{ID: "github:456", Name: "reviewer", Platform: store.PlatformGitHub},
		store:   st,
		now:     func() time.Time { return time.Unix(10, 0) },
	}
	client := &fakeAPIClient{prs: []PullRequest{draft, pr}}
	err := p.pollOnce(context.Background(), fakeHandler{}, AccountConfig{Repositories: []string{"base/repo"}}, client, staticTokenSource{})
	if err != nil {
		t.Fatalf("pollOnce returned error: %v", err)
	}
	state, err := loadCursor(st, "github:456")
	if err != nil {
		t.Fatalf("loadCursor returned error: %v", err)
	}
	if len(state.PRs) != 1 {
		t.Fatalf("cursor entries = %#v, want one non-draft entry", state.PRs)
	}
	entry := state.PRs[cursorKey(pr)]
	if entry.Status != cursorStatusMissingInstructions || entry.HeadSHA != pr.Head.SHA {
		t.Fatalf("entry = %#v, want missing instructions for head SHA", entry)
	}
	if client.instructionsCalls != 1 {
		t.Fatalf("instructionsCalls = %d, want 1", client.instructionsCalls)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

type fakeTool struct {
	spec     tooltypes.Spec
	lastArgs map[string]any
}

func (f *fakeTool) Spec() tooltypes.Spec { return f.spec }

func (f *fakeTool) Execute(ctx context.Context, call tooltypes.Call) tooltypes.Result {
	_ = json.Unmarshal(call.Arguments, &f.lastArgs)
	return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: "ok"}
}

type fakeAPIClient struct {
	prs               []PullRequest
	instructions      ReviewInstructions
	instructionsOK    bool
	instructionsCalls int
}

func (f *fakeAPIClient) ListOpenPullRequests(ctx context.Context, repo Repository) ([]PullRequest, error) {
	return f.prs, nil
}

func (f *fakeAPIClient) ReviewInstructions(ctx context.Context, pr PullRequest) (ReviewInstructions, bool, error) {
	f.instructionsCalls++
	return f.instructions, f.instructionsOK, nil
}

type fakeHandler struct{}

func (fakeHandler) Handle(ctx context.Context, msg core.InboundMessage, sender core.Sender) error {
	return nil
}

func testPullRequest() PullRequest {
	return PullRequest{
		Number:  7,
		Title:   "Add feature",
		Body:    "body",
		HTMLURL: "https://github.com/base/repo/pull/7",
		Head: PullRequestRef{
			SHA:  "head-sha",
			Ref:  "feature",
			Repo: Repository{Owner: "head", Name: "repo"},
		},
		Base: PullRequestRef{
			SHA:  "base-sha",
			Ref:  "main",
			Repo: Repository{Owner: "base", Name: "repo"},
		},
	}
}
