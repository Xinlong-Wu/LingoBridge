package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tooltypes "lingobridge/internal/tools"
)

const githubMCPToolPrefix = "mcp_github_"

var allowedReviewRemoteTools = map[string]bool{
	"pull_request_read":             true,
	"get_file_contents":             true,
	"pull_request_review_write":     true,
	"add_comment_to_pending_review": true,
}

type reviewGuardState struct {
	SubmittedComment bool
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
	return t.inner.Spec()
}

func (t guardedTool) Execute(ctx context.Context, call tooltypes.Call) tooltypes.Result {
	args, err := parseToolArgs(call.Arguments)
	if err != nil {
		return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: err.Error(), IsError: true}
	}
	if err := t.validateAndMutate(args); err != nil {
		return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: err.Error(), IsError: true}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return tooltypes.Result{CallID: call.ID, Name: call.Name, Content: fmt.Sprintf("marshal guarded tool args: %v", err), IsError: true}
	}
	call.Arguments = data
	result := t.inner.Execute(ctx, call)
	if t.remote == "pull_request_review_write" && isCommentSubmit(args) && !result.IsError && t.state != nil {
		t.state.SubmittedComment = true
	}
	return result
}

func (t guardedTool) validateAndMutate(args map[string]any) error {
	switch t.remote {
	case "pull_request_read":
		return validateBasePRArgs(args, t.pr)
	case "get_file_contents":
		return validateFileContentsArgs(args, t.pr)
	case "pull_request_review_write":
		return validateReviewWriteArgs(args, t.pr)
	case "add_comment_to_pending_review":
		if err := validateBasePRArgs(args, t.pr); err != nil {
			return err
		}
		return validateReviewCommentArgs(args)
	default:
		return fmt.Errorf("github mcp tool %q is not allowed for automated PR review", t.remote)
	}
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
	case hasSHA:
		if !allowedReviewSHA(sha, pr) {
			return fmt.Errorf("get_file_contents sha must be current PR base or head SHA")
		}
	case hasRef:
		if !allowedReviewSHA(ref, pr) {
			return fmt.Errorf("get_file_contents ref must be current PR base or head SHA")
		}
	default:
		args["sha"] = pr.Head.SHA
		delete(args, "ref")
	}
	return nil
}

func validateReviewWriteArgs(args map[string]any, pr PullRequest) error {
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
		if event, ok := stringArg(args, "event"); ok && strings.TrimSpace(event) != "" {
			return fmt.Errorf("pull_request_review_write create must create a pending review without event")
		}
		if commitID, ok := stringArg(args, "commitID"); ok && strings.TrimSpace(commitID) != strings.TrimSpace(pr.Head.SHA) {
			return fmt.Errorf("pull_request_review_write commitID must be current PR head SHA")
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
	return nil
}

func isCommentSubmit(args map[string]any) bool {
	method, _ := stringArg(args, "method")
	event, _ := stringArg(args, "event")
	return strings.TrimSpace(method) == "submit_pending" && strings.TrimSpace(event) == "COMMENT"
}

func allowedReviewSHA(value string, pr PullRequest) bool {
	value = strings.TrimSpace(value)
	return value != "" && (value == strings.TrimSpace(pr.Base.SHA) || value == strings.TrimSpace(pr.Head.SHA))
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

func sameRepo(left, right Repository) bool {
	return strings.EqualFold(strings.TrimSpace(left.Owner), strings.TrimSpace(right.Owner)) &&
		strings.EqualFold(strings.TrimSpace(left.Name), strings.TrimSpace(right.Name))
}
