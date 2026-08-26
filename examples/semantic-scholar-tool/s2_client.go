package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	s2BaseURL = "https://api.semanticscholar.org/graph/v1"

	// Fields to request for search and citation results
	s2SearchFields = "title,authors,year,citationCount,abstract,url,publicationDate"

	// Fields to request for paper details (superset of search fields + extras)
	// Must request sub-fields for references/citations or API only returns paperId+title
	s2DetailFields = "title,authors,year,citationCount,abstract,url,publicationDate," +
		"tldr,referenceCount,influentialCitationCount,openAccessPdf," +
		"references.title,references.authors,references.year,references.citationCount,references.url," +
		"citations.title,citations.authors,citations.year,citations.citationCount,citations.url"

	// Fields to request for author profile
	// papers.limit=20 prevents the API from returning ALL papers (no pagination)
	s2AuthorFields = "name,affiliations,paperCount,citationCount,hIndex,url," +
		"papers,papers.title,papers.year,papers.authors,papers.citationCount,papers.url"
)

// S2Client handles API communication with Semantic Scholar
type S2Client struct {
	apiKey     string
	httpClient *http.Client
	rateMu     sync.Mutex
	rateTicker *time.Ticker
}

// --- Raw API response types ---

// S2SearchResponse represents the raw search API response
type S2SearchResponse struct {
	Total int       `json:"total"`
	Data  []S2Paper `json:"data"`
	Next  *int      `json:"next,omitempty"` // ABSENT when no more results, not null
}

// S2Paper represents a paper from the API
type S2Paper struct {
	PaperID                  string       `json:"paperId"`
	Title                    string       `json:"title"`
	Authors                  []S2Author   `json:"authors"`
	Year                     int          `json:"year"`
	CitationCount            int          `json:"citationCount"`
	Abstract                 string       `json:"abstract"`
	URL                      string       `json:"url"`
	PublicationDate          string       `json:"publicationDate"`
	TLDR                     *S2TLDR      `json:"tldr"`
	OpenAccessPdf            *S2PDF       `json:"openAccessPdf"`
	ReferenceCount           int          `json:"referenceCount"`
	InfluentialCitationCount int          `json:"influentialCitationCount"`
	References               []S2PaperRef `json:"references"`
	Citations                []S2PaperRef `json:"citations"`
}

// S2PaperRef represents a reference/citation entry where paperId can be NULL
type S2PaperRef struct {
	PaperID       *string    `json:"paperId"` // Can be NULL -- use *string
	Title         string     `json:"title"`
	Authors       []S2Author `json:"authors"`
	Year          int        `json:"year"`
	CitationCount int        `json:"citationCount"`
	URL           string     `json:"url"`
}

// S2Author represents an author from the API
type S2Author struct {
	AuthorID string `json:"authorId"` // String (numeric format), not integer
	Name     string `json:"name"`
}

// S2TLDR represents the AI-generated summary
type S2TLDR struct {
	Model string `json:"model"`
	Text  string `json:"text"`
}

// S2PDF represents open access PDF info
type S2PDF struct {
	URL    string `json:"url"`
	Status string `json:"status"`
}

// S2AuthorProfile represents the raw author profile API response
type S2AuthorProfile struct {
	AuthorID      string    `json:"authorId"`
	Name          string    `json:"name"`
	Affiliations  []string  `json:"affiliations"`
	PaperCount    int       `json:"paperCount"`
	CitationCount int       `json:"citationCount"`
	HIndex        int       `json:"hIndex"`
	URL           string    `json:"url"`
	Papers        []S2Paper `json:"papers"`
}

// S2CitationsResponse represents the raw citations API response
type S2CitationsResponse struct {
	Data []struct {
		CitingPaper S2Paper `json:"citingPaper"`
	} `json:"data"`
	Next *int `json:"next,omitempty"`
}

// S2APIError represents the API error response format
// Note: code is STRING, not integer
type S2APIError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// NewS2Client creates a new Semantic Scholar API client with traced HTTP client
func NewS2Client(apiKey string) *S2Client {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	return &S2Client{
		apiKey:     apiKey,
		httpClient: tracedClient,
		rateTicker: time.NewTicker(1 * time.Second), // 1 req/sec rate limit
	}
}

// waitForRateLimit enforces 1 req/sec client-side rate limiting
func (c *S2Client) waitForRateLimit() {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	<-c.rateTicker.C
}

// doRequest performs an HTTP GET with x-api-key header and rate limiting
func (c *S2Client) doRequest(ctx context.Context, fullURL string) (*http.Response, error) {
	// Enforce rate limit before making request
	c.waitForRateLimit()

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set API key header if configured
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	return resp, nil
}

// SearchPapers searches for academic papers by keyword
func (c *S2Client) SearchPapers(ctx context.Context, query string, maxResults int, year string, fieldsOfStudy string) (*S2SearchResponse, error) {
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 100 {
		maxResults = 100
	}

	params := url.Values{}
	params.Add("query", query)
	params.Add("limit", strconv.Itoa(maxResults))
	params.Add("fields", s2SearchFields)

	if year != "" {
		params.Add("year", year)
	}
	if fieldsOfStudy != "" {
		params.Add("fieldsOfStudy", fieldsOfStudy)
	}

	fullURL := fmt.Sprintf("%s/paper/search?%s", s2BaseURL, params.Encode())

	resp, err := c.doRequest(ctx, fullURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result S2SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetPaperDetails fetches detailed information about a specific paper
func (c *S2Client) GetPaperDetails(ctx context.Context, paperID string) (*S2Paper, error) {
	params := url.Values{}
	params.Add("fields", s2DetailFields)

	// Paper ID is part of the path, not a query parameter
	fullURL := fmt.Sprintf("%s/paper/%s?%s", s2BaseURL, url.PathEscape(paperID), params.Encode())

	resp, err := c.doRequest(ctx, fullURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var paper S2Paper
	if err := json.NewDecoder(resp.Body).Decode(&paper); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if paper.PaperID == "" {
		return nil, fmt.Errorf("API error 404: paper not found: %s", paperID)
	}

	return &paper, nil
}

// GetAuthor fetches author profile information
func (c *S2Client) GetAuthor(ctx context.Context, authorID string) (*S2AuthorProfile, error) {
	params := url.Values{}
	params.Add("fields", s2AuthorFields)

	// Limit papers returned (API returns ALL papers with no pagination otherwise)
	// papers.limit is set via the fields parameter: papers.limit is not a separate param,
	// we limit by only requesting needed paper fields and capping in our response
	fullURL := fmt.Sprintf("%s/author/%s?%s", s2BaseURL, url.PathEscape(authorID), params.Encode())

	resp, err := c.doRequest(ctx, fullURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var author S2AuthorProfile
	if err := json.NewDecoder(resp.Body).Decode(&author); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if author.AuthorID == "" {
		return nil, fmt.Errorf("API error 404: author not found: %s", authorID)
	}

	return &author, nil
}

// GetCitations fetches papers that cite the given paper
func (c *S2Client) GetCitations(ctx context.Context, paperID string, maxResults int) (*S2CitationsResponse, error) {
	if maxResults <= 0 {
		maxResults = 20
	}
	if maxResults > 100 {
		maxResults = 100
	}

	params := url.Values{}
	params.Add("fields", s2SearchFields)
	params.Add("limit", strconv.Itoa(maxResults))

	fullURL := fmt.Sprintf("%s/paper/%s/citations?%s", s2BaseURL, url.PathEscape(paperID), params.Encode())

	resp, err := c.doRequest(ctx, fullURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result S2CitationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
