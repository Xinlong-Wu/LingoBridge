package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrNotFound is returned when the GitHub API responds with 404.
// Use NotFoundError to access the response body for diagnostics.
var ErrNotFound = errors.New("github resource not found")

// NotFoundError wraps ErrNotFound with the response body from GitHub.
type NotFoundError struct {
	APIPath string
	Body    string
}

func (e *NotFoundError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("github resource not found: %s body=%s", e.APIPath, e.Body)
	}
	return fmt.Sprintf("github resource not found: %s", e.APIPath)
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

type apiClient interface {
	ListOpenPullRequests(ctx context.Context, repo Repository) ([]PullRequest, error)
	ReviewInstructions(ctx context.Context, pr PullRequest) (ReviewInstructions, bool, error)
}

type githubClient struct {
	baseURL     string
	httpClient  *http.Client
	tokenSource tokenSource
}

type PullRequest struct {
	Number  int
	Title   string
	Body    string
	HTMLURL string
	Draft   bool
	Head    PullRequestRef
	Base    PullRequestRef
}

type PullRequestRef struct {
	SHA  string
	Ref  string
	Repo Repository
}

type ReviewInstructions struct {
	Text   string
	Source string
}

func newGitHubClient(baseURL string, tokenSource tokenSource, httpClient *http.Client) *githubClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &githubClient{baseURL: baseURL, tokenSource: tokenSource, httpClient: httpClient}
}

func (c *githubClient) ListOpenPullRequests(ctx context.Context, repo Repository) ([]PullRequest, error) {
	var out []PullRequest
	for page := 1; ; page++ {
		var raws []rawPullRequest
		query := url.Values{}
		query.Set("state", "open")
		query.Set("per_page", "100")
		query.Set("page", strconv.Itoa(page))
		if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls", pathPart(repo.Owner), pathPart(repo.Name)), query, nil, &raws); err != nil {
			return nil, err
		}
		for _, raw := range raws {
			out = append(out, raw.toPullRequest())
		}
		if len(raws) < 100 {
			return out, nil
		}
	}
}

func (c *githubClient) ReviewInstructions(ctx context.Context, pr PullRequest) (ReviewInstructions, bool, error) {
	// Try the exact base SHA first (pinned to the PR's merge base).
	text, err := c.GetFileContents(ctx, pr.Base.Repo, reviewInstructionsPath, pr.Base.SHA)
	if err == nil {
		return ReviewInstructions{Text: text, Source: fmt.Sprintf("%s@%s:%s", pr.Base.Repo.FullName(), shortSHA(pr.Base.SHA), reviewInstructionsPath)}, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ReviewInstructions{}, false, fmt.Errorf("fetch %s/%s@%s:%s: %w", pr.Base.Repo.Owner, pr.Base.Repo.Name, shortSHA(pr.Base.SHA), reviewInstructionsPath, err)
	}
	// The file may have been added after the PR's base commit. Fall back to
	// the current tip of the base branch (e.g. main HEAD).
	if pr.Base.Ref != "" {
		text, err = c.GetFileContents(ctx, pr.Base.Repo, reviewInstructionsPath, pr.Base.Ref)
		if err == nil {
			return ReviewInstructions{Text: text, Source: fmt.Sprintf("%s@%s:%s", pr.Base.Repo.FullName(), pr.Base.Ref, reviewInstructionsPath)}, true, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return ReviewInstructions{}, false, fmt.Errorf("fetch %s/%s@%s:%s: %w", pr.Base.Repo.Owner, pr.Base.Repo.Name, pr.Base.Ref, reviewInstructionsPath, err)
		}
	}
	return ReviewInstructions{}, false, nil
}

func (c *githubClient) GetFileContents(ctx context.Context, repo Repository, filePath, ref string) (string, error) {
	query := url.Values{}
	if strings.TrimSpace(ref) != "" {
		query.Set("ref", strings.TrimSpace(ref))
	}
	var out struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/contents/%s", pathPart(repo.Owner), pathPart(repo.Name), path(filePath)), query, nil, &out)
	if err != nil {
		return "", err
	}
	if out.Type != "" && out.Type != "file" {
		return "", fmt.Errorf("github contents path %s is %s, not file", filePath, out.Type)
	}
	content := strings.ReplaceAll(out.Content, "\n", "")
	if out.Encoding == "base64" {
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return "", fmt.Errorf("decode github file content: %w", err)
		}
		return string(data), nil
	}
	return out.Content, nil
}

func (c *githubClient) doJSON(ctx context.Context, method, apiPath string, query url.Values, body io.Reader, out any) error {
	token, err := c.tokenSource.Token(ctx)
	if err != nil {
		return err
	}
	reqURL := c.baseURL + apiPath
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	// githubLog.Debug(ctx, "github request %s", req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github api %s %s: %w", method, apiPath, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read github api response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return &NotFoundError{APIPath: apiPath, Body: truncateForError(string(data))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github api %s %s: status=%d body=%s", method, apiPath, resp.StatusCode, truncateForError(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse github api response: %w", err)
	}
	return nil
}

type rawPullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	Head    rawPRRef
	Base    rawPRRef
}

type rawPRRef struct {
	SHA  string   `json:"sha"`
	Ref  string   `json:"ref"`
	Repo *rawRepo `json:"repo"`
}

type rawRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (r rawPullRequest) toPullRequest() PullRequest {
	return PullRequest{
		Number:  r.Number,
		Title:   r.Title,
		Body:    r.Body,
		HTMLURL: r.HTMLURL,
		Draft:   r.Draft,
		Head:    r.Head.toRef(),
		Base:    r.Base.toRef(),
	}
}

func (r rawPRRef) toRef() PullRequestRef {
	ref := PullRequestRef{SHA: strings.TrimSpace(r.SHA), Ref: strings.TrimSpace(r.Ref)}
	if r.Repo == nil {
		return ref
	}
	ref.Repo = Repository{Owner: strings.TrimSpace(r.Repo.Owner.Login), Name: strings.TrimSpace(r.Repo.Name)}
	if ref.Repo.Owner == "" || ref.Repo.Name == "" {
		if owner, name, ok := strings.Cut(strings.TrimSpace(r.Repo.FullName), "/"); ok {
			ref.Repo = Repository{Owner: owner, Name: name}
		}
	}
	return ref
}

func pathPart(value string) string {
	return url.PathEscape(strings.TrimSpace(value))
}

func path(value string) string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}
