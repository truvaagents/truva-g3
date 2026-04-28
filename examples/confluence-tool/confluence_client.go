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

// ConfluenceClient handles HTTP communication with the Confluence REST API v2.
type ConfluenceClient struct {
	baseURL    string // e.g. "https://mycompany.atlassian.net"
	authHeader string // "Basic base64(email:token)"
	httpClient *http.Client
}

// ---------------------------------------------------------------------------
// Confluence API response types
// ---------------------------------------------------------------------------

// ConfluenceAPIPage represents a page from the Confluence REST API v2.
type ConfluenceAPIPage struct {
	ID        string             `json:"id"`
	Type      string             `json:"type"`
	Status    string             `json:"status"`
	Title     string             `json:"title"`
	SpaceID   string             `json:"spaceId"`
	ParentID  string             `json:"parentId,omitempty"`
	Version   *ConfluenceVersion `json:"version,omitempty"`
	Body      *ConfluenceBody    `json:"body,omitempty"`
	Links     *ConfluenceLinks   `json:"_links,omitempty"`
	CreatedAt string             `json:"createdAt,omitempty"`
}

// ConfluenceVersion holds the version number and timestamp.
type ConfluenceVersion struct {
	Number    int    `json:"number"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// ConfluenceBody holds the storage-format body content.
type ConfluenceBody struct {
	Storage *ConfluenceStorage `json:"storage,omitempty"`
}

// ConfluenceStorage holds the XHTML storage representation.
type ConfluenceStorage struct {
	Representation string `json:"representation"`
	Value          string `json:"value"`
}

// ConfluenceLinks holds link references from the API.
type ConfluenceLinks struct {
	WebUI string `json:"webui,omitempty"`
	Base  string `json:"base,omitempty"`
}

// ConfluenceAPISpace represents a space from the Confluence REST API v2.
type ConfluenceAPISpace struct {
	ID          string          `json:"id"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	HomepageID  string          `json:"homepageId"`
	Description *string         `json:"description"`
	CreatedAt   string          `json:"createdAt"`
	Links       *ConfluenceLinks `json:"_links,omitempty"`
}

// ConfluenceSpacesResult is the response from GET /wiki/api/v2/spaces.
type ConfluenceSpacesResult struct {
	Results []ConfluenceAPISpace `json:"results"`
}

// ConfluenceSearchResult is the response from the CQL search endpoint.
type ConfluenceSearchResult struct {
	Results []ConfluenceSearchHit `json:"results"`
	Start   int                   `json:"start"`
	Limit   int                   `json:"limit"`
	Size    int                   `json:"size"`
}

// ConfluenceSearchHit represents a single search result from the v1 CQL content/search endpoint.
// NOTE: /wiki/rest/api/content/search returns content fields directly on each result (no "content" wrapper).
type ConfluenceSearchHit struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
	URL     string `json:"url"`
	Version struct {
		Number int `json:"number"`
	} `json:"version"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
	Space struct {
		ID   int    `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"space"`
	History struct {
		CreatedDate string `json:"createdDate"`
		LastUpdated struct {
			When string `json:"when"`
		} `json:"lastUpdated"`
	} `json:"history"`
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewConfluenceClient creates a new Confluence API client with traced HTTP transport.
// Auth pattern identical to jira-tool: Basic Auth with email:api_token.
func NewConfluenceClient(baseURL, email, apiToken string) *ConfluenceClient {
	auth := base64.StdEncoding.EncodeToString(
		[]byte(email + ":" + apiToken),
	)

	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	return &ConfluenceClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		authHeader: "Basic " + auth,
		httpClient: tracedClient,
	}
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

// doRequest builds and executes an HTTP request with Atlassian auth headers.
func (c *ConfluenceClient) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	reqURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TruvaG3-ConfluenceTool/1.0")

	return c.httpClient.Do(req)
}

// readError extracts a structured error message from a Confluence API response.
func (c *ConfluenceClient) readError(resp *http.Response) error {
	bodyBytes, _ := io.ReadAll(resp.Body)

	var confErr struct {
		StatusCode int    `json:"statusCode"`
		Message    string `json:"message"`
		Data       struct {
			Errors []struct {
				Message struct {
					Key  string `json:"key"`
					Args []any  `json:"args"`
				} `json:"message"`
			} `json:"errors"`
		} `json:"data"`
	}
	if json.Unmarshal(bodyBytes, &confErr) == nil && confErr.Message != "" {
		return fmt.Errorf("status %d: %s", resp.StatusCode, confErr.Message)
	}

	if len(bodyBytes) > 0 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes))
	}
	return fmt.Errorf("status %d", resp.StatusCode)
}

// ---------------------------------------------------------------------------
// API Methods
// ---------------------------------------------------------------------------

// CreatePage creates a new page via POST /wiki/api/v2/pages.
func (c *ConfluenceClient) CreatePage(ctx context.Context, spaceID, title, content, parentID string) (*ConfluenceAPIPage, error) {
	body := map[string]interface{}{
		"spaceId": spaceID,
		"status":  "current",
		"title":   title,
		"body": map[string]interface{}{
			"representation": "storage",
			"value":          contentToStorageFormat(content),
		},
	}

	if parentID != "" {
		body["parentId"] = parentID
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal create page: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/wiki/api/v2/pages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, c.readError(resp)
	}

	var page ConfluenceAPIPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode create page: %w", err)
	}
	return &page, nil
}

// SearchPages searches pages via CQL: GET /wiki/rest/api/content/search?cql=...
// Uses v1 search endpoint because v2 doesn't have a CQL search equivalent.
func (c *ConfluenceClient) SearchPages(ctx context.Context, query, spaceKey string, limit int) (*ConfluenceSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 250 {
		limit = 250
	}

	// Build CQL query
	var cql string
	if strings.Contains(query, "=") || strings.Contains(query, "~") {
		// User provided raw CQL
		cql = query
	} else {
		// Simple text search -- search title and text
		cql = fmt.Sprintf("type=page AND (title~\"%s\" OR text~\"%s\")", query, query)
	}
	if spaceKey != "" && !strings.Contains(strings.ToLower(cql), "space=") {
		cql = fmt.Sprintf("space=%s AND (%s)", spaceKey, cql)
	}

	params := url.Values{}
	params.Set("cql", cql)
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("expand", "version,space,history.lastUpdated")

	path := "/wiki/rest/api/content/search?" + params.Encode()
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("search pages: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result ConfluenceSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	return &result, nil
}

// GetPage retrieves a single page via GET /wiki/api/v2/pages/{id}.
func (c *ConfluenceClient) GetPage(ctx context.Context, pageID string, includeBody bool) (*ConfluenceAPIPage, error) {
	path := "/wiki/api/v2/pages/" + url.PathEscape(pageID)
	if includeBody {
		path += "?body-format=storage"
	}

	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var page ConfluenceAPIPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode page: %w", err)
	}
	return &page, nil
}

// UpdatePage updates a page via PUT /wiki/api/v2/pages/{id}.
// CRITICAL: Must GET current version first, then PUT with version.number + 1.
func (c *ConfluenceClient) UpdatePage(ctx context.Context, pageID, title, content string) (*ConfluenceAPIPage, error) {
	// Step 1: GET current page to obtain version number and current title
	current, err := c.GetPage(ctx, pageID, false)
	if err != nil {
		return nil, fmt.Errorf("get current version for update: %w", err)
	}

	// Use current title if none provided
	if title == "" {
		title = current.Title
	}

	currentVersion := 1
	if current.Version != nil {
		currentVersion = current.Version.Number
	}

	// Step 2: Build update body with version + 1
	body := map[string]interface{}{
		"id":     pageID,
		"status": "current",
		"title":  title,
		"version": map[string]interface{}{
			"number": currentVersion + 1,
		},
	}

	// Only include body if content was provided
	if content != "" {
		body["body"] = map[string]interface{}{
			"representation": "storage",
			"value":          contentToStorageFormat(content),
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal update: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPut, "/wiki/api/v2/pages/"+url.PathEscape(pageID), bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("update page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var page ConfluenceAPIPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode update: %w", err)
	}
	return &page, nil
}

// ListSpaces lists spaces via GET /wiki/api/v2/spaces.
func (c *ConfluenceClient) ListSpaces(ctx context.Context, limit int) (*ConfluenceSpacesResult, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 250 {
		limit = 250
	}

	path := fmt.Sprintf("/wiki/api/v2/spaces?limit=%d", limit)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("list spaces: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var result ConfluenceSpacesResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode spaces: %w", err)
	}
	return &result, nil
}

// ---------------------------------------------------------------------------
// Content conversion: markdown-like text -> Confluence storage format (XHTML)
// ---------------------------------------------------------------------------

// contentToStorageFormat converts markdown-like text into Confluence storage format (XHTML).
// Handles:
//   - "## Heading" -> <h2>Heading</h2>
//   - "### Heading" -> <h3>Heading</h3>
//   - "- Item" or "* Item" -> <ul><li>Item</li></ul>
//   - Plain text -> <p>Text</p>
//   - Empty lines -> paragraph breaks
func contentToStorageFormat(content string) string {
	if content == "" {
		return ""
	}

	var sb strings.Builder
	lines := strings.Split(content, "\n")
	inList := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			if inList {
				sb.WriteString("</ul>")
				inList = false
			}
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "### "):
			if inList {
				sb.WriteString("</ul>")
				inList = false
			}
			text := strings.TrimPrefix(trimmed, "### ")
			sb.WriteString("<h3>" + htmlEscape(text) + "</h3>")

		case strings.HasPrefix(trimmed, "## "):
			if inList {
				sb.WriteString("</ul>")
				inList = false
			}
			text := strings.TrimPrefix(trimmed, "## ")
			sb.WriteString("<h2>" + htmlEscape(text) + "</h2>")

		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			if !inList {
				sb.WriteString("<ul>")
				inList = true
			}
			text := trimmed[2:]
			sb.WriteString("<li>" + htmlEscape(text) + "</li>")

		default:
			if inList {
				sb.WriteString("</ul>")
				inList = false
			}
			sb.WriteString("<p>" + htmlEscape(trimmed) + "</p>")
		}
	}

	if inList {
		sb.WriteString("</ul>")
	}

	return sb.String()
}

// htmlEscape escapes special characters for Confluence storage format.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// pageURL builds the full web URL for a Confluence page from its _links.
func (c *ConfluenceClient) pageURL(page *ConfluenceAPIPage) string {
	if page.Links != nil && page.Links.WebUI != "" {
		return c.baseURL + "/wiki" + page.Links.WebUI
	}
	return ""
}
