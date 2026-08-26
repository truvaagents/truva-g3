package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	arxivBaseURL = "https://export.arxiv.org/api/query"

	// Rate limit: arXiv requires minimum 3 seconds between requests
	// Violating this results in 403 Forbidden
	rateLimitInterval = 3 * time.Second
)

// ---- Atom XML Struct Hierarchy ----

// AtomFeed represents the top-level <feed> element in the arXiv Atom response.
// Contains OpenSearch pagination metadata and a list of Entry elements.
type AtomFeed struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string   `xml:"http://www.w3.org/2005/Atom title"`
	ID      string   `xml:"http://www.w3.org/2005/Atom id"`
	Updated string   `xml:"http://www.w3.org/2005/Atom updated"`

	// OpenSearch namespace elements for pagination metadata
	TotalResults int `xml:"http://a9.com/-/spec/opensearch/1.1/ totalResults"`
	StartIndex   int `xml:"http://a9.com/-/spec/opensearch/1.1/ startIndex"`
	ItemsPerPage int `xml:"http://a9.com/-/spec/opensearch/1.1/ itemsPerPage"`

	// List of paper entries
	Entries []AtomEntry `xml:"http://www.w3.org/2005/Atom entry"`
}

// AtomEntry represents a single <entry> element (one paper).
// Contains core Atom elements plus arXiv-namespaced extensions.
type AtomEntry struct {
	ID        string `xml:"http://www.w3.org/2005/Atom id"`        // Full URL: http://arxiv.org/abs/2209.15001v3
	Title     string `xml:"http://www.w3.org/2005/Atom title"`     // Paper title (may contain newlines)
	Summary   string `xml:"http://www.w3.org/2005/Atom summary"`   // Abstract (may contain newlines)
	Published string `xml:"http://www.w3.org/2005/Atom published"` // ISO 8601 datetime
	Updated   string `xml:"http://www.w3.org/2005/Atom updated"`   // ISO 8601 datetime

	// Multiple authors per paper
	Authors []AtomAuthor `xml:"http://www.w3.org/2005/Atom author"`

	// Multiple links (abs page, PDF, DOI, etc.)
	Links []AtomLink `xml:"http://www.w3.org/2005/Atom link"`

	// Multiple categories per paper (Atom namespace)
	Categories []AtomCategory `xml:"http://www.w3.org/2005/Atom category"`

	// arXiv-namespaced elements (use pointer types for optional elements)
	PrimaryCategory *ArxivCategory `xml:"http://arxiv.org/schemas/atom primary_category"`
	Comment         *string        `xml:"http://arxiv.org/schemas/atom comment"`     // Optional: e.g., "15 pages, 3 figures"
	JournalRef      *string        `xml:"http://arxiv.org/schemas/atom journal_ref"` // Optional: journal citation
	DOI             *string        `xml:"http://arxiv.org/schemas/atom doi"`         // Optional: DOI string
}

// AtomAuthor represents an <author> element.
type AtomAuthor struct {
	Name        string  `xml:"http://www.w3.org/2005/Atom name"`
	Affiliation *string `xml:"http://arxiv.org/schemas/atom affiliation"` // Optional
}

// AtomLink represents a <link> element.
// Rel values: "alternate" (abs page), "related" (PDF with title="pdf"), etc.
type AtomLink struct {
	Href  string `xml:"href,attr"`
	Rel   string `xml:"rel,attr"`
	Type  string `xml:"type,attr,omitempty"`
	Title string `xml:"title,attr,omitempty"`
}

// AtomCategory represents a <category> element (Atom namespace).
type AtomCategory struct {
	Term   string `xml:"term,attr"`
	Scheme string `xml:"scheme,attr,omitempty"`
}

// ArxivCategory represents an <arxiv:primary_category> element (arXiv namespace).
type ArxivCategory struct {
	Term   string `xml:"term,attr"`
	Scheme string `xml:"scheme,attr,omitempty"`
}

// ArxivClient handles API communication with the arXiv API.
// Includes client-side rate limiting (1 request per 3 seconds) to comply
// with arXiv's usage policy. Violations result in 403 Forbidden.
type ArxivClient struct {
	httpClient  *http.Client
	mu          sync.Mutex // Protects lastRequest for rate limiting
	lastRequest time.Time  // Time of last API request
}

// NewArxivClient creates a new arXiv API client with traced HTTP transport
// and client-side rate limiting.
func NewArxivClient() *ArxivClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second // arXiv can be slow for large queries

	return &ArxivClient{
		httpClient: tracedClient,
	}
}

// enforceRateLimit blocks until at least rateLimitInterval has passed since the last request.
// This is critical -- arXiv returns 403 if requests are too frequent.
func (c *ArxivClient) enforceRateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.lastRequest.IsZero() {
		elapsed := time.Since(c.lastRequest)
		if elapsed < rateLimitInterval {
			time.Sleep(rateLimitInterval - elapsed)
		}
	}
	c.lastRequest = time.Now()
}

// SearchPapers searches arXiv for papers matching the query.
// Query syntax: use +AND+ for AND, +OR+ for OR, +ANDNOT+ for NOT.
// Category filter uses: cat:{category} combined with search_query via +AND+.
func (c *ArxivClient) SearchPapers(ctx context.Context, query, category string, maxResults int, sortBy string) (*AtomFeed, error) {
	c.enforceRateLimit()

	// Build search query — do NOT use url.QueryEscape here because
	// url.Values.Encode() will handle encoding. Double-encoding breaks
	// multi-word queries (spaces become %2B instead of + for arXiv).
	// We build the URL manually to preserve arXiv's +AND+ connector syntax.
	searchQuery := fmt.Sprintf("all:%s", query)

	// Add category filter if provided
	if category != "" {
		searchQuery = fmt.Sprintf("%s+AND+cat:%s", searchQuery, category)
	}

	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 30000 {
		maxResults = 30000
	}

	// Build remaining params normally
	params := url.Values{}
	params.Set("max_results", fmt.Sprintf("%d", maxResults))

	// Sort options: relevance (default), lastUpdatedDate, submittedDate
	if sortBy != "" {
		params.Set("sortBy", sortBy)
		params.Set("sortOrder", "descending")
	}

	// Manually construct URL: search_query uses arXiv-specific syntax with +AND+
	// that would be mangled by url.Values encoding
	fullURL := fmt.Sprintf("%s?search_query=%s&%s", arxivBaseURL, url.QueryEscape(searchQuery), params.Encode())

	return c.doRequest(ctx, fullURL)
}

// GetPaper fetches a specific paper by its arXiv ID.
// The ID can include a version suffix (e.g., "2301.07041v2").
// Uses id_list parameter instead of search_query.
func (c *ArxivClient) GetPaper(ctx context.Context, arxivID string) (*AtomFeed, error) {
	c.enforceRateLimit()

	params := url.Values{}
	params.Set("id_list", arxivID)

	fullURL := fmt.Sprintf("%s?%s", arxivBaseURL, params.Encode())

	return c.doRequest(ctx, fullURL)
}

// GetRecentPapers fetches the most recently submitted papers in a category.
// Uses search_query=cat:{category} with sortBy=submittedDate and sortOrder=descending.
func (c *ArxivClient) GetRecentPapers(ctx context.Context, category string, maxResults int) (*AtomFeed, error) {
	c.enforceRateLimit()

	if maxResults <= 0 {
		maxResults = 10
	}

	// Don't double-encode: url.QueryEscape below handles encoding once
	searchQuery := fmt.Sprintf("cat:%s", category)

	params := url.Values{}
	params.Set("sortBy", "submittedDate")
	params.Set("sortOrder", "descending")
	params.Set("max_results", fmt.Sprintf("%d", maxResults))

	fullURL := fmt.Sprintf("%s?search_query=%s&%s", arxivBaseURL, url.QueryEscape(searchQuery), params.Encode())

	return c.doRequest(ctx, fullURL)
}

// doRequest performs the HTTP request and parses the Atom XML response.
func (c *ArxivClient) doRequest(ctx context.Context, fullURL string) (*AtomFeed, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var feed AtomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("failed to parse XML response: %w", err)
	}

	return &feed, nil
}

// arxivIDRegex extracts the paper ID from the full arXiv URL.
// Example: "http://arxiv.org/abs/2209.15001v3" -> "2209.15001v3"
var arxivIDRegex = regexp.MustCompile(`arxiv\.org/abs/(.+)$`)

// extractArxivID extracts the paper ID from the <id> URL.
// The <id> element contains a full URL like "http://arxiv.org/abs/2209.15001v3".
func extractArxivID(idURL string) string {
	matches := arxivIDRegex.FindStringSubmatch(idURL)
	if len(matches) > 1 {
		return matches[1]
	}
	// Fallback: return the URL as-is
	return idURL
}

// normalizeWhitespace collapses multiple whitespace characters (including newlines)
// into single spaces and trims the result. arXiv titles and abstracts often contain
// embedded newlines in the XML.
func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// entryToPaperResult converts an AtomEntry (parsed XML) to a PaperResult (JSON output).
func entryToPaperResult(entry AtomEntry) PaperResult {
	paper := PaperResult{
		ArxivID:       extractArxivID(entry.ID),
		Title:         normalizeWhitespace(entry.Title),
		Abstract:      normalizeWhitespace(entry.Summary),
		PublishedDate: entry.Published,
		UpdatedDate:   entry.Updated,
	}

	// Extract author names
	authors := make([]string, 0, len(entry.Authors))
	for _, a := range entry.Authors {
		authors = append(authors, a.Name)
	}
	paper.Authors = authors

	// Extract categories
	categories := make([]string, 0, len(entry.Categories))
	for _, c := range entry.Categories {
		categories = append(categories, c.Term)
	}
	paper.Categories = categories

	// Primary category from arxiv:primary_category
	if entry.PrimaryCategory != nil {
		paper.PrimaryCategory = entry.PrimaryCategory.Term
	} else if len(paper.Categories) > 0 {
		paper.PrimaryCategory = paper.Categories[0] // Fallback to first category
	}

	// Extract PDF and abstract page URLs from links
	for _, link := range entry.Links {
		if link.Title == "pdf" || link.Type == "application/pdf" {
			paper.PDFURL = link.Href
		}
		if link.Rel == "alternate" {
			paper.AbsURL = link.Href
		}
	}

	// Construct URLs if not found in links
	if paper.AbsURL == "" {
		paper.AbsURL = fmt.Sprintf("https://arxiv.org/abs/%s", paper.ArxivID)
	}
	if paper.PDFURL == "" {
		paper.PDFURL = fmt.Sprintf("https://arxiv.org/pdf/%s", paper.ArxivID)
	}

	// Optional arXiv-namespaced fields (pointer types -- only set if present)
	if entry.Comment != nil {
		paper.Comment = normalizeWhitespace(*entry.Comment)
	}
	if entry.JournalRef != nil {
		paper.JournalRef = normalizeWhitespace(*entry.JournalRef)
	}
	if entry.DOI != nil {
		paper.DOI = strings.TrimSpace(*entry.DOI)
	}

	return paper
}
