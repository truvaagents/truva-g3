package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	githubAcceptHeader  = "application/vnd.github+json"
	githubAPIVersion    = "2022-11-28"
	maxErrBodyBytes     = 4 * 1024
	defaultPageSize     = 100
	errBodyReadTruncate = 256
)

type GitHubClient struct {
	HTTPClient *http.Client
	BaseURL    string
	Token      string
}

type GitHubClientConfig struct {
	Token   string
	BaseURL string
}

func NewGitHubClient(httpClient *http.Client, cfg GitHubClientConfig) *GitHubClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.github.com"
	}
	return &GitHubClient{
		HTTPClient: httpClient,
		BaseURL:    cfg.BaseURL,
		Token:      cfg.Token,
	}
}

func (c *GitHubClient) Configured() bool { return c.Token != "" }

// do executes a GitHub REST request. Error messages include the upstream
// status code so core.ClassifyUpstreamError can categorize them downstream.
func (c *GitHubClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", githubAcceptHeader)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("github request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw := readSmallBody(resp.Body)
		return fmt.Errorf("github API error status %d: %s", resp.StatusCode, raw)
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// readSmallBody returns up to maxErrBodyBytes of the body, truncated for log
// safety. Used only for error messages — never for happy-path decoding.
func readSmallBody(r io.Reader) string {
	buf, err := io.ReadAll(io.LimitReader(r, maxErrBodyBytes))
	if err != nil {
		return "<unreadable body>"
	}
	s := string(buf)
	if len(s) > errBodyReadTruncate {
		s = s[:errBodyReadTruncate] + "..."
	}
	return s
}

// --- High-level API methods ---

func (c *GitHubClient) GetPullRequest(ctx context.Context, owner, repo string, number int) (*githubPullRequest, error) {
	var pr githubPullRequest
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

func (c *GitHubClient) ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]githubFile, error) {
	var all []githubFile
	page := 1
	for {
		var batch []githubFile
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=%d&page=%d",
			owner, repo, number, defaultPageSize, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < defaultPageSize {
			break
		}
		page++
	}
	return all, nil
}

// ListExistingComments returns review comments and issue comments combined.
// Errors from one source don't block the other — partial results are useful.
func (c *GitHubClient) ListExistingComments(ctx context.Context, owner, repo string, number int) ([]ExistingComment, error) {
	out := []ExistingComment{}

	// Review comments (inline, on diff lines)
	page := 1
	for {
		var batch []githubReviewComment
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=%d&page=%d",
			owner, repo, number, defaultPageSize, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return out, err
		}
		for _, c := range batch {
			out = append(out, ExistingComment{
				ID:   c.ID,
				Path: c.Path,
				Line: c.Line,
				Body: c.Body,
				User: c.User.Login,
			})
		}
		if len(batch) < defaultPageSize {
			break
		}
		page++
	}

	// Issue comments (top-level, no path/line)
	page = 1
	for {
		var batch []githubIssueComment
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d&page=%d",
			owner, repo, number, defaultPageSize, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return out, err
		}
		for _, c := range batch {
			out = append(out, ExistingComment{
				ID:   c.ID,
				Body: c.Body,
				User: c.User.Login,
			})
		}
		if len(batch) < defaultPageSize {
			break
		}
		page++
	}

	return out, nil
}

func (c *GitHubClient) FetchFileAtRef(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	// Use the contents API with media type that returns raw bytes.
	url := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw := readSmallBody(resp.Body)
		return nil, fmt.Errorf("github API error status %d: %s", resp.StatusCode, raw)
	}
	return io.ReadAll(resp.Body)
}

func (c *GitHubClient) CreatePullRequestReview(ctx context.Context, req CreatePRReviewRequest) (*githubReviewResponse, error) {
	body := map[string]any{
		"commit_id": req.CommitID,
		"event":     req.Event,
		"body":      req.Body,
	}
	if len(req.Comments) > 0 {
		body["comments"] = req.Comments
	}
	var resp githubReviewResponse
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", req.Owner, req.Repo, req.PullNumber)
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *GitHubClient) CreateIssueComment(ctx context.Context, req CreateIssueCommentRequest) (*githubIssueCommentResponse, error) {
	body := map[string]any{"body": req.Body}
	var resp githubIssueCommentResponse
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", req.Owner, req.Repo, req.PullNumber)
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
