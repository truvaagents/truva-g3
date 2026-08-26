package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
	"golang.org/x/time/rate"
)

const (
	ncbiBaseURL = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"
)

type PubMedClient struct {
	apiKey     string
	toolName   string
	email      string
	httpClient *http.Client
	limiter    *rate.Limiter
}

func NewPubMedClient(apiKey, toolName, email string) *PubMedClient {
	// Rate limit: 3 req/sec without key, 10 req/sec with key
	rateLimit := rate.Limit(3)
	if apiKey != "" {
		rateLimit = rate.Limit(10)
	}

	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	return &PubMedClient{
		apiKey:     apiKey,
		toolName:   toolName,
		email:      email,
		httpClient: tracedClient,
		limiter:    rate.NewLimiter(rateLimit, 1), // burst of 1 to enforce strict rate
	}
}

// addNCBIParams adds required NCBI policy parameters to every request.
func (c *PubMedClient) addNCBIParams(params url.Values) {
	params.Set("tool", c.toolName)
	params.Set("email", c.email)
	params.Set("retmode", "json")
	if c.apiKey != "" {
		params.Set("api_key", c.apiKey)
	}
}

// ESearchResult represents the esearch.fcgi JSON response.
// WARNING: count, retmax, retstart are STRINGS, not integers.
type ESearchResult struct {
	Header ESearchHeader  `json:"header"`
	Result ESearchPayload `json:"esearchresult"`
}

type ESearchHeader struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

type ESearchPayload struct {
	Count    string   `json:"count"`    // STRING -- must strconv.Atoi()
	RetMax   string   `json:"retmax"`   // STRING
	RetStart string   `json:"retstart"` // STRING
	IDList   []string `json:"idlist"`   // List of PMIDs as strings
	// translationstack is a mixed array of objects and raw strings -- skip parsing
}

// ESummaryResult represents the esummary.fcgi JSON response.
// The "result" field is a map where each PMID is a dynamic key.
type ESummaryResult struct {
	Header ESummaryHeader             `json:"header"`
	Result map[string]json.RawMessage `json:"result"`
	// result.uids is a JSON array of PMID strings
	// result["38000000"] is the article data object
}

type ESummaryHeader struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

// ESummaryArticle represents a single article in the esummary response.
type ESummaryArticle struct {
	UID             string              `json:"uid"`     // PMID as string
	PubDate         string              `json:"pubdate"` // e.g., "2023 Nov 16" (inconsistent format)
	Source          string              `json:"source"`  // Abbreviated journal name
	Authors         []ESummaryAuthor    `json:"authors"`
	Title           string              `json:"title"`
	Volume          string              `json:"volume"`
	Issue           string              `json:"issue"`
	Pages           string              `json:"pages"`
	FullJournalName string              `json:"fulljournalname"`
	SortPubDate     string              `json:"sortpubdate"` // e.g., "2023/04/14 00:00" (different format)
	ArticleIDs      []ESummaryArticleID `json:"articleids"`
	PMCRefCount     int                 `json:"pmcrefcount"` // One of few actual integers
	Attributes      []string            `json:"attributes"`  // Check for "Has Abstract"
}

type ESummaryAuthor struct {
	Name     string `json:"name"`
	AuthType string `json:"authtype"`
}

type ESummaryArticleID struct {
	IDType string `json:"idtype"` // "doi", "pmc", "pubmed", "pii"
	Value  string `json:"value"`
}

// ELinkResult represents the elink.fcgi JSON response.
type ELinkResult struct {
	Header   ELinkHeader    `json:"header"`
	LinkSets []ELinkLinkSet `json:"linksets"`
}

type ELinkHeader struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

// ELinkLinkSet represents a single linkset in the elink response.
// WARNING: linksetdbs key is COMPLETELY ABSENT when no citations -- not null, not empty.
type ELinkLinkSet struct {
	DBFrom     string          `json:"dbfrom"`
	IDList     []string        `json:"ids"`                  // Plain string array of PMIDs
	LinkSetDBs json.RawMessage `json:"linksetdbs,omitempty"` // Use RawMessage to check existence
}

// ELinkDBEntry represents a single link database entry.
type ELinkDBEntry struct {
	DBTo     string   `json:"dbto"`
	LinkName string   `json:"linkname"`
	Links    []string `json:"links"` // Plain string array of citing PMIDs
}

// SearchResult holds the combined esearch + esummary result.
type SearchResult struct {
	TotalCount int
	Articles   []ESummaryArticle
}

// SearchArticles performs a two-step search:
// 1. esearch.fcgi -> get matching PMIDs
// 2. esummary.fcgi -> get metadata for those PMIDs
func (c *PubMedClient) SearchArticles(ctx context.Context, query string, maxResults int, sort string) (*SearchResult, error) {
	// Wait for rate limiter
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

	// Step 1: esearch
	params := url.Values{}
	params.Set("db", "pubmed")
	params.Set("term", query)
	params.Set("retmax", strconv.Itoa(maxResults))
	if sort == "date" {
		params.Set("sort", "pub_date")
	} else {
		params.Set("sort", "relevance")
	}
	c.addNCBIParams(params)

	searchURL := fmt.Sprintf("%s/esearch.fcgi?%s", ncbiBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create esearch request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("esearch API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("esearch API returned status %d: %s", resp.StatusCode, string(body))
	}

	var searchResult ESearchResult
	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return nil, fmt.Errorf("failed to decode esearch response: %w", err)
	}

	// Parse count from STRING to int
	totalCount, _ := strconv.Atoi(searchResult.Result.Count)

	if len(searchResult.Result.IDList) == 0 {
		return &SearchResult{TotalCount: totalCount, Articles: nil}, nil
	}

	// Step 2: esummary for the PMIDs
	articles, err := c.GetSummaries(ctx, searchResult.Result.IDList)
	if err != nil {
		return nil, fmt.Errorf("esummary failed after esearch: %w", err)
	}

	return &SearchResult{TotalCount: totalCount, Articles: articles}, nil
}

// GetSummaries fetches article metadata for a list of PMIDs via esummary.
// Handles the dynamic JSON key pattern where each PMID is a key in the result map.
func (c *PubMedClient) GetSummaries(ctx context.Context, pmids []string) ([]ESummaryArticle, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

	params := url.Values{}
	params.Set("db", "pubmed")
	params.Set("id", strings.Join(pmids, ","))
	c.addNCBIParams(params)

	summaryURL := fmt.Sprintf("%s/esummary.fcgi?%s", ncbiBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", summaryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create esummary request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("esummary API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("esummary API returned status %d: %s", resp.StatusCode, string(body))
	}

	var summaryResult ESummaryResult
	if err := json.NewDecoder(resp.Body).Decode(&summaryResult); err != nil {
		return nil, fmt.Errorf("failed to decode esummary response: %w", err)
	}

	// Extract UIDs array from the result map (result.uids is a JSON string array)
	uidsRaw, ok := summaryResult.Result["uids"]
	if !ok {
		return nil, fmt.Errorf("esummary response missing 'uids' key")
	}

	var uids []string
	if err := json.Unmarshal(uidsRaw, &uids); err != nil {
		return nil, fmt.Errorf("failed to parse uids array: %w", err)
	}

	// Parse each PMID's article data using the dynamic key
	articles := make([]ESummaryArticle, 0, len(uids))
	for _, uid := range uids {
		articleRaw, exists := summaryResult.Result[uid]
		if !exists {
			continue // Skip missing PMIDs
		}

		var article ESummaryArticle
		if err := json.Unmarshal(articleRaw, &article); err != nil {
			continue // Skip unparseable articles
		}
		articles = append(articles, article)
	}

	return articles, nil
}

// GetCitingArticles finds articles that cite the given PMID.
// Uses elink.fcgi to find citing PMIDs, then esummary to get their metadata.
// CRITICAL: linksetdbs key is COMPLETELY ABSENT when no citations -- must check existence.
func (c *PubMedClient) GetCitingArticles(ctx context.Context, pmid string) ([]ESummaryArticle, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

	params := url.Values{}
	params.Set("dbfrom", "pubmed")
	params.Set("db", "pubmed")
	params.Set("id", pmid)
	params.Set("linkname", "pubmed_pubmed_citedin")
	params.Set("cmd", "neighbor")
	c.addNCBIParams(params)

	linkURL := fmt.Sprintf("%s/elink.fcgi?%s", ncbiBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", linkURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create elink request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elink API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("elink API returned status %d: %s", resp.StatusCode, string(body))
	}

	var linkResult ELinkResult
	if err := json.NewDecoder(resp.Body).Decode(&linkResult); err != nil {
		return nil, fmt.Errorf("failed to decode elink response: %w", err)
	}

	// Extract citing PMIDs from linksets
	// linksetdbs is COMPLETELY ABSENT when no citations (not null, not empty)
	var citingPMIDs []string
	for _, linkSet := range linkResult.LinkSets {
		// Check if linksetdbs key exists (it's json.RawMessage, nil when absent)
		if linkSet.LinkSetDBs == nil || len(linkSet.LinkSetDBs) == 0 {
			continue
		}

		var dbEntries []ELinkDBEntry
		if err := json.Unmarshal(linkSet.LinkSetDBs, &dbEntries); err != nil {
			continue
		}

		for _, entry := range dbEntries {
			if entry.LinkName == "pubmed_pubmed_citedin" {
				for _, link := range entry.Links {
					citingPMIDs = append(citingPMIDs, link)
				}
			}
		}
	}

	if len(citingPMIDs) == 0 {
		return nil, nil // No citations found -- valid result, not an error
	}

	// Fetch metadata for citing articles via esummary
	return c.GetSummaries(ctx, citingPMIDs)
}

// extractDOI extracts the DOI from the articleids array.
func extractDOI(articleIDs []ESummaryArticleID) string {
	for _, id := range articleIDs {
		if id.IDType == "doi" {
			return id.Value
		}
	}
	return ""
}

// hasAbstract checks the attributes array for "Has Abstract".
func hasAbstract(attributes []string) bool {
	for _, attr := range attributes {
		if attr == "Has Abstract" {
			return true
		}
	}
	return false
}
