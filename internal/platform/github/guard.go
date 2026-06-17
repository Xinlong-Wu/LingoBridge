package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tooltypes "lingobridge/internal/tools"
)

const (
	githubMCPToolPrefix = "mcp_github_"
	reviewLogTextLimit  = 500
)

var allowedReviewRemoteTools = map[string]bool{
	"pull_request_read":             true,
	"get_file_contents":             true,
	"pull_request_review_write":     true,
	"add_comment_to_pending_review": true,
}

var allowedReviewReadMethods = map[string]bool{
	"get":            true,
	"get_diff":       true,
	"get_files":      true,
	"get_status":     true,
	"get_check_runs": true,
}

var allowedReviewReadMethodList = []string{"get", "get_diff", "get_files", "get_status", "get_check_runs"}

type reviewGuardState struct {
	SubmittedComment        bool
	WriteAttempted          bool
	PendingReviewCreated    bool
	InlineCommentsAttempted int
	InlineCommentsAdded     int
	SubmitAttempted         bool
}

type guardedTool struct {
	inner  tooltypes.Tool
	remote string
	pr     PullRequest
	state  *reviewGuardState
}

func guardReviewTools(ctx context.Context, tools []tooltypes.Tool, pr PullRequest, state *reviewGuardState) []tooltypes.Tool {
	out := make([]tooltypes.Tool, 0, len(tools))
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
		remote, ok := githubRemoteToolName(spec.Name)
		if !ok {
			githubLog.Warn(ctx, "skipping non-github mcp tool name=%s", spec.Name)
			continue
		}
		if !allowedReviewRemoteTools[remote] {
			githubLog.Warn(ctx, "skipping disallowed github mcp tool remote=%s", remote)
			continue
		}
		if seen[spec.Name] {
			githubLog.Warn(ctx, "skipping duplicate guarded github mcp tool name=%s", spec.Name)
			continue
		}
		seen[spec.Name] = true
		out = append(out, guardedTool{inner: tool, remote: remote, pr: pr, state: state})
	}
	return out
}

func githubRemoteToolName(exposed string) (string, bool) {
	exposed = strings.TrimSpace(exposed)
	if !strings.HasPrefix(exposed, githubMCPToolPrefix) {
		return "", false
	}
	remote := strings.TrimPrefix(exposed, githubMCPToolPrefix)
	return remote, remote != ""
}

func (t guardedTool) Spec() tooltypes.Spec {
	spec := t.inner.Spec()
	switch t.remote {
	case "pull_request_read":
		spec.Parameters = restrictPullRequestReadSchema(spec.Parameters)
	case "get_file_contents":
		spec.Description = appendGetFileContentsGuardDescription(spec.Description, t.pr)
	case "pull_request_review_write":
		spec.Parameters = restrictReviewWriteSchema(spec.Parameters)
	}
	return spec
}

func (t guardedTool) Execute(ctx context.Context, call tooltypes.Call) tooltypes.Result {
	args, err := parseToolArgs(call.Arguments)
	if err != nil {
		return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: err.Error(), IsError: true}
	}
	if err := t.validateAndMutate(ctx, args); err != nil {
		return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: err.Error(), IsError: true}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: fmt.Sprintf("marshal guarded tool args: %v", err), IsError: true}
	}
	call.Arguments = data
	t.recordWriteAttempt(args)
	result := t.inner.Execute(ctx, call)
	t.recordWriteResult(ctx, call, args, result)
	return result
}

func (t guardedTool) validateAndMutate(ctx context.Context, args map[string]any) error {
	switch t.remote {
	case "pull_request_read":
		return validatePullRequestReadArgs(args, t.pr)
	case "get_file_contents":
		return validateFileContentsArgs(args, t.pr)
	case "pull_request_review_write":
		return validateReviewWriteArgs(ctx, args, t.pr)
	case "add_comment_to_pending_review":
		if err := validateBasePRArgs(args, t.pr); err != nil {
			return err
		}
		return validateReviewCommentArgs(args)
	default:
		return fmt.Errorf("github mcp tool %q is not allowed for automated PR review", t.remote)
	}
}

func (t guardedTool) recordWriteAttempt(args map[string]any) {
	if t.state == nil {
		return
	}
	switch t.remote {
	case "pull_request_review_write":
		t.state.WriteAttempted = true
		if isCommentSubmit(args) {
			t.state.SubmitAttempted = true
		}
	case "add_comment_to_pending_review":
		t.state.WriteAttempted = true
		t.state.InlineCommentsAttempted++
	}
}

func (t guardedTool) recordWriteResult(ctx context.Context, call tooltypes.Call, args map[string]any, result tooltypes.Result) {
	switch t.remote {
	case "pull_request_review_write":
		t.recordReviewWriteResult(ctx, call, args, result)
	case "add_comment_to_pending_review":
		t.recordReviewCommentResult(ctx, call, args, result)
	}
}

func (t guardedTool) recordReviewWriteResult(ctx context.Context, call tooltypes.Call, args map[string]any, result tooltypes.Result) {
	method, _ := stringArg(args, "method")
	body, _ := stringArg(args, "body")
	bodyChars := reviewLogTextChars(body)
	switch strings.TrimSpace(method) {
	case "create":
		if result.IsError {
			githubLog.Warn(ctx, "github pending review create failed repo=%s number=%d head=%s call_id=%s error=%s", t.pr.Base.Repo.FullName(), t.pr.Number, shortSHA(t.pr.Head.SHA), call.ID, summarizeReviewLogText(result.Content, reviewLogTextLimit))
			return
		}
		if t.state != nil {
			t.state.PendingReviewCreated = true
		}
		githubLog.Debug(ctx, "github pending review created repo=%s number=%d head=%s call_id=%s", t.pr.Base.Repo.FullName(), t.pr.Number, shortSHA(t.pr.Head.SHA), call.ID)
	case "submit_pending":
		if result.IsError {
			githubLog.Warn(ctx, "github pending review submit failed repo=%s number=%d head=%s call_id=%s body_chars=%d error=%s", t.pr.Base.Repo.FullName(), t.pr.Number, shortSHA(t.pr.Head.SHA), call.ID, bodyChars, summarizeReviewLogText(result.Content, reviewLogTextLimit))
			return
		}
		if t.state != nil && isCommentSubmit(args) {
			t.state.SubmittedComment = true
		}
		githubLog.Debug(ctx, "github pending review submitted repo=%s number=%d head=%s call_id=%s body_chars=%d", t.pr.Base.Repo.FullName(), t.pr.Number, shortSHA(t.pr.Head.SHA), call.ID, bodyChars)
	}
}

func (t guardedTool) recordReviewCommentResult(ctx context.Context, call tooltypes.Call, args map[string]any, result tooltypes.Result) {
	path, _ := stringArg(args, "path")
	subjectType, _ := stringArg(args, "subjectType")
	body, _ := stringArg(args, "body")
	bodyChars := reviewLogTextChars(body)
	line := reviewIntArgLogValue(args, "line")
	startLine := reviewIntArgLogValue(args, "startLine")
	side, _ := stringArg(args, "side")
	startSide, _ := stringArg(args, "startSide")
	if result.IsError {
		githubLog.Warn(ctx, "github pending review comment failed repo=%s number=%d head=%s call_id=%s path=%s subject_type=%s start_line=%s line=%s start_side=%s side=%s body_chars=%d error=%s", t.pr.Base.Repo.FullName(), t.pr.Number, shortSHA(t.pr.Head.SHA), call.ID, path, subjectType, startLine, line, startSide, side, bodyChars, summarizeReviewLogText(result.Content, reviewLogTextLimit))
		return
	}
	if t.state != nil {
		t.state.InlineCommentsAdded++
	}
	githubLog.Debug(ctx, "github pending review comment added repo=%s number=%d head=%s call_id=%s path=%s subject_type=%s start_line=%s line=%s start_side=%s side=%s body_chars=%d", t.pr.Base.Repo.FullName(), t.pr.Number, shortSHA(t.pr.Head.SHA), call.ID, path, subjectType, startLine, line, startSide, side, bodyChars)
}

func parseToolArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func validateBasePRArgs(args map[string]any, pr PullRequest) error {
	owner, ok := stringArg(args, "owner")
	if !ok {
		return fmt.Errorf("owner is required")
	}
	repo, ok := stringArg(args, "repo")
	if !ok {
		return fmt.Errorf("repo is required")
	}
	pullNumber, ok := intArg(args, "pullNumber")
	if !ok {
		return fmt.Errorf("pullNumber is required")
	}
	if !sameRepo(Repository{Owner: owner, Name: repo}, pr.Base.Repo) || pullNumber != pr.Number {
		return fmt.Errorf("tool call must target current PR %s#%d", pr.Base.Repo.FullName(), pr.Number)
	}
	return nil
}

func validatePullRequestReadArgs(args map[string]any, pr PullRequest) error {
	if err := validateBasePRArgs(args, pr); err != nil {
		return err
	}
	method, ok := stringArg(args, "method")
	if !ok {
		return fmt.Errorf("method is required")
	}
	method = strings.TrimSpace(method)
	if !allowedReviewReadMethods[method] {
		return fmt.Errorf("pull_request_read method %q is not allowed for automated PR review", method)
	}
	return nil
}

func validateFileContentsArgs(args map[string]any, pr PullRequest) error {
	owner, ok := stringArg(args, "owner")
	if !ok {
		return fmt.Errorf("owner is required")
	}
	repo, ok := stringArg(args, "repo")
	if !ok {
		return fmt.Errorf("repo is required")
	}
	target := Repository{Owner: owner, Name: repo}
	if !sameRepo(target, pr.Base.Repo) && !sameRepo(target, pr.Head.Repo) {
		return fmt.Errorf("get_file_contents may only target current PR base/head repositories")
	}

	sha, hasSHA := stringArg(args, "sha")
	ref, hasRef := stringArg(args, "ref")
	switch {
	case hasSHA && hasRef:
		return fmt.Errorf("get_file_contents must not include both sha and ref")
	case hasSHA:
		if !allowedReviewSHA(sha, pr) {
			return fmt.Errorf("get_file_contents sha must be current PR base or head SHA")
		}
	case hasRef:
		if !allowedReviewRef(ref, target, pr) {
			return fmt.Errorf("get_file_contents ref must be current PR base/head branch ref, refs/pull/%d/head, or current PR base/head SHA", pr.Number)
		}
	default:
		args["sha"] = pr.Head.SHA
		delete(args, "ref")
	}
	return nil
}

func validateReviewWriteArgs(ctx context.Context, args map[string]any, pr PullRequest) error {
	if err := validateBasePRArgs(args, pr); err != nil {
		return err
	}
	method, ok := stringArg(args, "method")
	if !ok {
		return fmt.Errorf("method is required")
	}
	method = strings.TrimSpace(method)
	switch method {
	case "create":
		_, hadEvent := args["event"]
		body, _ := stringArg(args, "body")
		_, hadRawBody := args["body"]
		bodyChars := reviewLogTextChars(body)
		delete(args, "event")
		delete(args, "body")

		expectedCommitID := strings.TrimSpace(pr.Head.SHA)
		commitID, hasCommitID := stringArg(args, "commitID")
		if hasCommitID && strings.TrimSpace(commitID) != expectedCommitID {
			return fmt.Errorf("pull_request_review_write commitID must be current PR head SHA")
		}
		injectedCommitID := !hasCommitID
		if injectedCommitID {
			args["commitID"] = expectedCommitID
		}
		droppedExtraArgs := keepReviewCreateArgs(args)
		if hadEvent || hadRawBody || injectedCommitID || droppedExtraArgs > 0 {
			githubLog.Warn(ctx, "normalized create review repo=%s number=%d head=%s dropped_event=%t dropped_body=%t dropped_extra_args=%d injected_commit_id=%t body_chars=%d", pr.Base.Repo.FullName(), pr.Number, shortSHA(pr.Head.SHA), hadEvent, hadRawBody, droppedExtraArgs, injectedCommitID, bodyChars)
		}
		return nil
	case "submit_pending":
		event, ok := stringArg(args, "event")
		if !ok || strings.TrimSpace(event) != "COMMENT" {
			return fmt.Errorf("pull_request_review_write submit_pending is only allowed with event=COMMENT")
		}
		return nil
	default:
		return fmt.Errorf("pull_request_review_write method %q is not allowed", method)
	}
}

func validateReviewCommentArgs(args map[string]any) error {
	path, ok := stringArg(args, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is required")
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") || strings.Contains(path, "..") {
		return fmt.Errorf("path must be a relative repository path")
	}
	body, ok := stringArg(args, "body")
	if !ok || strings.TrimSpace(body) == "" {
		return fmt.Errorf("body is required")
	}
	subjectType, ok := stringArg(args, "subjectType")
	if !ok {
		return fmt.Errorf("subjectType is required")
	}
	switch subjectType {
	case "FILE":
		if _, hasStartLine := intArg(args, "startLine"); hasStartLine {
			return fmt.Errorf("startLine is only allowed for LINE comments")
		}
		if _, hasStartSide := stringArg(args, "startSide"); hasStartSide {
			return fmt.Errorf("startSide is only allowed for LINE comments")
		}
		return nil
	case "LINE":
		if _, ok := intArg(args, "line"); !ok {
			return fmt.Errorf("line is required for LINE comments")
		}
		side, ok := stringArg(args, "side")
		if !ok || !validReviewCommentSide(side) {
			return fmt.Errorf("side must be LEFT or RIGHT for LINE comments")
		}
		_, hasStartLine := intArg(args, "startLine")
		startSide, hasStartSide := stringArg(args, "startSide")
		if hasStartLine != hasStartSide {
			return fmt.Errorf("startLine and startSide must be provided together for multi-line comments")
		}
		if hasStartSide && !validReviewCommentSide(startSide) {
			return fmt.Errorf("startSide must be LEFT or RIGHT")
		}
		return nil
	default:
		return fmt.Errorf("subjectType must be FILE or LINE")
	}
}

func validReviewCommentSide(value string) bool {
	value = strings.TrimSpace(value)
	return value == "LEFT" || value == "RIGHT"
}

func isCommentSubmit(args map[string]any) bool {
	method, _ := stringArg(args, "method")
	event, _ := stringArg(args, "event")
	return strings.TrimSpace(method) == "submit_pending" && strings.TrimSpace(event) == "COMMENT"
}

func keepReviewCreateArgs(args map[string]any) int {
	dropped := 0
	for key := range args {
		switch key {
		case "owner", "repo", "pullNumber", "method", "commitID":
			continue
		default:
			delete(args, key)
			dropped++
		}
	}
	return dropped
}

func allowedReviewSHA(value string, pr PullRequest) bool {
	value = strings.TrimSpace(value)
	return value != "" && (value == strings.TrimSpace(pr.Base.SHA) || value == strings.TrimSpace(pr.Head.SHA))
}

func allowedReviewRef(value string, target Repository, pr PullRequest) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if allowedReviewSHA(value, pr) {
		return true
	}
	if sameRepo(target, pr.Base.Repo) && branchRefMatches(value, pr.Base.Ref) {
		return true
	}
	if sameRepo(target, pr.Head.Repo) && branchRefMatches(value, pr.Head.Ref) {
		return true
	}
	return sameRepo(target, pr.Base.Repo) && value == fmt.Sprintf("refs/pull/%d/head", pr.Number)
}

func branchRefMatches(value, branch string) bool {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false
	}
	return value == branch || value == "refs/heads/"+branch
}

func appendGetFileContentsGuardDescription(description string, pr PullRequest) string {
	description = strings.TrimSpace(description)
	var b strings.Builder
	if description != "" {
		b.WriteString(description)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "LingoBridge GitHub PR review guard: owner/repo must be the current PR base repo (%s) or head repo (%s). ",
		pr.Base.Repo.FullName(), pr.Head.Repo.FullName())
	fmt.Fprintf(&b, "sha may only be the current base SHA (%s) or head SHA (%s). ", pr.Base.SHA, pr.Head.SHA)
	fmt.Fprintf(&b, "ref may only be the matching current PR branch ref (base %q or head %q), refs/heads/<that branch>, refs/pull/%d/head on the base repo, or one of those SHAs. ",
		pr.Base.Ref, pr.Head.Ref, pr.Number)
	b.WriteString("If neither sha nor ref is provided, the guard defaults to the current head SHA.")
	return b.String()
}

func restrictPullRequestReadSchema(raw json.RawMessage) json.RawMessage {
	return restrictSchema(raw, map[string]schemaPropertyPatch{
		"method": {
			Enum:        allowedReviewReadMethodList,
			Description: "Pull request read operation allowed by LingoBridge automated review. Comments, commits, review comments, and historical reviews are not allowed.",
		},
	})
}

func restrictReviewWriteSchema(raw json.RawMessage) json.RawMessage {
	return restrictSchema(raw, map[string]schemaPropertyPatch{
		"method": {
			Enum:        []string{"create", "submit_pending"},
			Description: "Review operation allowed by LingoBridge. Use create without event to create a pending review, then submit_pending with event=COMMENT.",
		},
		"event": {
			Enum:        []string{"COMMENT"},
			Description: "Omit for method=create to create a pending review; set COMMENT only for method=submit_pending.",
		},
	})
}

type schemaPropertyPatch struct {
	Enum        []string
	Description string
}

func restrictSchema(raw json.RawMessage, patches map[string]schemaPropertyPatch) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return raw
	}
	for name, patch := range patches {
		property, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		if len(patch.Enum) > 0 {
			values := make([]any, 0, len(patch.Enum))
			for _, value := range patch.Enum {
				values = append(values, value)
			}
			property["enum"] = values
		}
		if patch.Description != "" {
			property["description"] = patch.Description
		}
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return raw
	}
	return data
}

func stringArg(args map[string]any, key string) (string, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", false
	}
	switch v := value.(type) {
	case string:
		v = strings.TrimSpace(v)
		return v, v != ""
	default:
		return "", false
	}
}

func intArg(args map[string]any, key string) (int, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		n := int(v)
		return n, float64(n) == v
	case int:
		return v, true
	case int64:
		return int(v), int64(int(v)) == v
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), int64(int(n)) == n
	default:
		return 0, false
	}
}

func reviewIntArgLogValue(args map[string]any, key string) string {
	value, ok := intArg(args, key)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d", value)
}

func summarizeReviewLogText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func reviewLogTextChars(text string) int {
	text = strings.Join(strings.Fields(text), " ")
	return len([]rune(text))
}

func sameRepo(left, right Repository) bool {
	return strings.EqualFold(strings.TrimSpace(left.Owner), strings.TrimSpace(right.Owner)) &&
		strings.EqualFold(strings.TrimSpace(left.Name), strings.TrimSpace(right.Name))
}
