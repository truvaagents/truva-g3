package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// JiraClient handles all HTTP communication with the JIRA Cloud REST API v3.
type JiraClient struct {
	baseURL    string // e.g. "https://mycompany.atlassian.net"
	authHeader string // "Basic base64(email:token)"
	httpClient *http.Client
}

// NewJiraClient creates a JIRA API client with traced HTTP for distributed tracing.
func NewJiraClient(baseURL, email, apiToken string) *JiraClient {
	auth := base64.StdEncoding.EncodeToString(
		[]byte(email + ":" + apiToken),
	)

	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	return &JiraClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		authHeader: "Basic " + auth,
		httpClient: tracedClient,
	}
}

// doRequest is the base method for all JIRA API calls.
// Uses NewRequestWithContext so TracedHTTPClient injects the traceparent header.
func (c *JiraClient) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	reqURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TruvaG3-JiraTool/1.0")

	return c.httpClient.Do(req)
}

// --- Response Structs ---

type IssueResponse struct {
	ID        string                 `json:"id"`
	Key       string                 `json:"key"`
	Self      string                 `json:"self"`
	Fields map[string]interface{} `json:"fields"`
}

type SearchResponse struct {
	Issues        []IssueResponse `json:"issues"`
	Total         int             `json:"total"`
	NextPageToken string          `json:"nextPageToken,omitempty"`
}

type CreateIssueResponse struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Self string `json:"self"`
}

type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
}

type CommentResponse struct {
	ID      string                 `json:"id"`
	Self    string                 `json:"self"`
	Body    map[string]interface{} `json:"body"`
	Author  map[string]interface{} `json:"author"`
	Created string                 `json:"created"`
}

type UserResponse struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	Active      bool   `json:"active"`
	AvatarURLs  map[string]string `json:"avatarUrls"`
	AccountType string `json:"accountType"`
}

type ProjectResponse struct {
	ID         string                   `json:"id"`
	Key        string                   `json:"key"`
	Name       string                   `json:"name"`
	Self       string                   `json:"self"`
	Lead       map[string]interface{}   `json:"lead"`
	IssueTypes []map[string]interface{} `json:"issueTypes"`
	Components []map[string]interface{} `json:"components"`
	Style      string                   `json:"style"`
}

type SprintResponse struct {
	ID            int    `json:"id"`
	Self          string `json:"self"`
	State         string `json:"state"`
	Name          string `json:"name"`
	StartDate     string `json:"startDate,omitempty"`
	EndDate       string `json:"endDate,omitempty"`
	CompleteDate  string `json:"completeDate,omitempty"`
	OriginBoardID int    `json:"originBoardId"`
	Goal          string `json:"goal,omitempty"`
}

type BoardResponse struct {
	ID       int    `json:"id"`
	Self     string `json:"self"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location struct {
		ProjectID   int    `json:"projectId"`
		ProjectKey  string `json:"projectKey"`
		ProjectName string `json:"projectName"`
	} `json:"location"`
}

type WorklogResponse struct {
	ID               string                 `json:"id"`
	Self             string                 `json:"self"`
	Author           map[string]interface{} `json:"author"`
	TimeSpent        string                 `json:"timeSpent"`
	TimeSpentSeconds int                    `json:"timeSpentSeconds"`
	Started          string                 `json:"started"`
	Created          string                 `json:"created"`
}

type IssueLinkTypeResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

type ChangelogResponse struct {
	Histories []ChangelogHistory `json:"histories"`
	Total     int                `json:"total"`
}

type ChangelogHistory struct {
	ID      string                 `json:"id"`
	Author  map[string]interface{} `json:"author"`
	Created string                 `json:"created"`
	Items   []ChangelogItem        `json:"items"`
}

type ChangelogItem struct {
	Field      string `json:"field"`
	FieldType  string `json:"fieldtype"`
	FromString string `json:"fromString"`
	ToString   string `json:"toString"`
}

// --- API Methods ---

// GetIssue fetches a single issue by key or ID.
func (c *JiraClient) GetIssue(ctx context.Context, issueKey string, fields string) (*IssueResponse, error) {
	path := "/rest/api/3/issue/" + url.PathEscape(issueKey)
	if fields != "" {
		path += "?fields=" + url.QueryEscape(fields)
	}

	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var issue IssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	return &issue, nil
}

// SearchIssues searches issues using JQL via POST /rest/api/3/search/jql.
func (c *JiraClient) SearchIssues(ctx context.Context, jql string, fields string, maxResults int) (*SearchResponse, error) {
	payload := map[string]interface{}{
		"jql":        jql,
		"maxResults": maxResults,
	}
	if fields != "" {
		payload["fields"] = strings.Split(fields, ",")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal search: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/rest/api/3/search/jql", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	return &result, nil
}

// CreateIssue creates a new issue.
func (c *JiraClient) CreateIssue(ctx context.Context, fields map[string]interface{}) (*CreateIssueResponse, error) {
	payload := map[string]interface{}{
		"fields": fields,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal create: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/rest/api/3/issue/", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, c.readError(resp)
	}

	var result CreateIssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode create: %w", err)
	}
	return &result, nil
}

// UpdateIssue updates fields on an existing issue. Returns nil on success (204).
func (c *JiraClient) UpdateIssue(ctx context.Context, issueKey string, fields map[string]interface{}, update map[string]interface{}) error {
	payload := make(map[string]interface{})
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	if len(update) > 0 {
		payload["update"] = update
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal update: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(issueKey), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("update issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return c.readError(resp)
	}
	return nil
}

// AddComment adds a comment to an issue using ADF format.
func (c *JiraClient) AddComment(ctx context.Context, issueKey string, adfBody map[string]interface{}) (*CommentResponse, error) {
	payload := map[string]interface{}{
		"body": adfBody,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal comment: %w", err)
	}

	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/comment"
	resp, err := c.doRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("add comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, c.readError(resp)
	}

	var result CommentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode comment: %w", err)
	}
	return &result, nil
}

// GetTransitions returns available workflow transitions for an issue.
func (c *JiraClient) GetTransitions(ctx context.Context, issueKey string) ([]Transition, error) {
	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/transitions"

	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get transitions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result struct {
		Transitions []Transition `json:"transitions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode transitions: %w", err)
	}
	return result.Transitions, nil
}

// TransitionIssue executes a workflow transition on an issue.
func (c *JiraClient) TransitionIssue(ctx context.Context, issueKey, transitionID string) error {
	payload := map[string]interface{}{
		"transition": map[string]string{"id": transitionID},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal transition: %w", err)
	}

	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/transitions"
	resp, err := c.doRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("transition issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return c.readError(resp)
	}
	return nil
}

// AssignIssue assigns an issue to a user by accountId, or unassigns if accountID is empty.
func (c *JiraClient) AssignIssue(ctx context.Context, issueKey, accountID string) error {
	var payload map[string]interface{}
	if accountID == "" || strings.EqualFold(accountID, "none") {
		payload = map[string]interface{}{"accountId": nil}
	} else {
		payload = map[string]interface{}{"accountId": accountID}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal assign: %w", err)
	}

	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/assignee"
	resp, err := c.doRequest(ctx, http.MethodPut, path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("assign issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return c.readError(resp)
	}
	return nil
}

// SearchUsers finds users by display name or email via GET /rest/api/3/user/search.
func (c *JiraClient) SearchUsers(ctx context.Context, query string, maxResults int) ([]UserResponse, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("maxResults", fmt.Sprintf("%d", maxResults))

	resp, err := c.doRequest(ctx, http.MethodGet, "/rest/api/3/user/search?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var users []UserResponse
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	return users, nil
}

// GetProject fetches project details via GET /rest/api/3/project/{projectIdOrKey}.
func (c *JiraClient) GetProject(ctx context.Context, projectKey string) (*ProjectResponse, error) {
	path := "/rest/api/3/project/" + url.PathEscape(projectKey) + "?expand=issueTypes"

	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var project ProjectResponse
	if err := json.NewDecoder(resp.Body).Decode(&project); err != nil {
		return nil, fmt.Errorf("decode project: %w", err)
	}
	return &project, nil
}

// GetBoards fetches agile boards, optionally filtered by project key.
// Uses GET /rest/agile/1.0/board.
func (c *JiraClient) GetBoards(ctx context.Context, projectKey string) ([]BoardResponse, error) {
	params := url.Values{}
	if projectKey != "" {
		params.Set("projectKeyOrId", projectKey)
	}
	params.Set("maxResults", "50")

	path := "/rest/agile/1.0/board?" + params.Encode()
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get boards: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result struct {
		Values []BoardResponse `json:"values"`
		Total  int             `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode boards: %w", err)
	}
	return result.Values, nil
}

// GetSprints fetches sprints for a board, optionally filtered by state.
// Uses GET /rest/agile/1.0/board/{boardId}/sprint.
func (c *JiraClient) GetSprints(ctx context.Context, boardID int, state string) ([]SprintResponse, error) {
	params := url.Values{}
	if state != "" {
		params.Set("state", state)
	}
	params.Set("maxResults", "50")

	path := fmt.Sprintf("/rest/agile/1.0/board/%d/sprint?%s", boardID, params.Encode())
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get sprints: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result struct {
		Values []SprintResponse `json:"values"`
		Total  int              `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode sprints: %w", err)
	}
	return result.Values, nil
}

// GetSprintIssues fetches issues in a sprint via GET /rest/agile/1.0/sprint/{sprintId}/issue.
func (c *JiraClient) GetSprintIssues(ctx context.Context, sprintID int, fields string, maxResults int) (*SearchResponse, error) {
	params := url.Values{}
	if fields != "" {
		params.Set("fields", fields)
	}
	params.Set("maxResults", fmt.Sprintf("%d", maxResults))

	path := fmt.Sprintf("/rest/agile/1.0/sprint/%d/issue?%s", sprintID, params.Encode())
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get sprint issues: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode sprint issues: %w", err)
	}
	return &result, nil
}

// AddWorklog adds a time-tracking worklog to an issue.
// Uses POST /rest/api/3/issue/{issueIdOrKey}/worklog.
func (c *JiraClient) AddWorklog(ctx context.Context, issueKey string, timeSpentSeconds int, started string, comment string) (*WorklogResponse, error) {
	payload := map[string]interface{}{
		"timeSpentSeconds": timeSpentSeconds,
	}
	if started != "" {
		payload["started"] = started
	}
	if comment != "" {
		payload["comment"] = adfFromText(comment)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal worklog: %w", err)
	}

	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/worklog"
	resp, err := c.doRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("add worklog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, c.readError(resp)
	}

	var result WorklogResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode worklog: %w", err)
	}
	return &result, nil
}

// CreateIssueLink creates a link between two issues.
// Uses POST /rest/api/3/issueLink.
func (c *JiraClient) CreateIssueLink(ctx context.Context, linkType, inwardKey, outwardKey string) error {
	payload := map[string]interface{}{
		"type":         map[string]string{"name": linkType},
		"inwardIssue":  map[string]string{"key": inwardKey},
		"outwardIssue": map[string]string{"key": outwardKey},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal issue link: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/rest/api/3/issueLink", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create issue link: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return c.readError(resp)
	}
	return nil
}

// GetIssueLinkTypes fetches available issue link types.
// Uses GET /rest/api/3/issueLinkType.
func (c *JiraClient) GetIssueLinkTypes(ctx context.Context) ([]IssueLinkTypeResponse, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/rest/api/3/issueLinkType", nil)
	if err != nil {
		return nil, fmt.Errorf("get link types: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result struct {
		IssueLinkTypes []IssueLinkTypeResponse `json:"issueLinkTypes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode link types: %w", err)
	}
	return result.IssueLinkTypes, nil
}

// GetChangelog fetches the change history for an issue.
// Uses GET /rest/api/3/issue/{issueIdOrKey}/changelog.
func (c *JiraClient) GetChangelog(ctx context.Context, issueKey string, maxResults int) (*ChangelogResponse, error) {
	params := url.Values{}
	params.Set("maxResults", fmt.Sprintf("%d", maxResults))

	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/changelog?" + params.Encode()
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get changelog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result struct {
		Values []ChangelogHistory `json:"values"`
		Total  int                `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode changelog: %w", err)
	}
	return &ChangelogResponse{
		Histories: result.Values,
		Total:     result.Total,
	}, nil
}

// --- Helpers ---

// adfFromText wraps plain text into Atlassian Document Format (required by v3 API).
func adfFromText(text string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": []map[string]interface{}{
			{
				"type": "paragraph",
				"content": []map[string]interface{}{
					{"type": "text", "text": text},
				},
			},
		},
	}
}

// readError reads the error body from a JIRA API response and returns a formatted error
// including the HTTP status code for upstream error routing.
func (c *JiraClient) readError(resp *http.Response) error {
	bodyBytes, _ := io.ReadAll(resp.Body)

	// Try to extract JIRA error messages
	var jiraErr struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if json.Unmarshal(bodyBytes, &jiraErr) == nil {
		msgs := jiraErr.ErrorMessages
		for field, msg := range jiraErr.Errors {
			msgs = append(msgs, field+": "+msg)
		}
		if len(msgs) > 0 {
			return fmt.Errorf("status %d: %s", resp.StatusCode, strings.Join(msgs, "; "))
		}
	}

	// Fallback: raw body
	if len(bodyBytes) > 0 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes))
	}
	return fmt.Errorf("status %d", resp.StatusCode)
}
